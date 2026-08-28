package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// ── humanBytes ────────────────────────────────────────────────────────────────

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.00 KiB"},
		{1536, "1.50 KiB"},
		{10240, "10.00 KiB"},
		{1024 * 1024, "1.00 MiB"},
		{1.5 * 1024 * 1024, "1.50 MiB"},
		{1024 * 1024 * 1024, "1.00 GiB"},
		{2.5 * 1024 * 1024 * 1024, "2.50 GiB"},
	}
	for _, c := range cases {
		got := humanBytes(c.in)
		if got != c.want {
			t.Errorf("humanBytes(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHumanBytes_Negative(t *testing.T) {
	got := humanBytes(-1)
	if got != "-1 B" {
		t.Errorf("humanBytes(-1) = %q, want \"-1 B\"", got)
	}
}

// ── backoffDuration ──────────────────────────────────────────────────────────

func TestBackoff_Exponential(t *testing.T) {
	init := time.Second
	max := 60 * time.Second

	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 16 * time.Second},
		{6, 32 * time.Second},
		{7, 60 * time.Second}, // capped
	}
	for _, c := range cases {
		got := backoffDuration(c.attempt, init, max)
		if got != c.want {
			t.Errorf("backoffDuration(%d, 1s, 60s) = %v, want %v", c.attempt, got, c.want)
		}
	}
}

func TestBackoff_CapsAtMax(t *testing.T) {
	got := backoffDuration(100, time.Second, 30*time.Second)
	if got != 30*time.Second {
		t.Errorf("got %v, want 30s cap", got)
	}
}

func TestBackoff_MaxEqualInitial(t *testing.T) {
	got := backoffDuration(1, 5*time.Second, 5*time.Second)
	if got != 5*time.Second {
		t.Errorf("got %v", got)
	}
}

func TestBackoff_LargeAttemptNoPanic(t *testing.T) {
	got := backoffDuration(1000, time.Second, time.Minute)
	if got != time.Minute {
		t.Errorf("got %v, want 1m cap", got)
	}
}

func TestBackoff_SmallIntervals(t *testing.T) {
	got := backoffDuration(1, 100*time.Millisecond, 500*time.Millisecond)
	if got != 100*time.Millisecond {
		t.Errorf("got %v, want 100ms", got)
	}
	got = backoffDuration(3, 100*time.Millisecond, 500*time.Millisecond)
	if got != 400*time.Millisecond {
		t.Errorf("got %v, want 400ms", got)
	}
}

// ── buildLogger ───────────────────────────────────────────────────────────────

func TestBuildLogger_Levels(t *testing.T) {
	for _, lvl := range []string{"debug", "info", "warn", "error", "unknown"} {
		l := buildLogger(lvl)
		if l == nil {
			t.Errorf("buildLogger(%q) returned nil", lvl)
		}
	}
}

func TestBuildLogger_Debug(t *testing.T) {
	var buf strings.Builder
	l := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	l.Debug("test")
	if !strings.Contains(buf.String(), "test") {
		t.Error("debug message not logged")
	}
}

func TestBuildLogger_UnknownDefaultsToInfo(t *testing.T) {
	l := buildLogger("garbage")
	if l == nil {
		t.Fatal("buildLogger returned nil")
	}
}

// ── status server ─────────────────────────────────────────────────────────────

func TestStatusServer_Health_Connected(t *testing.T) {
	s := &state{}
	s.connected.Store(true)
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startStatusServer(ctx, addr, s)
	waitReady(t, addr)

	resp, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "ok") {
		t.Errorf("body = %q, want 'ok'", body)
	}
}

func TestStatusServer_Health_Disconnected(t *testing.T) {
	s := &state{}
	s.connected.Store(false)
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startStatusServer(ctx, addr, s)
	waitReady(t, addr)

	resp, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "disconnected") {
		t.Errorf("body = %q, want 'disconnected'", body)
	}
}

