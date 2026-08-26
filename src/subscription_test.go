package main

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseSubscription_Base64(t *testing.T) {
	links := "vless://user@host1:443#Server-1\nvless://user@host2:443#Server-2\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(links))

	profiles, err := parseSubscription(encoded)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(profiles))
	}
	if profiles[0].Name != "Server-1" {
		t.Errorf("profile[0].Name = %q, want Server-1", profiles[0].Name)
	}
	if profiles[1].Name != "Server-2" {
		t.Errorf("profile[1].Name = %q, want Server-2", profiles[1].Name)
	}
}

func TestParseSubscription_Base64NoPadding(t *testing.T) {
	links := "vless://user@host1:443#A\nvless://user@host2:443#B\n"
	encoded := base64.RawStdEncoding.EncodeToString([]byte(links))

	profiles, err := parseSubscription(encoded)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(profiles))
	}
}

func TestParseSubscription_Base64URLSafe(t *testing.T) {
	links := "vless://user@host1:443#A\nvless://user@host2:443#B\n"
	encoded := base64.URLEncoding.EncodeToString([]byte(links))

	profiles, err := parseSubscription(encoded)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(profiles))
	}
}

func TestParseSubscription_Base64WithNewlines(t *testing.T) {
	links := "vless://user@host1:443#A\nvless://user@host2:443#B\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(links))
	// Insert MIME-style line breaks
	wrapped := ""
	for i := 0; i < len(encoded); i += 76 {
		end := i + 76
		if end > len(encoded) {
			end = len(encoded)
		}
		wrapped += encoded[i:end] + "\n"
	}

	profiles, err := parseSubscription(wrapped)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(profiles))
	}
}

func TestParseSubscription_PlainText(t *testing.T) {
	raw := "vless://user@host1:443#Plain\n# comment\nvless://user@host2:443#Two\n"

	profiles, err := parseSubscription(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(profiles))
	}
	if profiles[0].Name != "Plain" {
		t.Errorf("got %q, want Plain", profiles[0].Name)
	}
}

func TestParseSubscription_Dedup(t *testing.T) {
	raw := "vless://user@host:443#S\nvless://user@host:443#S\n"

	profiles, err := parseSubscription(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("got %d profiles, want 1 (dedup)", len(profiles))
	}
}

func TestParseSubscription_Empty(t *testing.T) {
	_, err := parseSubscription("")
	if err == nil {
		t.Fatal("expected error for empty subscription")
	}
}

func TestParseSubscription_SkipsBlankAndComment(t *testing.T) {
	raw := "\n\n# comment\n\n"
	_, err := parseSubscription(raw)
	if err == nil {
		t.Fatal("expected error for comment-only subscription")
	}
}

func TestParseSubscription_RejectsGarbageHTML(t *testing.T) {
	raw := "<html><body>404 Not Found</body></html>"
	_, err := parseSubscription(raw)
	if err == nil {
		t.Fatal("expected error for HTML response")
	}
}

func TestLinkToProfile_VLESSWithFragment(t *testing.T) {
	p := linkToProfile("vless://user@host:443?type=tcp#My-Server")
	if p.Name != "My-Server" {
		t.Errorf("Name = %q, want My-Server", p.Name)
	}
	if !strings.HasPrefix(p.Link, "vless://") {
		t.Errorf("Link = %q, expected vless:// prefix", p.Link)
	}
}

func TestLinkToProfile_NoFragment(t *testing.T) {
	p := linkToProfile("vless://user@example.com:443?type=tcp")
	if p.Name != "example.com" {
		t.Errorf("Name = %q, want example.com", p.Name)
	}
}

func TestLinkToProfile_VMessBase64(t *testing.T) {
	obj := `{"v":"2","ps":"Tokyo Server","add":"1.2.3.4","port":"443","id":"abc"}`
	encoded := base64.StdEncoding.EncodeToString([]byte(obj))
	link := "vmess://" + encoded

	p := linkToProfile(link)
	if p.Name != "Tokyo Server" {
		t.Errorf("Name = %q, want Tokyo Server", p.Name)
	}
}

