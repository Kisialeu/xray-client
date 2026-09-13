package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

type reloadFunc func(logger *slog.Logger) ([]Profile, error)

func runDaemon(
	ctx context.Context,
	logger *slog.Logger,
	s *state,
	initial Profile,
	allProfiles []Profile,
	maxReconnects int,
	addr string,
	reload reloadFunc,
	dnsServers []string,
	authToken string,
) {
	if err := validateLoopback(addr); err != nil {
		logger.Error("invalid daemon address", "err", err)
		return
	}
	var mu sync.RWMutex
	profiles := allProfiles
	settings := newConnectionSettingsState(defaultConnectionSettings())

	getProfiles := func() []Profile {
		mu.RLock()
		defer mu.RUnlock()
		return profiles
	}

	cmdCh := make(chan vpnCmd)

	go func() {
		var (
			vpnCancel context.CancelFunc
			done      <-chan struct{}
			selected  = initial
		)

		start := func(p Profile) {
			saveLastProfile(p.Name)
			vpnCtx, cancel := context.WithCancel(ctx)
			vpnCancel = cancel
			d := make(chan struct{})
			done = d
			go func() {
				defer close(d)
				runWithReconnect(vpnCtx, logger, s, p, maxReconnects, dnsServers, settings)
			}()
		}

		stop := func() error {
			if vpnCancel == nil {
				return nil
			}
			vpnCancel()

			timeoutCtx, cancel := context.WithTimeout(context.Background(), stopTimeout)
			defer cancel()
			select {
			case <-done:
				vpnCancel = nil
				done = nil
				return nil
			case <-timeoutCtx.Done():
				return fmt.Errorf("previous session did not stop within %s", stopTimeout)
			}
		}

		if settings.snapshot().AutoConnect {
			start(initial)
		} else {
			s.setStatus(statusDisconnected)
		}

		for {
			select {
			case <-ctx.Done():
				_ = stop()
				return
			case cmd := <-cmdCh:
				var err error
				switch cmd.kind {
				case cmdSwitch:
					selected = cmd.profile
					if err = stop(); err == nil {
						start(cmd.profile)
					}
				case cmdStop:
					err = stop()
					if err == nil {
						s.setStatus(statusDisconnected)
					}
				case cmdReconnect:
					if err = stop(); err == nil {
						start(selected)
					}
				case cmdSettings:
					previous := settings.snapshot()
					updated, updateErr := settings.update(cmd.settings)
					err = updateErr
					if err == nil && !previous.AutoConnect && updated.AutoConnect {
						finished := vpnCancel == nil
						if !finished && done != nil {
							select {
							case <-done:
								finished = true
							default:
							}
						}
						if finished {
							if err = stop(); err == nil {
								start(selected)
							}
						}
					}
				}
				if cmd.done != nil {
					cmd.done <- err
					close(cmd.done)
				}
			}
		}
	}()

	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		if s.connected.Load() {
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, "ok")
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintln(w, "disconnected")
		}
	})

	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		activeProfile := ""
		if ap := s.activeProfile.Load(); ap != nil {
			activeProfile = ap.Name
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"connected":      s.connected.Load(),
			"status":         s.statusValue(),
			"active_profile": activeProfile,
			"uptime_s":       int64(time.Since(s.startAt).Seconds()),
			"bytes_in":       s.bytesIn.Load(),
			"bytes_out":      s.bytesOut.Load(),
			"reconnects":     s.reconnects.Load(),
		})
	})

	mux.HandleFunc("/settings", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{
				"auto_connect":   settings.snapshot().AutoConnect,
				"auto_reconnect": settings.snapshot().AutoReconnect,
				"active_profile": activeProfileName(s),
				"status":         s.statusValue(),
			})
		case http.MethodPost:
			var patch connectionSettingsPatch
			if err := decodeJSONBody(r.Body, &patch); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid settings body"})
				return
			}
			done := make(chan error, 1)
			select {
			case cmdCh <- vpnCmd{kind: cmdSettings, settings: patch, done: done}:
			case <-r.Context().Done():
				return
			}
			select {
			case err := <-done:
				if err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
					return
				}
			case <-r.Context().Done():
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":             true,
				"auto_connect":   settings.snapshot().AutoConnect,
				"auto_reconnect": settings.snapshot().AutoReconnect,
			})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/profiles", func(w http.ResponseWriter, _ *http.Request) {
		active := ""
		if ap := s.activeProfile.Load(); ap != nil {
			active = ap.Name
		}
		type profileEntry struct {
			Name string `json:"name"`
			Flag string `json:"flag,omitempty"`
		}
		cur := getProfiles()
		entries := make([]profileEntry, len(cur))
		for i, p := range cur {
			entries[i] = profileEntry{Name: p.Name, Flag: profileFlag(p)}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"profiles": entries,
			"active":   active,
		})
	})

	mux.HandleFunc("/connect", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Profile string `json:"profile"`
		}
		if err := decodeJSONBody(r.Body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
			return
		}
		p, ok := findProfile(getProfiles(), req.Profile)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": fmt.Sprintf("profile %q not found", req.Profile)})
			return
		}
		done := make(chan error, 1)
		select {
		case cmdCh <- vpnCmd{kind: cmdSwitch, profile: p, done: done}:
		case <-r.Context().Done():
			return
		}
		select {
		case err := <-done:
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
				return
			}
		case <-r.Context().Done():
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("/disconnect", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		done := make(chan error, 1)
		select {
		case cmdCh <- vpnCmd{kind: cmdStop, done: done}:
		case <-r.Context().Done():
			return
		}
		select {
		case err := <-done:
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
				return
			}
		case <-r.Context().Done():
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("/reconnect", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		done := make(chan error, 1)
		select {
		case cmdCh <- vpnCmd{kind: cmdReconnect, done: done}:
		case <-r.Context().Done():
			return
		}
		select {
		case err := <-done:
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
				return
			}
		case <-r.Context().Done():
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		results := pingProfiles(getProfiles())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	})

	mux.HandleFunc("/server-info", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ap := s.activeProfile.Load()
		if ap == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "not connected"})
			return
		}
		info := gatherServerInfo(*ap)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(info)
	})

	mux.HandleFunc("/refresh", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if reload == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "no subscription or config to refresh"})
			return
		}
		updated, err := reload(logger)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		mu.Lock()
		profiles = updated
		s.profiles.Store(&updated)
		mu.Unlock()
		logger.Info("profiles refreshed", "count", len(updated))
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "profiles": len(updated)})
	})

	srv := &http.Server{
		Addr:         addr,
		Handler:      controlHandler(mux, authToken),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 45 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	logger.Info("daemon listening", "addr", addr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		logger.Error("daemon server error", "err", err)
	}
}

func findProfile(profiles []Profile, name string) (Profile, bool) {
	for _, p := range profiles {
		if p.Name == name {
			return p, true
		}
	}
	return Profile{}, false
}

func activeProfileName(s *state) string {
	if profile := s.activeProfile.Load(); profile != nil {
		return profile.Name
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decodeJSONBody(r io.Reader, dst any) error {
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}
