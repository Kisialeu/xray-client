package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var discardLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// ── parseSubscription ────────────────────────────────────────────────────────

func TestParseSubscription_Base64Std(t *testing.T) {
	links := "vless://user@host1:443#Server-1\nvless://user@host2:443#Server-2\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(links))

	profiles, err := parseSubscription(discardLogger, encoded)
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

func TestParseSubscription_Base64Raw(t *testing.T) {
	links := "vless://user@host1:443#A\nvless://user@host2:443#B\n"
	encoded := base64.RawStdEncoding.EncodeToString([]byte(links))

	profiles, err := parseSubscription(discardLogger, encoded)
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

	profiles, err := parseSubscription(discardLogger, encoded)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(profiles))
	}
}

func TestParseSubscription_Base64RawURL(t *testing.T) {
	links := "vless://user@host1:443#A\nvless://user@host2:443#B\n"
	encoded := base64.RawURLEncoding.EncodeToString([]byte(links))

	profiles, err := parseSubscription(discardLogger, encoded)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(profiles))
	}
}

func TestParseSubscription_Base64MIMEWrapped(t *testing.T) {
	links := "vless://user@host1:443#A\nvless://user@host2:443#B\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(links))
	var wrapped string
	for i := 0; i < len(encoded); i += 76 {
		end := i + 76
		if end > len(encoded) {
			end = len(encoded)
		}
		wrapped += encoded[i:end] + "\n"
	}

	profiles, err := parseSubscription(discardLogger, wrapped)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(profiles))
	}
}

