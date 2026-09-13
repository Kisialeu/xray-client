package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testRoundTripper func(*http.Request) (*http.Response, error)

func (f testRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

var daemonTestHTTP = &http.Client{Transport: testRoundTripper(func(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer test-token")
	return http.DefaultTransport.RoundTrip(r)
})}

func TestControlAuthentication(t *testing.T) {
	h := controlHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }), "secret")
	for _, tc := range []struct {
		token, origin string
		status        int
	}{
		{"", "", 401}, {"Bearer wrong", "", 401}, {"Bearer secret", "https://attacker.example", 403}, {"Bearer secret", "", 204},
	} {
		r := httptest.NewRequest("POST", "http://127.0.0.1/disconnect", nil)
		r.Header.Set("Authorization", tc.token)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("status %d, want %d", w.Code, tc.status)
		}
	}
}

func TestControlRejectsPublicBinding(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:19099", "[::]:19099", "example.com:19099"} {
		if validateLoopback(addr) == nil {
			t.Fatalf("accepted %s", addr)
		}
	}
}

func TestControlTokenPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	token, err := createControlToken(path)
	if err != nil || len(token) != 64 {
		t.Fatalf("token creation: %v", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readControlToken(path); err == nil {
		t.Fatal("accepted readable token")
	}
}

func TestSubscriptionRejectsPlaintextAndRedirect(t *testing.T) {
	if _, err := subscriptionURL("http://example.com/secret"); err == nil {
		t.Fatal("accepted plaintext")
	}
	targetCalled := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalled = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer source.Close()
	if _, err := fetchSubscription(discardLogger, source.URL+"/private-token"); err == nil {
		t.Fatal("accepted cross-origin redirect")
	}
	if targetCalled {
		t.Fatal("redirect target contacted")
	}
}

func TestSubscriptionRedactsTokenAndRejectsOversize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, strings.Repeat("x", (1<<20)+1)) }))
	defer srv.Close()
	var logs bytes.Buffer
	_, err := fetchSubscription(slog.New(slog.NewTextHandler(&logs, nil)), srv.URL+"/private-token")
	if err == nil {
		t.Fatal("accepted oversized body")
	}
	if strings.Contains(logs.String(), "private-token") || strings.Contains(err.Error(), "private-token") {
		t.Fatal("token leaked")
	}
}

func TestCachedSubscriptionSurvivesOutage(t *testing.T) {
	old := subscriptionCacheDir
	subscriptionCacheDir = t.TempDir()
	defer func() { subscriptionCacheDir = old }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "vless://00000000-0000-4000-8000-000000000001@host:443?security=tls#cached")
	}))
	url := srv.URL
	if _, err := fetchSubscription(discardLogger, url); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	result, err := fetchSubscription(discardLogger, url)
	if err != nil || len(result.profiles) != 1 {
		t.Fatalf("cache: %v", err)
	}
}

func TestOutboundEscapesCredentials(t *testing.T) {
	raw := outboundJSON("servers", "example.com", "443", map[string]any{"password": `a"b\c`})
	var settings map[string][]map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	if settings["servers"][0]["password"] != `a"b\c` {
		t.Fatal("password changed")
	}
}

func TestTunnelCompletionCanBeObservedTwice(t *testing.T) {
	ch := make(chan error, 1)
	ch <- io.EOF
	close(ch)
	c := &Client{tunnelStopped: ch}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if c.monitorSession(ctx) != io.EOF {
		t.Fatal("missing tunnel failure")
	}
	select {
	case <-c.TunnelDone():
	case <-ctx.Done():
		t.Fatal("completion was consumed")
	}
}

func TestEndpointRejectsIPv6(t *testing.T) {
	if _, err := resolveEndpoint("2001:db8::1"); err == nil {
		t.Fatal("accepted endpoint without IPv6 route support")
	}
	ip, err := resolveEndpoint("192.0.2.1")
	if err != nil || !ip.IP.Equal(net.ParseIP("192.0.2.1")) {
		t.Fatalf("IPv4 endpoint: %v", err)
	}
}