func TestStatusServer_Status_JSON(t *testing.T) {
	s := &state{startAt: time.Now()}
	s.connected.Store(true)
	s.bytesIn.Store(1234)
	s.bytesOut.Store(5678)
	s.reconnects.Store(2)
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startStatusServer(ctx, addr, s)
	waitReady(t, addr)

	resp, err := http.Get("http://" + addr + "/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer resp.Body.Close()

	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if got["connected"] != true {
		t.Errorf("connected = %v, want true", got["connected"])
	}
	if got["bytes_in"].(float64) != 1234 {
		t.Errorf("bytes_in = %v, want 1234", got["bytes_in"])
	}
	if got["bytes_out"].(float64) != 5678 {
		t.Errorf("bytes_out = %v, want 5678", got["bytes_out"])
	}
	if got["reconnects"].(float64) != 2 {
		t.Errorf("reconnects = %v, want 2", got["reconnects"])
	}
	if _, ok := got["uptime_s"]; !ok {
		t.Error("missing uptime_s field")
	}
}

func TestStatusServer_Status_ContentType(t *testing.T) {
	s := &state{startAt: time.Now()}
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startStatusServer(ctx, addr, s)
	waitReady(t, addr)

	resp, err := http.Get("http://" + addr + "/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestStatusServer_Shutdown(t *testing.T) {
	s := &state{}
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	startStatusServer(ctx, addr, s)
	waitReady(t, addr)

	cancel()
	time.Sleep(100 * time.Millisecond)

	_, err := http.Get("http://" + addr + "/health")
	if err == nil {
		t.Error("expected error after shutdown")
	}
}

func TestStatusServer_UnknownPath(t *testing.T) {
	s := &state{}
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startStatusServer(ctx, addr, s)
	waitReady(t, addr)

	resp, err := http.Get("http://" + addr + "/nonexistent")
	if err != nil {
		t.Fatalf("GET /nonexistent: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// ── runWithReconnect ──────────────────────────────────────────────────────────

func TestRunWithReconnect_StopsOnContextCancel(t *testing.T) {
	s := &state{startAt: time.Now()}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	profile := Profile{Name: "test", Link: "vless://invalid"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		runWithReconnect(ctx, logger, s, profile, 0, nil)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runWithReconnect did not exit after context cancel")
	}
}

func TestRunWithReconnect_MaxAttemptsRespected(t *testing.T) {
	s := &state{startAt: time.Now()}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	profile := Profile{Name: "test", Link: "vless://invalid-host-that-does-not-exist"}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		runWithReconnect(ctx, logger, s, profile, 1, nil)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runWithReconnect did not respect maxAttempts")
	}
}

func TestRunWithReconnect_ReconnectCountIncremented(t *testing.T) {
	s := &state{startAt: time.Now()}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	profile := Profile{Name: "test", Link: "vless://invalid"}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		runWithReconnect(ctx, logger, s, profile, 2, nil)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runWithReconnect timed out")
	}

	if s.reconnects.Load() < 1 {
		t.Error("expected at least 1 reconnect attempt")
	}
}

// ── state atomic fields ───────────────────────────────────────────────────────

func TestState_AtomicFields(t *testing.T) {
	s := &state{}

	s.connected.Store(true)
	if !s.connected.Load() {
		t.Error("connected should be true")
	}
	s.connected.Store(false)
	if s.connected.Load() {
		t.Error("connected should be false")
	}

	s.bytesIn.Store(999)
	if s.bytesIn.Load() != 999 {
		t.Error("bytesIn mismatch")
	}

	s.bytesOut.Store(777)
	if s.bytesOut.Load() != 777 {
		t.Error("bytesOut mismatch")
	}

	s.reconnects.Add(1)
	s.reconnects.Add(1)
	if s.reconnects.Load() != 2 {
		t.Error("reconnects should be 2")
	}

	p := &Profile{Name: "x", Link: "vless://x"}
	s.activeProfile.Store(p)
	loaded := s.activeProfile.Load()
	if loaded == nil || loaded.Name != "x" {
		t.Error("activeProfile mismatch")
	}

	now := time.Now().UnixNano()
	s.connectedAt.Store(now)
	if s.connectedAt.Load() != now {
		t.Error("connectedAt mismatch")
	}
}

func TestState_ZeroValue(t *testing.T) {
	s := &state{}
	if s.connected.Load() {
		t.Error("zero state connected should be false")
	}
	if s.bytesIn.Load() != 0 {
		t.Error("zero state bytesIn should be 0")
	}
	if s.reconnects.Load() != 0 {
		t.Error("zero state reconnects should be 0")
	}
	if s.activeProfile.Load() != nil {
		t.Error("zero state activeProfile should be nil")
	}
	if s.connectedAt.Load() != 0 {
		t.Error("zero state connectedAt should be 0")
	}
}

// ── tray_darwin helpers ───────────────────────────────────────────────────────

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{1 * time.Second, "1s"},
		{5 * time.Second, "5s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m 00s"},
		{65 * time.Second, "1m 05s"},
		{600 * time.Second, "10m 00s"},
		{3600 * time.Second, "1h 00m 00s"},
		{3661 * time.Second, "1h 01m 01s"},
		{7200 * time.Second, "2h 00m 00s"},
		{86400 * time.Second, "24h 00m 00s"},
	}
	for _, c := range cases {
		got := formatDuration(c.d)
		if got != c.want {
			t.Errorf("formatDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestCircleIcon_ReturnsPNG(t *testing.T) {
	for _, filled := range []bool{true, false} {
		icon := circleIcon(filled)
		if len(icon) == 0 {
			t.Errorf("circleIcon(%v) returned empty slice", filled)
		}
		if len(icon) < 8 {
			t.Fatalf("circleIcon(%v) too short for PNG header", filled)
		}
		// PNG magic bytes
		if icon[0] != 0x89 || icon[1] != 0x50 || icon[2] != 0x4E || icon[3] != 0x47 {
			t.Errorf("circleIcon(%v) does not start with PNG magic bytes", filled)
		}
	}
}

func TestIconConnDisc_AreDifferent(t *testing.T) {
	conn := iconConn()
	disc := iconDisc()
	if string(conn) == string(disc) {
		t.Error("connected and disconnected icons should be different")
	}
}

func TestIconConn_Cached(t *testing.T) {
	a := iconConn()
	b := iconConn()
	if &a[0] != &b[0] {
		t.Error("iconConn should return the same cached slice")
	}
}

func TestIconDisc_Cached(t *testing.T) {
	a := iconDisc()
	b := iconDisc()
	if &a[0] != &b[0] {
		t.Error("iconDisc should return the same cached slice")
	}
}

// ── test helpers ──────────────────────────────────────────────────────────────

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freeAddr: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func waitReady(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server at %s did not become ready", addr)
}
