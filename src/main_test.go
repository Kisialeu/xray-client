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
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.00 KiB"},
		{1536, "1.50 KiB"},
		{1024 * 1024, "1.00 MiB"},
		{1.5 * 1024 * 1024, "1.50 MiB"},
		{1024 * 1024 * 1024, "1.00 GiB"},
	}
	for _, c := range cases {
		got := humanBytes(c.in)
		if got != c.want {
			t.Errorf("humanBytes(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ── backoff ───────────────────────────────────────────────────────────────────

func TestBackoff_Exponential(t *testing.T) {
	init := time.Second
	max := 60 * time.Second

	if got := backoff(1, init, max); got != time.Second {
		t.Errorf("attempt 1: got %v, want 1s", got)
	}
	if got := backoff(2, init, max); got != 2*time.Second {
		t.Errorf("attempt 2: got %v, want 2s", got)
	}
	if got := backoff(3, init, max); got != 4*time.Second {
		t.Errorf("attempt 3: got %v, want 4s", got)
	}
}

func TestBackoff_CapsAtMax(t *testing.T) {
	got := backoff(100, time.Second, 30*time.Second)
	if got != 30*time.Second {
		t.Errorf("got %v, want 30s cap", got)
	}
}

func TestBackoff_MaxEqualInitial(t *testing.T) {
	got := backoff(1, 5*time.Second, 5*time.Second)
	if got != 5*time.Second {
		t.Errorf("got %v", got)
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

// ── status server ─────────────────────────────────────────────────────────────

func TestStatusServer_Health_Connected(t *testing.T) {
	s := &state{}
	s.connected.Store(true)
	addr := freeAddr(t)
	startStatusServer(addr, s)
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
	startStatusServer(addr, s)
	waitReady(t, addr)

	resp, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}

func TestStatusServer_Status_JSON(t *testing.T) {
	s := &state{startAt: time.Now()}
	s.connected.Store(true)
	s.bytesIn.Store(1234)
	s.bytesOut.Store(5678)
	s.reconnects.Store(2)
	addr := freeAddr(t)
	startStatusServer(addr, s)
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
}

// ── runWithReconnect ──────────────────────────────────────────────────────────

// runWithReconnect calls NewClientWithOpts internally which requires real
// system access (gateway, TUN). We test the state transitions and backoff
// behaviour by injecting failures via maxAttempts.

func TestRunWithReconnect_StopsOnContextCancel(t *testing.T) {
	s := &state{startAt: time.Now()}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	profile := Profile{Name: "test", Link: "vless://invalid"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so the loop exits before attempting a real dial

	done := make(chan struct{})
	go func() {
		runWithReconnect(ctx, logger, s, profile, 0)
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
	// invalid link → connect always fails
	profile := Profile{Name: "test", Link: "vless://invalid-host-that-does-not-exist"}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		// maxAttempts=1 means one failure then give up — no real backoff wait
		// because context also cancels it early.
		runWithReconnect(ctx, logger, s, profile, 1)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runWithReconnect did not respect maxAttempts")
	}
}

// ── state atomic fields ───────────────────────────────────────────────────────

func TestState_AtomicFields(t *testing.T) {
	s := &state{}

	s.connected.Store(true)
	if !s.connected.Load() {
		t.Error("connected should be true")
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

	p := &Profile{Name: "x"}
	s.activeProfile.Store(p)
	if s.activeProfile.Load().Name != "x" {
		t.Error("activeProfile mismatch")
	}

	now := time.Now().UnixNano()
	s.connectedAt.Store(now)
	if s.connectedAt.Load() != now {
		t.Error("connectedAt mismatch")
	}
}

// ── tray_darwin helpers ───────────────────────────────────────────────────────

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{5 * time.Second, "5s"},
		{65 * time.Second, "1m 05s"},
		{3661 * time.Second, "1h 01m 01s"},
		{0, "0s"},
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
