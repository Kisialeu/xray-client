package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Profile is a named connection entry from a YAML config file.
type Profile struct {
	Name        string `yaml:"name"`
	Link        string `yaml:"link"`
	TLSInsecure bool   `yaml:"tls_insecure"`
}

// configFile is the YAML schema.
type configFile struct {
	Default  string    `yaml:"default"`
	Profiles []Profile `yaml:"profiles"`
}

func isYAML(path string) bool {
	ext := strings.ToLower(path)
	return strings.HasSuffix(ext, ".yaml") || strings.HasSuffix(ext, ".yml")
}

func parseYAMLConfig(path string) (configFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return configFile{}, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg configFile
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return configFile{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// loadLink resolves the final connection link from flags and/or config file.
//
//   - If path ends in .txt, treats the file as a single-line link.
//   - If path ends in .yaml/.yml, parses multi-profile YAML; profileName selects
//     a profile by name (falls back to "default" field, then first entry).
//   - flagLink / XRAY_LINK env always wins over file content.
func loadLink(path, profileName, flagLink string) (Profile, error) {
	// CLI / env takes priority.
	if flagLink != "" {
		return Profile{Link: flagLink}, nil
	}

	if path == "" {
		return Profile{}, fmt.Errorf("no link provided: use --link, --config, or set XRAY_LINK")
	}

	switch {
	case strings.HasSuffix(strings.ToLower(path), ".txt"):
		return loadTXT(path)
	case isYAML(path):
		return loadYAML(path, profileName)
	default:
		// Try txt first, then yaml.
		if p, err := loadTXT(path); err == nil {
			return p, nil
		}
		return loadYAML(path, profileName)
	}
}

// loadTXT reads the first non-empty, non-comment line as the connection link.
func loadTXT(path string) (Profile, error) {
	f, err := os.Open(path)
	if err != nil {
		return Profile{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return Profile{Link: line}, nil
	}
	return Profile{}, fmt.Errorf("%s: no link found", path)
}

// loadYAML parses a multi-profile YAML and picks the requested profile.
func loadYAML(path, profileName string) (Profile, error) {
	cfg, err := parseYAMLConfig(path)
	if err != nil {
		return Profile{}, err
	}
	if len(cfg.Profiles) == 0 {
		return Profile{}, fmt.Errorf("%s: no profiles defined", path)
	}

	want := profileName
	if want == "" {
		want = cfg.Default
	}
	if want == "" {
		return cfg.Profiles[0], nil
	}

	for _, p := range cfg.Profiles {
		if p.Name == want {
			return p, nil
		}
	}
	return Profile{}, fmt.Errorf("profile %q not found in %s", want, path)
}

// loadAllProfiles returns all profiles from a YAML config file, or nil on error/non-YAML.
func loadAllProfiles(path string) ([]Profile, error) {
	if path == "" || !isYAML(path) {
		return nil, nil
	}
	cfg, err := parseYAMLConfig(path)
	if err != nil {
		return nil, err
	}
	return cfg.Profiles, nil
}

// listProfiles prints available profiles from a YAML config file.
func listProfiles(path string) error {
	cfg, err := parseYAMLConfig(path)
	if err != nil {
		return err
	}
	if len(cfg.Profiles) == 0 {
		fmt.Println("no profiles found")
		return nil
	}
	for _, p := range cfg.Profiles {
		marker := "  "
		if p.Name == cfg.Default {
			marker = "* "
		}
		fmt.Printf("%s%s\n", marker, p.Name)
	}
	return nil
}
