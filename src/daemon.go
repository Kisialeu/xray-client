package main

import (
	"context"
	"encoding/json"
	"fmt"
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
) {
	var mu sync.RWMutex
	profiles := allProfiles

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
		)

		start := func(p Profile) {
			saveLastProfile(p.Name)
			vpnCtx, cancel := context.WithCancel(ctx)
			vpnCancel = cancel
			d := make(chan struct{})
			done = d
			go func() {
				defer close(d)
				runWithReconnect(vpnCtx, logger, s, p, maxReconnects)
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

		start(initial)

		for {
			select {
			case <-ctx.Done():
				_ = stop()
				return
			case cmd := <-cmdCh:
				var err error
				switch cmd.kind {
				case cmdSwitch:
					if err = stop(); err == nil {
						start(cmd.profile)
					}
				case cmdStop:
					err = stop()
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
			"active_profile": activeProfile,
			"uptime_s":       int64(time.Since(s.startAt).Seconds()),
			"bytes_in":       s.bytesIn.Load(),
			"bytes_out":      s.bytesOut.Load(),
			"reconnects":     s.reconnects.Load(),
		})
	})

	mux.HandleFunc("/profiles", func(w http.ResponseWriter, _ *http.Request) {
		active := ""
		if ap := s.activeProfile.Load(); ap != nil {
			active = ap.Name
		}
		type profileEntry struct {
			Name string `json:"name"`
		}
		cur := getProfiles()
		entries := make([]profileEntry, len(cur))
		for i, p := range cur {
			entries[i] = profileEntry{Name: p.Name}
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
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
		if err := <-done; err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
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
		if err := <-done; err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
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
		mu.Unlock()
		logger.Info("profiles refreshed", "count", len(updated))
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "profiles": len(updated)})
	})

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