func TestParseSubscription_PlainText(t *testing.T) {
	raw := "vless://user@host1:443#Plain\n# comment\nvless://user@host2:443#Two\n"

	profiles, err := parseSubscription(discardLogger, raw)
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

func TestParseSubscription_PlainTextAllSchemes(t *testing.T) {
	raw := strings.Join([]string{
		"vless://user@h1:443#V1",
		"vmess://dummybase64data",
		"trojan://pass@h2:443#T1",
		"ss://method:pass@h3:443#S1",
		"ssr://dummydata",
	}, "\n")

	profiles, err := parseSubscription(discardLogger, raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(profiles) != 5 {
		t.Fatalf("got %d profiles, want 5", len(profiles))
	}
}

func TestParseSubscription_Dedup(t *testing.T) {
	raw := "vless://user@host:443#S\nvless://user@host:443#S\n"

	profiles, err := parseSubscription(discardLogger, raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("got %d profiles, want 1 (dedup)", len(profiles))
	}
}

func TestParseSubscription_DedupKeepsFirst(t *testing.T) {
	// Same link repeated twice — first occurrence wins
	raw := "vless://user@host:443#Name\nvless://user@host:443#Name\n"

	profiles, err := parseSubscription(discardLogger, raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("got %d profiles, want 1 (identical links dedup)", len(profiles))
	}
}

func TestParseSubscription_DifferentFragmentsDifferentLinks(t *testing.T) {
	// Different fragments make them distinct links
	raw := "vless://user@host:443#First\nvless://user@host:443#Second\n"

	profiles, err := parseSubscription(discardLogger, raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2 (different fragments = different links)", len(profiles))
	}
}

func TestParseSubscription_Empty(t *testing.T) {
	_, err := parseSubscription(discardLogger, "")
	if err == nil {
		t.Fatal("expected error for empty subscription")
	}
}

func TestParseSubscription_WhitespaceOnly(t *testing.T) {
	_, err := parseSubscription(discardLogger, "   \n\t\n   ")
	if err == nil {
		t.Fatal("expected error for whitespace-only subscription")
	}
}

func TestParseSubscription_CommentsOnly(t *testing.T) {
	raw := "\n\n# comment\n# another\n"
	_, err := parseSubscription(discardLogger, raw)
	if err == nil {
		t.Fatal("expected error for comment-only subscription")
	}
}

func TestParseSubscription_RejectsGarbageHTML(t *testing.T) {
	raw := "<html><body>404 Not Found</body></html>"
	_, err := parseSubscription(discardLogger, raw)
	if err == nil {
		t.Fatal("expected error for HTML response")
	}
}

func TestParseSubscription_RejectsRandomText(t *testing.T) {
	_, err := parseSubscription(discardLogger, "this is not a subscription response at all")
	if err == nil {
		t.Fatal("expected error for random text")
	}
}

func TestParseSubscription_SkipsNonProxyLines(t *testing.T) {
	raw := "http://example.com\nvless://user@host:443#Good\nhttps://junk\n"
	profiles, err := parseSubscription(discardLogger, raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("got %d profiles, want 1", len(profiles))
	}
	if profiles[0].Name != "Good" {
		t.Errorf("got %q, want Good", profiles[0].Name)
	}
}

func TestParseSubscription_LeadingTrailingWhitespace(t *testing.T) {
	raw := "  \n  vless://user@host:443#Trimmed  \n  "
	profiles, err := parseSubscription(discardLogger, raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("got %d profiles, want 1", len(profiles))
	}
}

func TestParseSubscription_CaseInsensitiveScheme(t *testing.T) {
	raw := "VLESS://user@host:443#Upper\nVmess://data#Mixed\n"
	profiles, err := parseSubscription(discardLogger, raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("got %d, want 2", len(profiles))
	}
}

func TestParseSubscription_SingleLink(t *testing.T) {
	raw := "vless://user@host:443#Solo"
	profiles, err := parseSubscription(discardLogger, raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(profiles) != 1 || profiles[0].Name != "Solo" {
		t.Errorf("unexpected result: %+v", profiles)
	}
}

// ── tryBase64Decode ──────────────────────────────────────────────────────────

func TestTryBase64Decode_StdEncoding(t *testing.T) {
	original := "vless://user@host:443#Test"
	encoded := base64.StdEncoding.EncodeToString([]byte(original))
	decoded, enc := tryBase64Decode(encoded)
	if enc != "base64" {
		t.Errorf("encoding = %q, want base64", enc)
	}
	if decoded != original {
		t.Errorf("decoded = %q, want %q", decoded, original)
	}
}

func TestTryBase64Decode_RawEncoding(t *testing.T) {
	original := "vless://user@host:443#X"
	encoded := base64.RawStdEncoding.EncodeToString([]byte(original))
	decoded, enc := tryBase64Decode(encoded)
	if enc != "base64-raw" {
		t.Errorf("encoding = %q, want base64-raw", enc)
	}
	if decoded != original {
		t.Errorf("decoded = %q, want %q", decoded, original)
	}
}

func TestTryBase64Decode_URLEncoding(t *testing.T) {
	// Use bytes that produce + and / in standard base64 to force URL-safe difference
	original := "vless://user@host:443#Test\xff\xfe"
	encoded := base64.URLEncoding.EncodeToString([]byte(original))
	decoded, enc := tryBase64Decode(encoded)
	if enc == "plaintext" {
		t.Errorf("should decode URL-safe base64, got plaintext")
	}
	if decoded != original {
		t.Errorf("decoded = %q, want %q", decoded, original)
	}
}

func TestTryBase64Decode_RawURLEncoding(t *testing.T) {
	original := "vless://user@host:443#Test\xff\xfe"
	encoded := base64.RawURLEncoding.EncodeToString([]byte(original))
	decoded, enc := tryBase64Decode(encoded)
	if enc == "plaintext" {
		t.Errorf("should decode raw URL-safe base64, got plaintext")
	}
	if decoded != original {
		t.Errorf("decoded = %q, want %q", decoded, original)
	}
}

func TestTryBase64Decode_PlaintextFallback(t *testing.T) {
	raw := "vless://user@host:443#Test"
	decoded, enc := tryBase64Decode(raw)
	if enc != "plaintext" {
		t.Errorf("encoding = %q, want plaintext", enc)
	}
	if decoded != raw {
		t.Errorf("decoded = %q, want %q", decoded, raw)
	}
}

func TestTryBase64Decode_ValidBase64ButNoLinks(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("just some random text"))
	decoded, enc := tryBase64Decode(encoded)
	if enc != "plaintext" {
		t.Errorf("encoding = %q, want plaintext (decoded had no proxy links)", enc)
	}
	if decoded != encoded {
		t.Errorf("should return original when decoded content has no proxy links")
	}
}

func TestTryBase64Decode_StripsWhitespace(t *testing.T) {
	original := "vless://user@host:443#Test"
	encoded := base64.StdEncoding.EncodeToString([]byte(original))
	withSpaces := encoded[:10] + " " + encoded[10:20] + "\n" + encoded[20:]
	decoded, enc := tryBase64Decode(withSpaces)
	if enc != "base64" {
		t.Errorf("encoding = %q, want base64", enc)
	}
	if decoded != original {
		t.Errorf("decoded = %q, want %q", decoded, original)
	}
}

// ── isProxyLink ──────────────────────────────────────────────────────────────

func TestIsProxyLink_AllSchemes(t *testing.T) {
	valid := []string{
		"vless://user@host:443",
		"vmess://base64data",
		"trojan://user@host:443",
		"ss://base64@host:443",
		"ssr://base64data",
	}
	for _, link := range valid {
		if !isProxyLink(link) {
			t.Errorf("should recognize %q", link)
		}
	}
}

func TestIsProxyLink_CaseInsensitive(t *testing.T) {
	if !isProxyLink("VLESS://USER@HOST:443") {
		t.Error("should recognize uppercase VLESS://")
	}
	if !isProxyLink("Vmess://Data") {
		t.Error("should recognize mixed case Vmess://")
	}
}

func TestIsProxyLink_Rejects(t *testing.T) {
	invalid := []string{
		"<html>garbage</html>",
		"http://example.com",
		"https://example.com",
		"ftp://example.com",
		"",
		"not a link at all",
		"vless-not-a-link",
	}
	for _, link := range invalid {
		if isProxyLink(link) {
			t.Errorf("should reject %q", link)
		}
	}
}

// ── containsProxyLink ────────────────────────────────────────────────────────

func TestContainsProxyLink_Positive(t *testing.T) {
	if !containsProxyLink("some text vless://user@host then more") {
		t.Error("should find vless:// in middle of string")
	}
	if !containsProxyLink("vmess://data") {
		t.Error("should find vmess:// at start")
	}
}

func TestContainsProxyLink_Negative(t *testing.T) {
	if containsProxyLink("no proxy links here at all") {
		t.Error("should return false for text without proxy links")
	}
	if containsProxyLink("") {
		t.Error("should return false for empty string")
	}
}

// ── linkToProfile ────────────────────────────────────────────────────────────

func TestLinkToProfile_VLESSWithFragment(t *testing.T) {
	p := linkToProfile("vless://user@host:443?type=tcp#My-Server")
	if p.Name != "My-Server" {
		t.Errorf("Name = %q, want My-Server", p.Name)
	}
	if !strings.HasPrefix(p.Link, "vless://") {
		t.Errorf("Link = %q, expected vless:// prefix", p.Link)
	}
}

func TestLinkToProfile_VLESSNoFragment(t *testing.T) {
	p := linkToProfile("vless://user@example.com:443?type=tcp")
	if p.Name != "example.com" {
		t.Errorf("Name = %q, want example.com (host fallback)", p.Name)
	}
}

func TestLinkToProfile_TrojanWithFragment(t *testing.T) {
	p := linkToProfile("trojan://pass@host:443#Trojan-Server")
	if p.Name != "Trojan-Server" {
		t.Errorf("Name = %q, want Trojan-Server", p.Name)
	}
}

func TestLinkToProfile_SSWithFragment(t *testing.T) {
	p := linkToProfile("ss://method:pass@host:443#SS-Server")
	if p.Name != "SS-Server" {
		t.Errorf("Name = %q, want SS-Server", p.Name)
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

func TestLinkToProfile_VMessFallbackToAdd(t *testing.T) {
	obj := `{"v":"2","ps":"","add":"my-server.com","port":"443","id":"abc"}`
	encoded := base64.StdEncoding.EncodeToString([]byte(obj))
	link := "vmess://" + encoded

	p := linkToProfile(link)
	if p.Name != "my-server.com" {
		t.Errorf("Name = %q, want my-server.com (fallback to add)", p.Name)
	}
}

func TestLinkToProfile_VMessEmptyPSAndAdd(t *testing.T) {
	obj := `{"v":"2","ps":"","add":"","port":"443","id":"abc"}`
	encoded := base64.StdEncoding.EncodeToString([]byte(obj))
	link := "vmess://" + encoded

	p := linkToProfile(link)
	if p.Name == "" {
		t.Error("expected non-empty name for vmess with empty ps+add")
	}
}

func TestLinkToProfile_VMessInvalidBase64(t *testing.T) {
	p := linkToProfile("vmess://not-valid-base64!!!")
	if p.Link != "vmess://not-valid-base64!!!" {
		t.Errorf("Link should be preserved, got %q", p.Link)
	}
	if p.Name == "" {
		t.Error("expected non-empty fallback name")
	}
}

func TestLinkToProfile_VMessInvalidJSON(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("not json {{{"))
	p := linkToProfile("vmess://" + encoded)
	if p.Name == "" {
		t.Error("expected non-empty fallback name for invalid JSON")
	}
}

func TestLinkToProfile_VMessRawBase64(t *testing.T) {
	obj := `{"v":"2","ps":"Raw","add":"1.2.3.4","port":"443","id":"abc"}`
	encoded := base64.RawStdEncoding.EncodeToString([]byte(obj))
	link := "vmess://" + encoded

	p := linkToProfile(link)
	if p.Name != "Raw" {
		t.Errorf("Name = %q, want Raw", p.Name)
	}
}

// ── extractFragment ──────────────────────────────────────────────────────────

func TestExtractFragment_Simple(t *testing.T) {
	f := extractFragment("vless://user@host:443#Server-1")
	if f != "Server-1" {
		t.Errorf("got %q, want Server-1", f)
	}
}

func TestExtractFragment_URLEncoded(t *testing.T) {
	f := extractFragment("vless://user@host:443#Hello%20World")
	if f != "Hello World" {
		t.Errorf("got %q, want Hello World", f)
	}
}

func TestExtractFragment_Empty(t *testing.T) {
	f := extractFragment("vless://user@host:443")
	if f != "" {
		t.Errorf("got %q, want empty", f)
	}
}

func TestExtractFragment_EmptyHash(t *testing.T) {
	f := extractFragment("vless://user@host:443#")
	if f != "" {
		t.Errorf("got %q, want empty", f)
	}
}

func TestExtractFragment_Unicode(t *testing.T) {
	f := extractFragment("vless://user@host:443#%D0%A1%D0%B5%D1%80%D0%B2%D0%B5%D1%80")
	if f != "Сервер" {
		t.Errorf("got %q, want Сервер", f)
	}
}

// ── extractHost ──────────────────────────────────────────────────────────────

func TestExtractHost_VLESS(t *testing.T) {
	h := extractHost("vless://user@example.com:443")
	if h != "example.com" {
		t.Errorf("got %q, want example.com", h)
	}
}

func TestExtractHost_IP(t *testing.T) {
	h := extractHost("vless://user@1.2.3.4:443")
	if h != "1.2.3.4" {
		t.Errorf("got %q, want 1.2.3.4", h)
	}
}

func TestExtractHost_Fallback(t *testing.T) {
	h := extractHost("not a url at all")
	if h != "not a url at all" {
		t.Errorf("got %q, expected original string as fallback", h)
	}
}

// ── mergeProfiles ────────────────────────────────────────────────────────────

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
	if merged[0].Link != "vless://link-a" {
		t.Errorf("should keep inline link, got %q", merged[0].Link)
	}
}

func TestMergeProfiles_AppendsNew(t *testing.T) {
	inline := []Profile{{Name: "a", Link: "vless://a"}}
	sub := []Profile{{Name: "b", Link: "vless://b"}}

	merged := mergeProfiles(inline, sub)
	if len(merged) != 2 {
		t.Fatalf("got %d profiles, want 2", len(merged))
	}
	if merged[0].Name != "a" || merged[1].Name != "b" {
		t.Errorf("unexpected order: %v", merged)
	}
}

func TestMergeProfiles_InlineFirst(t *testing.T) {
	inline := []Profile{{Name: "z", Link: "vless://z"}}
	sub := []Profile{{Name: "a", Link: "vless://a"}}

	merged := mergeProfiles(inline, sub)
	if merged[0].Name != "z" {
		t.Errorf("inline profiles should come first, got %q", merged[0].Name)
	}
}

func TestMergeProfiles_EmptyInline(t *testing.T) {
	sub := []Profile{
		{Name: "a", Link: "vless://a"},
		{Name: "b", Link: "vless://b"},
	}
	merged := mergeProfiles(nil, sub)
	if len(merged) != 2 {
		t.Fatalf("got %d profiles, want 2", len(merged))
	}
}

func TestMergeProfiles_EmptySub(t *testing.T) {
	inline := []Profile{{Name: "a", Link: "vless://a"}}
	merged := mergeProfiles(inline, nil)
	if len(merged) != 1 {
		t.Fatalf("got %d profiles, want 1", len(merged))
	}
}

func TestMergeProfiles_BothEmpty(t *testing.T) {
	merged := mergeProfiles(nil, nil)
	if len(merged) != 0 {
		t.Fatalf("got %d profiles, want 0", len(merged))
	}
}

func TestMergeProfiles_UnnamedSubProfiles(t *testing.T) {
	inline := []Profile{{Name: "named", Link: "vless://a"}}
	sub := []Profile{{Name: "", Link: "vless://b"}, {Name: "", Link: "vless://c"}}

	merged := mergeProfiles(inline, sub)
	if len(merged) != 3 {
		t.Fatalf("got %d profiles, want 3 (unnamed shouldn't dedup by empty name)", len(merged))
	}
}

func TestMergeProfiles_PreservesTLSInsecure(t *testing.T) {
	inline := []Profile{{Name: "a", Link: "vless://a", TLSInsecure: true}}
	sub := []Profile{{Name: "b", Link: "vless://b"}}

	merged := mergeProfiles(inline, sub)
	if !merged[0].TLSInsecure {
		t.Error("TLSInsecure should be preserved from inline")
	}
}

// ── fetchSubscription ────────────────────────────────────────────────────────

func TestFetchSubscription_Success(t *testing.T) {
	links := "vless://user@host1:443#Server-A\nvless://user@host2:443#Server-B\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(links))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); ua != "v2rayNG/1.0" {
			t.Errorf("User-Agent = %q, want v2rayNG/1.0", ua)
		}
		fmt.Fprint(w, encoded)
	}))
	defer srv.Close()

	profiles, err := fetchSubscription(discardLogger, srv.URL)
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

