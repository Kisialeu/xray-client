package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
)

func TestExtractHostPort_VLESS(t *testing.T) {
	link := "vless://uuid@example.com:443?type=tcp#MyServer"
	got := extractHostPort(link)
	if got != "example.com:443" {
		t.Errorf("got %q, want example.com:443", got)
	}
}

func TestExtractHostPort_Trojan(t *testing.T) {
	link := "trojan://password@trojan.example.com:8443?sni=example.com#Trojan"
	got := extractHostPort(link)
	if got != "trojan.example.com:8443" {
		t.Errorf("got %q, want trojan.example.com:8443", got)
	}
}

func TestExtractHostPort_SS(t *testing.T) {
	link := "ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ@ss.example.com:8388#SS"
	got := extractHostPort(link)
	if got != "ss.example.com:8388" {
		t.Errorf("got %q, want ss.example.com:8388", got)
	}
}

func TestExtractHostPort_VMess(t *testing.T) {
	obj := map[string]any{
		"add":  "vmess.example.com",
		"port": 443,
		"id":   "test-uuid",
	}
	data, _ := json.Marshal(obj)
	link := "vmess://" + base64.StdEncoding.EncodeToString(data)
	got := extractHostPort(link)
	if got != "vmess.example.com:443" {
		t.Errorf("got %q, want vmess.example.com:443", got)
	}
}

func TestExtractHostPort_VMessStringPort(t *testing.T) {
	obj := map[string]any{
		"add":  "vmess.example.com",
		"port": "8080",
		"id":   "test-uuid",
	}
	data, _ := json.Marshal(obj)
	link := "vmess://" + base64.StdEncoding.EncodeToString(data)
	got := extractHostPort(link)
	if got != "vmess.example.com:8080" {
		t.Errorf("got %q, want vmess.example.com:8080", got)
	}
}

func TestExtractHostPort_DefaultPort(t *testing.T) {
	link := "vless://uuid@example.com?type=tcp#NoPort"
	got := extractHostPort(link)
	if got != "example.com:443" {
		t.Errorf("got %q, want example.com:443", got)
	}
}

func TestExtractHostPort_Empty(t *testing.T) {
	if got := extractHostPort(""); got != "" {
		t.Errorf("got %q for empty string", got)
	}
	if got := extractHostPort("invalid"); got != "" {
		t.Errorf("got %q for invalid link", got)
	}
}

func TestPingProfiles_Reachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	profiles := []Profile{
		{Name: "local", Link: fmt.Sprintf("vless://uuid@%s#local", ln.Addr().String())},
	}
	results := pingProfiles(profiles)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Name != "local" {
		t.Errorf("name = %q", results[0].Name)
	}
	if results[0].LatencyMs < 0 {
		t.Errorf("latency = %d, want >= 0 for reachable server", results[0].LatencyMs)
	}
}

func TestPingProfiles_InvalidLink(t *testing.T) {
	profiles := []Profile{
		{Name: "bad", Link: "not-a-link"},
	}
	results := pingProfiles(profiles)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].LatencyMs != -1 {
		t.Errorf("latency = %d, want -1 for invalid link", results[0].LatencyMs)
	}
}

func TestDaemon_Ping(t *testing.T) {
	addr, _, cancel := startTestDaemon(t)
	defer cancel()

	resp, err := http.Get("http://" + addr + "/ping")
	if err != nil {
		t.Fatalf("GET /ping: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var got struct {
		Results []PingResult `json:"results"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Results) != 2 {
		t.Fatalf("got %d results, want 2", len(got.Results))
	}
	if got.Results[0].Name != "alpha" {
		t.Errorf("results[0].name = %q, want alpha", got.Results[0].Name)
	}
}

func TestDaemon_Ping_MethodNotAllowed(t *testing.T) {
	addr, _, cancel := startTestDaemon(t)
	defer cancel()

	resp, err := http.Post("http://"+addr+"/ping", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /ping: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}
