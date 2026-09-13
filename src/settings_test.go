package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"
)

func TestDefaultConnectionSettings(t *testing.T) {
	got := defaultConnectionSettings()
	if !got.AutoConnect || !got.AutoReconnect {
		t.Fatalf("defaults = %+v, want auto-connect and auto-reconnect enabled", got)
	}
}

func TestConnectionSettingsPatchValidation(t *testing.T) {
	current := defaultConnectionSettings()
	if _, err := (connectionSettingsPatch{}).apply(current); err == nil {
		t.Fatal("empty settings patch was accepted")
	}
	value := false
	got, err := (connectionSettingsPatch{AutoReconnect: &value}).apply(current)
	if err != nil {
		t.Fatalf("valid settings patch rejected: %v", err)
	}
	if !got.AutoConnect || got.AutoReconnect {
		t.Fatalf("patch result = %+v, want only auto-reconnect disabled", got)
	}
}

func TestConnectionSettingsStateSignalsChanges(t *testing.T) {
	settings := newConnectionSettingsState(defaultConnectionSettings())
	oldSignal := settings.changeSignal()
	value := false
	if _, err := settings.update(connectionSettingsPatch{AutoReconnect: &value}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-oldSignal:
	case <-time.After(time.Second):
		t.Fatal("settings update did not signal waiters")
	}
	if got := settings.snapshot(); !got.AutoConnect || got.AutoReconnect {
		t.Fatalf("updated settings = %+v", got)
	}
}

func TestRunWithReconnectStopsWhenAutoReconnectDisabled(t *testing.T) {
	settings := newConnectionSettingsState(defaultConnectionSettings())
	s := &state{startAt: time.Now()}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		runWithReconnect(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), s,
			Profile{Name: "invalid", Link: "vless://00000000-0000-4000-8000-000000000001@invalid-host-for-test:443?security=tls"},
			0, nil, settings)
		close(done)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && s.statusValue() != statusReconnecting {
		time.Sleep(10 * time.Millisecond)
	}
	if s.statusValue() != statusReconnecting {
		t.Fatalf("status = %q, want reconnecting before disabling auto-reconnect", s.statusValue())
	}
	value := false
	if _, err := settings.update(connectionSettingsPatch{AutoReconnect: &value}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runWithReconnect continued after auto-reconnect was disabled")
	}
	if got := s.statusValue(); got != statusFailed {
		t.Errorf("final status = %q, want operation_failed", got)
	}
}

func TestDaemonSettingsAndReconnectAPI(t *testing.T) {
	addr, _, cancel := startTestDaemon(t)
	defer cancel()

	resp, err := daemonTestHTTP.Get("http://" + addr + "/settings")
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		AutoConnect   bool             `json:"auto_connect"`
		AutoReconnect bool             `json:"auto_reconnect"`
		Status        connectionStatus `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&settings); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !settings.AutoConnect || !settings.AutoReconnect {
		t.Fatalf("GET /settings: status=%d settings=%+v", resp.StatusCode, settings)
	}

	body := bytes.NewBufferString(`{"auto_reconnect":false}`)
	resp, err = daemonTestHTTP.Post("http://"+addr+"/settings", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	var updated map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&updated); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || updated["auto_reconnect"] != false {
		t.Fatalf("POST /settings: status=%d response=%v", resp.StatusCode, updated)
	}

	resp, err = daemonTestHTTP.Post("http://"+addr+"/settings", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty settings patch status=%d, want 400", resp.StatusCode)
	}

	resp, err = daemonTestHTTP.Do(mustSettingsRequest(t, http.MethodPut, addr, `{"auto_connect":false}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("PUT /settings status=%d, want 405", resp.StatusCode)
	}

	resp, err = daemonTestHTTP.Get("http://" + addr + "/reconnect")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /reconnect status=%d, want 405", resp.StatusCode)
	}

	resp, err = daemonTestHTTP.Post("http://"+addr+"/reconnect", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /reconnect status=%d, want 200", resp.StatusCode)
	}
}

func mustSettingsRequest(t *testing.T, method, addr, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, "http://"+addr+"/settings", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestDaemonSettingsRequiresAuthentication(t *testing.T) {
	addr, _, cancel := startTestDaemon(t)
	defer cancel()

	resp, err := http.Get("http://" + addr + "/settings")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET /settings status=%d, want 401", resp.StatusCode)
	}
}
