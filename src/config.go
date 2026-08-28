package main

import (
	"bufio"
	"fmt"
	"log/slog"
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
	Default      string    `yaml:"default"`
	Subscription string    `yaml:"subscription"`
	DNS          []string  `yaml:"dns"`
	Profiles     []Profile `yaml:"profiles"`
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
	if flagLink != "" {
		return Profile{Link: flagLink}, nil
	}

	if path == "" {
		return Profile{}, fmt.Errorf("no link provided: use --link, --config, --subscribe, or set XRAY_LINK")
	}

	switch {
	case strings.HasSuffix(strings.ToLower(path), ".txt"):
		return loadTXT(path)
	case isYAML(path):
		return loadYAML(path, profileName)
	default:
		if p, err := loadTXT(path); err == nil {
			return p, nil
		}
		return loadYAML(path, profileName)
	}
}

func loadSubscriptionProfiles(subURL, profileName string, logger *slog.Logger) (Profile, []Profile, error) {
	profiles, err := fetchSubscription(logger, subURL)
	if err != nil {
		return Profile{}, nil, err
	}

	selected, err := selectProfile(profiles, profileName, "")
	if err != nil {
		return Profile{}, nil, fmt.Errorf("%w in subscription", err)
	}
	logger.Info("subscription profile selected", "name", selected.Name, "total", len(profiles))
	return selected, profiles, nil
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
// Inline profiles only — does not fetch subscriptions.
func loadYAML(path, profileName string) (Profile, error) {
	cfg, err := parseYAMLConfig(path)
	if err != nil {
		return Profile{}, err
	}
	if len(cfg.Profiles) == 0 {
		return Profile{}, fmt.Errorf("%s: no profiles defined", path)
	}
	return selectProfile(cfg.Profiles, profileName, cfg.Default)
}

// loadYAMLAll parses a YAML config file, fetches the subscription URL if
// present, merges all profiles, and returns the selected profile along with
// the full profile list. Subscription is fetched at most once.
func loadYAMLAll(path, profileName string, logger *slog.Logger) (Profile, []Profile, error) {
	cfg, err := parseYAMLConfig(path)
	if err != nil {
		return Profile{}, nil, err
	}

	profiles := cfg.Profiles
	logger.Debug("config loaded", "path", path, "inline_profiles", len(profiles), "has_subscription", cfg.Subscription != "")

	if cfg.Subscription != "" {
		sub, subErr := fetchSubscription(logger, cfg.Subscription)
		if subErr != nil {
			if len(profiles) == 0 {
				return Profile{}, nil, fmt.Errorf("subscription fetch failed and no inline profiles: %w", subErr)
			}
			logger.Warn("subscription fetch failed, using inline profiles only", "err", subErr)
		} else {
			before := len(profiles)
			profiles = mergeProfiles(profiles, sub)
			logger.Info("profiles merged", "inline", before, "subscription", len(sub), "total", len(profiles))
		}
	}

	if len(profiles) == 0 {
		return Profile{}, nil, fmt.Errorf("%s: no profiles defined", path)
	}

	selected, err := selectProfile(profiles, profileName, cfg.Default)
	if err != nil {
		return Profile{}, nil, fmt.Errorf("%w in %s", err, path)
	}
	logger.Info("profile selected", "name", selected.Name, "total", len(profiles))
	return selected, profiles, nil
}

func selectProfile(profiles []Profile, name, defaultName string) (Profile, error) {
	if len(profiles) == 0 {
		return Profile{}, fmt.Errorf("no profiles defined")
	}
	want := name
	if want == "" {
		want = defaultName
	}
	if want == "" {
		return profiles[0], nil
	}
	for _, p := range profiles {
		if p.Name == want {
			return p, nil
		}
	}
	return Profile{}, fmt.Errorf("profile %q not found", want)
}

// loadAllProfiles returns all profiles from a YAML config file, or nil on error/non-YAML.
// Inline profiles only — does not fetch subscriptions.
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

// mergeProfiles combines inline and subscription profiles. Inline profiles
// come first; subscription profiles are appended, skipping any whose name
// or link already exists in the inline set.
func mergeProfiles(inline, sub []Profile) []Profile {
	seenName := make(map[string]bool, len(inline))
	seenLink := make(map[string]bool, len(inline))
	for _, p := range inline {
		if p.Name != "" {
			seenName[p.Name] = true
		}
		seenLink[p.Link] = true
	}
	merged := make([]Profile, len(inline))
	copy(merged, inline)
	for _, p := range sub {
		if seenLink[p.Link] {
			continue
		}
		if p.Name != "" && seenName[p.Name] {
			continue
		}
		merged = append(merged, p)
		if p.Name != "" {
			seenName[p.Name] = true
		}
		seenLink[p.Link] = true
	}
	return merged
}

// listProfiles prints available profiles from a YAML config file,
// including subscription profiles if a subscription URL is configured.
func listProfiles(path string, logger *slog.Logger) error {
	cfg, err := parseYAMLConfig(path)
	if err != nil {
		return err
	}

	profiles := cfg.Profiles
	if cfg.Subscription != "" {
		sub, subErr := fetchSubscription(logger, cfg.Subscription)
		if subErr != nil {
			logger.Warn("subscription fetch failed", "err", subErr)
		} else {
			profiles = mergeProfiles(profiles, sub)
		}
	}

	if len(profiles) == 0 {
		fmt.Println("no profiles found")
		return nil
	}
	for _, p := range profiles {
		marker := "  "
		if p.Name == cfg.Default {
			marker = "* "
		}
		name := p.Name
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Printf("%s%s\n", marker, name)
	}
	return nil
}
