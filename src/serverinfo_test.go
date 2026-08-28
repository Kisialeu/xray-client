package main

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestExtractProtocol(t *testing.T) {
	tests := []struct {
		link string
		want string
	}{
		{"vless://uuid@host:443#name", "VLESS"},
		{"vmess://base64data", "VMess"},
		{"trojan://pass@host:443#name", "Trojan"},
		{"ss://base64@host:8388#name", "Shadowsocks"},
		{"", ""},
	}
	for _, tt := range tests {
		got := extractProtocol(tt.link)
		if got != tt.want {
			t.Errorf("extractProtocol(%q) = %q, want %q", tt.link, got, tt.want)
		}
	}
}

func TestDaemon_ServerInfo_NotConnected(t *testing.T) {
	addr, _, cancel := startTestDaemon(t)
	defer cancel()

	resp, err := http.Get("http://" + addr + "/server-info")
	if err != nil {
		t.Fatalf("GET /server-info: %v", err)
	}
	defer resp.Body.Close()

	var result struct {
		Error string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	if result.Error == "" {
		t.Error("expected error when not connected")
	}
}

func TestDaemon_ServerInfo_MethodNotAllowed(t *testing.T) {
	addr, _, cancel := startTestDaemon(t)
	defer cancel()

	resp, err := http.Post("http://"+addr+"/server-info", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /server-info: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}
