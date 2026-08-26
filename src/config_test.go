package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

var testLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// ── loadTXT ───────────────────────────────────────────────────────────────────

func TestLoadTXT_BasicLink(t *testing.T) {
	f := writeTmp(t, "link.txt", "vless://user@host:443\n")
	p, err := loadTXT(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Link != "vless://user@host:443" {
		t.Fatalf("got %q, want vless://user@host:443", p.Link)
	}
}

func TestLoadTXT_SkipsCommentAndBlank(t *testing.T) {
	f := writeTmp(t, "link.txt", "\n# comment\nvless://real\n")
	p, err := loadTXT(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Link != "vless://real" {
		t.Fatalf("got %q", p.Link)
	}
}

func TestLoadTXT_TrimWhitespace(t *testing.T) {
	f := writeTmp(t, "link.txt", "  vless://trimmed  \n")
	p, err := loadTXT(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Link != "vless://trimmed" {
		t.Fatalf("got %q, expected trimmed", p.Link)
	}
}

func TestLoadTXT_Empty(t *testing.T) {
	f := writeTmp(t, "empty.txt", "# nothing\n\n")
	_, err := loadTXT(f)
	if err == nil {
		t.Fatal("expected error for empty file")
	}
}

func TestLoadTXT_NotFound(t *testing.T) {
	_, err := loadTXT("/does/not/exist.txt")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadTXT_FirstLineWins(t *testing.T) {
	f := writeTmp(t, "multi.txt", "vless://first\nvless://second\n")
	p, err := loadTXT(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Link != "vless://first" {
		t.Fatalf("got %q, want vless://first (first line wins)", p.Link)
	}
}

// ── loadYAML ──────────────────────────────────────────────────────────────────

func TestLoadYAML_DefaultProfile(t *testing.T) {
	yaml := `
default: work
profiles:
  - name: home
    link: "vless://home"
  - name: work
    link: "vless://work"
`
	f := writeTmp(t, "cfg.yaml", yaml)
	p, err := loadYAML(f, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name != "work" {
		t.Fatalf("got profile %q, want work", p.Name)
	}
	if p.Link != "vless://work" {
		t.Fatalf("got link %q", p.Link)
	}
}

func TestLoadYAML_ExplicitProfile(t *testing.T) {
	yaml := `
profiles:
  - name: home
    link: "vless://home"
  - name: work
    link: "vless://work"
`
	f := writeTmp(t, "cfg.yaml", yaml)
	p, err := loadYAML(f, "home")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name != "home" {
		t.Fatalf("got %q, want home", p.Name)
	}
}

func TestLoadYAML_FirstWhenNoDefault(t *testing.T) {
	yaml := `
profiles:
  - name: alpha
    link: "vless://alpha"
  - name: beta
    link: "vless://beta"
`
	f := writeTmp(t, "cfg.yaml", yaml)
	p, err := loadYAML(f, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name != "alpha" {
		t.Fatalf("got %q, want alpha", p.Name)
	}
}

func TestLoadYAML_ProfileNotFound(t *testing.T) {
	yaml := `
profiles:
  - name: only
    link: "vless://only"
`
	f := writeTmp(t, "cfg.yaml", yaml)
	_, err := loadYAML(f, "missing")
	if err == nil {
		t.Fatal("expected error for missing profile")
	}
}

func TestLoadYAML_NoProfiles(t *testing.T) {
	f := writeTmp(t, "cfg.yaml", "default: x\nprofiles: []\n")
	_, err := loadYAML(f, "")
	if err == nil {
		t.Fatal("expected error for empty profiles list")
	}
}

func TestLoadYAML_TLSInsecure(t *testing.T) {
	yaml := `
profiles:
  - name: insecure
    link: "vless://x"
    tls_insecure: true
`
	f := writeTmp(t, "cfg.yaml", yaml)
	p, err := loadYAML(f, "insecure")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !p.TLSInsecure {
		t.Fatal("expected TLSInsecure=true")
	}
}

func TestLoadYAML_InvalidYAML(t *testing.T) {
	f := writeTmp(t, "bad.yaml", "{{invalid yaml content")
	_, err := loadYAML(f, "")
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoadYAML_MissingFile(t *testing.T) {
	_, err := loadYAML("/does/not/exist.yaml", "")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// ── selectProfile ────────────────────────────────────────────────────────────

func TestSelectProfile_ByName(t *testing.T) {
	profiles := []Profile{
		{Name: "a", Link: "vless://a"},
		{Name: "b", Link: "vless://b"},
		{Name: "c", Link: "vless://c"},
	}
	p, err := selectProfile(profiles, "b", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name != "b" {
		t.Errorf("got %q, want b", p.Name)
	}
}

func TestSelectProfile_ByDefault(t *testing.T) {
	profiles := []Profile{
		{Name: "a", Link: "vless://a"},
		{Name: "b", Link: "vless://b"},
	}
	p, err := selectProfile(profiles, "", "b")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name != "b" {
		t.Errorf("got %q, want b", p.Name)
	}
}

func TestSelectProfile_NameOverridesDefault(t *testing.T) {
	profiles := []Profile{
		{Name: "a", Link: "vless://a"},
		{Name: "b", Link: "vless://b"},
	}
	p, err := selectProfile(profiles, "a", "b")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name != "a" {
		t.Errorf("got %q, want a (explicit name should override default)", p.Name)
	}
}

func TestSelectProfile_FirstWhenNoNameOrDefault(t *testing.T) {
	profiles := []Profile{
		{Name: "first", Link: "vless://first"},
		{Name: "second", Link: "vless://second"},
	}
	p, err := selectProfile(profiles, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name != "first" {
		t.Errorf("got %q, want first", p.Name)
	}
}

func TestSelectProfile_NotFound(t *testing.T) {
	profiles := []Profile{{Name: "only", Link: "vless://only"}}
	_, err := selectProfile(profiles, "missing", "")
	if err == nil {
		t.Fatal("expected error for missing profile")
	}
}

func TestSelectProfile_EmptyList(t *testing.T) {
	_, err := selectProfile(nil, "any", "")
	if err == nil {
		t.Fatal("expected error for empty profile list")
	}
}

// ── loadLink ──────────────────────────────────────────────────────────────────

func TestLoadLink_FlagWins(t *testing.T) {
	p, err := loadLink("", "", "vless://from-flag")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Link != "vless://from-flag" {
		t.Fatalf("got %q", p.Link)
	}
}

func TestLoadLink_FlagWinsOverConfig(t *testing.T) {
	yaml := "profiles:\n  - name: cfg\n    link: \"vless://cfg\"\n"
	f := writeTmp(t, "cfg.yaml", yaml)
	p, err := loadLink(f, "", "vless://from-flag")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Link != "vless://from-flag" {
		t.Fatalf("got %q, want vless://from-flag (flag wins)", p.Link)
	}
}

func TestLoadLink_TxtByExtension(t *testing.T) {
	f := writeTmp(t, "link.txt", "vless://txt\n")
	p, err := loadLink(f, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Link != "vless://txt" {
		t.Fatalf("got %q", p.Link)
	}
}

func TestLoadLink_YamlByExtension(t *testing.T) {
	yaml := "profiles:\n  - name: only\n    link: \"vless://yaml\"\n"
	f := writeTmp(t, "cfg.yaml", yaml)
	p, err := loadLink(f, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Link != "vless://yaml" {
		t.Fatalf("got %q", p.Link)
	}
}

func TestLoadLink_YmlExtension(t *testing.T) {
	yaml := "profiles:\n  - name: only\n    link: \"vless://yml\"\n"
	f := writeTmp(t, "cfg.yml", yaml)
	p, err := loadLink(f, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Link != "vless://yml" {
		t.Fatalf("got %q", p.Link)
	}
}

func TestLoadLink_NoInputError(t *testing.T) {
	_, err := loadLink("", "", "")
	if err == nil {
		t.Fatal("expected error with no link and no config")
	}
}

func TestLoadLink_ProfileSelection(t *testing.T) {
	yaml := "profiles:\n  - name: a\n    link: \"vless://a\"\n  - name: b\n    link: \"vless://b\"\n"
	f := writeTmp(t, "cfg.yaml", yaml)
	p, err := loadLink(f, "b", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name != "b" {
		t.Fatalf("got %q, want b", p.Name)
	}
}

func TestLoadLink_UnknownExtensionFallsBackToTXT(t *testing.T) {
	f := writeTmp(t, "link.conf", "vless://fallback\n")
	p, err := loadLink(f, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Link != "vless://fallback" {
		t.Fatalf("got %q", p.Link)
	}
}

// ── loadAllProfiles ───────────────────────────────────────────────────────────

func TestLoadAllProfiles_ReturnsAll(t *testing.T) {
	yaml := "profiles:\n  - name: a\n    link: \"vless://a\"\n  - name: b\n    link: \"vless://b\"\n"
	f := writeTmp(t, "cfg.yaml", yaml)
	profiles, err := loadAllProfiles(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(profiles))
	}
}

func TestLoadAllProfiles_NonYamlReturnsNil(t *testing.T) {
	f := writeTmp(t, "link.txt", "vless://x\n")
	profiles, err := loadAllProfiles(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if profiles != nil {
		t.Fatalf("expected nil for non-yaml path")
	}
}

func TestLoadAllProfiles_EmptyPath(t *testing.T) {
	profiles, err := loadAllProfiles("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if profiles != nil {
		t.Fatal("expected nil for empty path")
	}
}

func TestLoadAllProfiles_MissingFile(t *testing.T) {
	_, err := loadAllProfiles("/no/such/file.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// ── loadYAMLAll ──────────────────────────────────────────────────────────────

func TestLoadYAMLAll_InlineOnly(t *testing.T) {
	yaml := `
default: b
profiles:
  - name: a
    link: "vless://a"
  - name: b
    link: "vless://b"
`
	f := writeTmp(t, "cfg.yaml", yaml)
	selected, all, err := loadYAMLAll(f, "", testLogger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected.Name != "b" {
		t.Errorf("selected = %q, want b (default)", selected.Name)
	}
	if len(all) != 2 {
		t.Errorf("got %d profiles, want 2", len(all))
	}
}

func TestLoadYAMLAll_WithSubscription(t *testing.T) {
	links := "vless://user@sub-host:443#SubProfile\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(links))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, encoded)
	}))
	defer srv.Close()

	yaml := fmt.Sprintf(`
subscription: "%s"
profiles:
  - name: inline
    link: "vless://inline"
`, srv.URL)
	f := writeTmp(t, "cfg.yaml", yaml)

	selected, all, err := loadYAMLAll(f, "", testLogger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected.Name != "inline" {
		t.Errorf("selected = %q, want inline (first)", selected.Name)
	}
	if len(all) != 2 {
		t.Errorf("got %d profiles, want 2 (inline + subscription)", len(all))
	}
}

func TestLoadYAMLAll_SubscriptionFailsFallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	yaml := fmt.Sprintf(`
subscription: "%s"
profiles:
  - name: fallback
    link: "vless://fallback"
`, srv.URL)
	f := writeTmp(t, "cfg.yaml", yaml)

	selected, all, err := loadYAMLAll(f, "", testLogger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected.Name != "fallback" {
		t.Errorf("should fall back to inline, got %q", selected.Name)
	}
	if len(all) != 1 {
		t.Errorf("got %d profiles, want 1 (inline only)", len(all))
	}
}

func TestLoadYAMLAll_SubscriptionFailsNoInline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	yaml := fmt.Sprintf(`
subscription: "%s"
profiles: []
`, srv.URL)
	f := writeTmp(t, "cfg.yaml", yaml)

	_, _, err := loadYAMLAll(f, "", testLogger)
	if err == nil {
		t.Fatal("expected error when subscription fails and no inline profiles")
	}
}

func TestLoadYAMLAll_ExplicitProfile(t *testing.T) {
	yaml := `
profiles:
  - name: a
    link: "vless://a"
  - name: b
    link: "vless://b"
`
	f := writeTmp(t, "cfg.yaml", yaml)
	selected, _, err := loadYAMLAll(f, "b", testLogger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected.Name != "b" {
		t.Errorf("selected = %q, want b", selected.Name)
	}
}

func TestLoadYAMLAll_ProfileNotFound(t *testing.T) {
	yaml := `
profiles:
  - name: only
    link: "vless://only"
`
	f := writeTmp(t, "cfg.yaml", yaml)
	_, _, err := loadYAMLAll(f, "missing", testLogger)
	if err == nil {
		t.Fatal("expected error for missing profile")
	}
}

func TestLoadYAMLAll_MergeDedupsSubscription(t *testing.T) {
	links := "vless://user@host:443#inline-dup\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(links))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, encoded)
	}))
	defer srv.Close()

	yaml := fmt.Sprintf(`
subscription: "%s"
profiles:
  - name: inline-dup
    link: "vless://different-link"
`, srv.URL)
	f := writeTmp(t, "cfg.yaml", yaml)

	_, all, err := loadYAMLAll(f, "", testLogger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("got %d profiles, want 1 (same name should dedup)", len(all))
	}
}

// ── listProfiles ──────────────────────────────────────────────────────────────

func TestListProfiles_PrintsNames(t *testing.T) {
	yaml := "default: b\nprofiles:\n  - name: a\n    link: \"vless://a\"\n  - name: b\n    link: \"vless://b\"\n"
	f := writeTmp(t, "cfg.yaml", yaml)
	if err := listProfiles(f, testLogger); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestListProfiles_Empty(t *testing.T) {
	f := writeTmp(t, "cfg.yaml", "profiles: []\n")
	if err := listProfiles(f, testLogger); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestListProfiles_NotFound(t *testing.T) {
	if err := listProfiles("/no/such/file.yaml", testLogger); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestListProfiles_WithSubscription(t *testing.T) {
	links := "vless://user@host:443#SubServer\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(links))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, encoded)
	}))
	defer srv.Close()

	yaml := fmt.Sprintf("subscription: \"%s\"\nprofiles:\n  - name: inline\n    link: \"vless://inline\"\n", srv.URL)
	f := writeTmp(t, "cfg.yaml", yaml)

	if err := listProfiles(f, testLogger); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ── isYAML ───────────────────────────────────────────────────────────────────

func TestIsYAML(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"config.yaml", true},
		{"config.yml", true},
		{"config.YAML", true},
		{"config.YML", true},
		{"config.txt", false},
		{"config.json", false},
		{"", false},
		{"yaml", false},
	}
	for _, tt := range tests {
		if got := isYAML(tt.path); got != tt.want {
			t.Errorf("isYAML(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

// ── parseYAMLConfig ──────────────────────────────────────────────────────────

func TestParseYAMLConfig_Full(t *testing.T) {
	yaml := `
default: work
subscription: "https://example.com/sub"
profiles:
  - name: home
    link: "vless://home"
  - name: work
    link: "vless://work"
    tls_insecure: true
`
	f := writeTmp(t, "cfg.yaml", yaml)
	cfg, err := parseYAMLConfig(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Default != "work" {
		t.Errorf("Default = %q, want work", cfg.Default)
	}
	if cfg.Subscription != "https://example.com/sub" {
		t.Errorf("Subscription = %q", cfg.Subscription)
	}
	if len(cfg.Profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(cfg.Profiles))
	}
	if !cfg.Profiles[1].TLSInsecure {
		t.Error("work profile should have tls_insecure=true")
	}
}

func TestParseYAMLConfig_Invalid(t *testing.T) {
	f := writeTmp(t, "bad.yaml", "{{not yaml")
	_, err := parseYAMLConfig(f)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestParseYAMLConfig_MissingFile(t *testing.T) {
	_, err := parseYAMLConfig("/does/not/exist.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// ── helper ────────────────────────────────────────────────────────────────────

func writeTmp(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("writeTmp: %v", err)
	}
	return path
}
