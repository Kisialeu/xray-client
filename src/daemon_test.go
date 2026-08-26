package main

import (
	"bytes"
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

func startTestDaemon(t *testing.T) (addr string, s *state, cancel context.CancelFunc) {
	t.Helper()
	s = &state{startAt: time.Now()}
	profiles := []Profile{
		{Name: "alpha", Link: "vless://alpha"},
		{Name: "beta", Link: "vless://beta"},
	}
	initial := profiles[0]

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr = ln.Addr().String()
	ln.Close()

	ctx, c := context.WithCancel(context.Background())
	cancel = c
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	go runDaemon(ctx, logger, s, initial, profiles, 1, addr)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("daemon at %s did not become ready", addr)
	return
}

// ── GET /health ──────────────────────────────────────────────────────────────

func TestDaemon_Health_Disconnected(t *testing.T) {
	addr, _, cancel := startTestDaemon(t)
	defer cancel()

	resp, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}

func TestDaemon_Health_Connected(t *testing.T) {
	addr, s, cancel := startTestDaemon(t)
	defer cancel()

	s.connected.Store(true)
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

// ── GET /status ──────────────────────────────────────────────────────────────

func TestDaemon_Status_JSON(t *testing.T) {
	addr, s, cancel := startTestDaemon(t)
	defer cancel()

	s.connected.Store(true)
	s.bytesIn.Store(1000)
	s.bytesOut.Store(2000)
	p := &Profile{Name: "alpha"}
	s.activeProfile.Store(p)

	resp, err := http.Get("http://" + addr + "/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer resp.Body.Close()

	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["connected"] != true {
		t.Errorf("connected = %v", got["connected"])
	}
	if got["active_profile"] != "alpha" {
		t.Errorf("active_profile = %v", got["active_profile"])
	}
	if got["bytes_in"].(float64) != 1000 {
		t.Errorf("bytes_in = %v", got["bytes_in"])
	}
	if got["bytes_out"].(float64) != 2000 {
		t.Errorf("bytes_out = %v", got["bytes_out"])
	}
}

func TestDaemon_Status_Disconnected(t *testing.T) {
	addr, _, cancel := startTestDaemon(t)
	defer cancel()

	resp, err := http.Get("http://" + addr + "/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer resp.Body.Close()

	var got map[string]any
	json.NewDecoder(resp.Body).Decode(&got)
	if got["active_profile"] != "" {
		t.Errorf("active_profile should be empty when disconnected, got %v", got["active_profile"])
	}
}

func TestDaemon_Status_ContentType(t *testing.T) {
	addr, _, cancel := startTestDaemon(t)
	defer cancel()

	resp, _ := http.Get("http://" + addr + "/status")
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
}

// ── GET /profiles ────────────────────────────────────────────────────────────

func TestDaemon_Profiles(t *testing.T) {
	addr, _, cancel := startTestDaemon(t)
	defer cancel()

	resp, err := http.Get("http://" + addr + "/profiles")
	if err != nil {
		t.Fatalf("GET /profiles: %v", err)
	}
	defer resp.Body.Close()

	var got struct {
		Profiles []struct {
			Name string `json:"name"`
		} `json:"profiles"`
		Active string `json:"active"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(got.Profiles))
	}
	if got.Profiles[0].Name != "alpha" {
		t.Errorf("profiles[0] = %q, want alpha", got.Profiles[0].Name)
	}
	if got.Profiles[1].Name != "beta" {
		t.Errorf("profiles[1] = %q, want beta", got.Profiles[1].Name)
	}
}

func TestDaemon_Profiles_ShowsActive(t *testing.T) {
	addr, s, cancel := startTestDaemon(t)
	defer cancel()

	p := &Profile{Name: "beta"}
	s.activeProfile.Store(p)

	resp, _ := http.Get("http://" + addr + "/profiles")
	defer resp.Body.Close()

	var got struct {
		Active string `json:"active"`
	}
	json.NewDecoder(resp.Body).Decode(&got)
	if got.Active != "beta" {
		t.Errorf("active = %q, want beta", got.Active)
	}
}

// ── POST /connect ────────────────────────────────────────────────────────────

func TestDaemon_Connect_UnknownProfile(t *testing.T) {
	addr, _, cancel := startTestDaemon(t)
	defer cancel()

	body, _ := json.Marshal(map[string]string{"profile": "nonexistent"})
	resp, err := http.Post("http://"+addr+"/connect", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /connect: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	var got map[string]any
	json.NewDecoder(resp.Body).Decode(&got)
	if got["ok"] != false {
		t.Errorf("ok = %v, want false", got["ok"])
	}
	errMsg, _ := got["error"].(string)
	if !strings.Contains(errMsg, "not found") {
		t.Errorf("error = %q, want 'not found'", errMsg)
	}
}

func TestDaemon_Connect_InvalidBody(t *testing.T) {
	addr, _, cancel := startTestDaemon(t)
	defer cancel()

	resp, err := http.Post("http://"+addr+"/connect", "application/json", strings.NewReader("not json"))
	if err != nil {
		t.Fatalf("POST /connect: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestDaemon_Connect_MethodNotAllowed(t *testing.T) {
	addr, _, cancel := startTestDaemon(t)
	defer cancel()

	resp, err := http.Get("http://" + addr + "/connect")
	if err != nil {
		t.Fatalf("GET /connect: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

// ── POST /disconnect ─────────────────────────────────────────────────────────

func TestDaemon_Disconnect_MethodNotAllowed(t *testing.T) {
	addr, _, cancel := startTestDaemon(t)
	defer cancel()

	resp, err := http.Get("http://" + addr + "/disconnect")
	if err != nil {
		t.Fatalf("GET /disconnect: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

func TestDaemon_Disconnect_WhenAlreadyStopped(t *testing.T) {
	addr, _, cancel := startTestDaemon(t)
	defer cancel()

	// Give daemon time to start and fail (invalid link)
	time.Sleep(500 * time.Millisecond)

	resp, err := http.Post("http://"+addr+"/disconnect", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /disconnect: %v", err)
	}
	defer resp.Body.Close()

	var got map[string]any
	json.NewDecoder(resp.Body).Decode(&got)
	if got["ok"] != true {
		t.Errorf("ok = %v, want true (disconnect when already stopped should succeed)", got["ok"])
	}
}

// ── findProfile ──────────────────────────────────────────────────────────────

func TestFindProfile_Found(t *testing.T) {
	profiles := []Profile{
		{Name: "a", Link: "vless://a"},
		{Name: "b", Link: "vless://b"},
	}
	p, ok := findProfile(profiles, "b")
	if !ok {
		t.Fatal("expected to find profile b")
	}
	if p.Name != "b" {
		t.Errorf("got %q", p.Name)
	}
}

func TestFindProfile_NotFound(t *testing.T) {
	profiles := []Profile{{Name: "a", Link: "vless://a"}}
	_, ok := findProfile(profiles, "missing")
	if ok {
		t.Fatal("should not find missing profile")
	}
}

func TestFindProfile_EmptyList(t *testing.T) {
	_, ok := findProfile(nil, "any")
	if ok {
		t.Fatal("should not find in empty list")
	}
}

// ── writeJSON ────────────────────────────────────────────────────────────────

func TestWriteJSON(t *testing.T) {
	addr, _, cancel := startTestDaemon(t)
	defer cancel()

	// /profiles uses writeJSON under the hood via json.Encode
	resp, _ := http.Get("http://" + addr + "/profiles")
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}
