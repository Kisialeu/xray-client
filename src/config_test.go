package main

import (
	"os"
	"path/filepath"
	"testing"
)

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

// ── listProfiles ──────────────────────────────────────────────────────────────

func TestListProfiles_PrintsNames(t *testing.T) {
	yaml := "default: b\nprofiles:\n  - name: a\n    link: \"vless://a\"\n  - name: b\n    link: \"vless://b\"\n"
	f := writeTmp(t, "cfg.yaml", yaml)
	// Just ensure no error — output goes to stdout.
	if err := listProfiles(f); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestListProfiles_Empty(t *testing.T) {
	f := writeTmp(t, "cfg.yaml", "profiles: []\n")
	if err := listProfiles(f); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestListProfiles_NotFound(t *testing.T) {
	if err := listProfiles("/no/such/file.yaml"); err == nil {
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