func TestLinkToProfile_VMessEmptyPS(t *testing.T) {
	obj := `{"v":"2","ps":"","add":"","port":"443","id":"abc"}`
	encoded := base64.StdEncoding.EncodeToString([]byte(obj))
	link := "vmess://" + encoded

	p := linkToProfile(link)
	if p.Name == "" {
		t.Error("expected non-empty name for vmess with empty ps+add")
	}
}

func TestExtractFragment_URLEncoded(t *testing.T) {
	// url.Parse decodes the fragment automatically
	f := extractFragment("vless://user@host:443#Hello%20World")
	if f != "Hello World" {
		t.Errorf("got %q, want Hello World", f)
	}
}

func TestIsProxyLink(t *testing.T) {
	if !isProxyLink("vless://user@host:443") {
		t.Error("should recognize vless://")
	}
	if !isProxyLink("vmess://base64data") {
		t.Error("should recognize vmess://")
	}
	if !isProxyLink("trojan://user@host:443") {
		t.Error("should recognize trojan://")
	}
	if !isProxyLink("ss://base64@host:443") {
		t.Error("should recognize ss://")
	}
	if isProxyLink("<html>garbage</html>") {
		t.Error("should reject HTML")
	}
	if isProxyLink("http://example.com") {
		t.Error("should reject http://")
	}
}

func TestMergeProfiles_DedupByLink(t *testing.T) {
	inline := []Profile{{Name: "my-server", Link: "vless://same-link"}}
	sub := []Profile{{Name: "different-name", Link: "vless://same-link"}}

	merged := mergeProfiles(inline, sub)
	if len(merged) != 1 {
		t.Fatalf("got %d profiles, want 1 (same link should dedup)", len(merged))
	}
	if merged[0].Name != "my-server" {
		t.Errorf("should keep inline profile name, got %q", merged[0].Name)
	}
}

func TestMergeProfiles_DedupByName(t *testing.T) {
	inline := []Profile{{Name: "server", Link: "vless://link-a"}}
	sub := []Profile{{Name: "server", Link: "vless://link-b"}}

	merged := mergeProfiles(inline, sub)
	if len(merged) != 1 {
		t.Fatalf("got %d profiles, want 1 (same name should dedup)", len(merged))
	}
}

func TestMergeProfiles_AppendsNew(t *testing.T) {
	inline := []Profile{{Name: "a", Link: "vless://a"}}
	sub := []Profile{{Name: "b", Link: "vless://b"}}

	merged := mergeProfiles(inline, sub)
	if len(merged) != 2 {
		t.Fatalf("got %d profiles, want 2", len(merged))
	}
}

func TestFetchSubscription_Integration(t *testing.T) {
	links := "vless://user@host1:443#Server-A\nvless://user@host2:443#Server-B\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(links))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, encoded)
	}))
	defer srv.Close()

	profiles, err := fetchSubscription(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(profiles))
	}
	if profiles[0].Name != "Server-A" {
		t.Errorf("got %q, want Server-A", profiles[0].Name)
	}
}

func TestFetchSubscription_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := fetchSubscription(srv.URL)
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}

func TestLoadSubscriptionProfiles_SelectByName(t *testing.T) {
	links := "vless://user@host1:443#Alpha\nvless://user@host2:443#Beta\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(links))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, encoded)
	}))
	defer srv.Close()

	profile, all, err := loadSubscriptionProfiles(srv.URL, "Beta")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if profile.Name != "Beta" {
		t.Errorf("selected = %q, want Beta", profile.Name)
	}
	if len(all) != 2 {
		t.Errorf("got %d profiles, want 2", len(all))
	}
}

func TestLoadSubscriptionProfiles_DefaultFirst(t *testing.T) {
	links := "vless://user@host1:443#First\nvless://user@host2:443#Second\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(links))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, encoded)
	}))
	defer srv.Close()

	profile, _, err := loadSubscriptionProfiles(srv.URL, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if profile.Name != "First" {
		t.Errorf("selected = %q, want First", profile.Name)
	}
}

func TestLoadSubscriptionProfiles_NotFound(t *testing.T) {
	links := "vless://user@host:443#Only\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(links))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, encoded)
	}))
	defer srv.Close()

	_, _, err := loadSubscriptionProfiles(srv.URL, "missing")
	if err == nil {
		t.Fatal("expected error for missing profile")
	}
}