func TestFetchSubscription_HTTP404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := fetchSubscription(discardLogger, srv.URL)
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention 404, got: %v", err)
	}
}

func TestFetchSubscription_HTTP500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := fetchSubscription(discardLogger, srv.URL)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestFetchSubscription_EmptyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, err := fetchSubscription(discardLogger, srv.URL)
	if err == nil {
		t.Fatal("expected error for empty body")
	}
}

func TestFetchSubscription_HTMLBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "<html><body>CDN Error</body></html>")
	}))
	defer srv.Close()

	_, err := fetchSubscription(discardLogger, srv.URL)
	if err == nil {
		t.Fatal("expected error for HTML response body")
	}
}

func TestFetchSubscription_InvalidURL(t *testing.T) {
	_, err := fetchSubscription(discardLogger, "://not-a-url")
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

func TestFetchSubscription_PlainTextBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "vless://user@host:443#Direct\ntrojan://pass@host2:443#T1\n")
	}))
	defer srv.Close()

	profiles, err := fetchSubscription(discardLogger, srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(profiles))
	}
}

// ── loadSubscriptionProfiles ─────────────────────────────────────────────────

func TestLoadSubscriptionProfiles_SelectByName(t *testing.T) {
	links := "vless://user@host1:443#Alpha\nvless://user@host2:443#Beta\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(links))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, encoded)
	}))
	defer srv.Close()

	profile, all, err := loadSubscriptionProfiles(srv.URL, "Beta", discardLogger)
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

	profile, _, err := loadSubscriptionProfiles(srv.URL, "", discardLogger)
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

	_, _, err := loadSubscriptionProfiles(srv.URL, "missing", discardLogger)
	if err == nil {
		t.Fatal("expected error for missing profile")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

func TestLoadSubscriptionProfiles_FetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	_, _, err := loadSubscriptionProfiles(srv.URL, "", discardLogger)
	if err == nil {
		t.Fatal("expected error when fetch fails")
	}
}

// ── tryBase64DecodeBytes ─────────────────────────────────────────────────────

func TestTryBase64DecodeBytes_Std(t *testing.T) {
	data := []byte(`{"key":"value"}`)
	encoded := base64.StdEncoding.EncodeToString(data)
	decoded := tryBase64DecodeBytes(encoded)
	if string(decoded) != string(data) {
		t.Errorf("got %q, want %q", decoded, data)
	}
}

func TestTryBase64DecodeBytes_Raw(t *testing.T) {
	data := []byte(`{"key":"value"}`)
	encoded := base64.RawStdEncoding.EncodeToString(data)
	decoded := tryBase64DecodeBytes(encoded)
	if string(decoded) != string(data) {
		t.Errorf("got %q, want %q", decoded, data)
	}
}

func TestTryBase64DecodeBytes_Invalid(t *testing.T) {
	decoded := tryBase64DecodeBytes("!!not valid base64!!")
	if decoded != nil {
		t.Errorf("expected nil for invalid base64, got %v", decoded)
	}
}
