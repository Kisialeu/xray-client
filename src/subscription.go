package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const subscriptionTimeout = 30 * time.Second

var knownSchemes = []string{"vless://", "vmess://", "trojan://", "ss://", "ssr://"}

func fetchSubscription(logger *slog.Logger, rawURL string) ([]Profile, error) {
	logger.Info("fetching subscription", "url", rawURL)

	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid subscription URL: %w", err)
	}
	req.Header.Set("User-Agent", "v2rayNG/1.0")

	start := time.Now()
	resp, err := (&http.Client{Timeout: subscriptionTimeout}).Do(req)
	if err != nil {
		logger.Error("subscription fetch failed", "url", rawURL, "elapsed", time.Since(start), "err", err)
		return nil, fmt.Errorf("fetch subscription: %w", err)
	}
	defer resp.Body.Close()

	logger.Debug("subscription response", "status", resp.StatusCode, "elapsed", time.Since(start))

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("subscription returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read subscription body: %w", err)
	}
	logger.Debug("subscription body", "bytes", len(body))

	profiles, err := parseSubscription(logger, string(body))
	if err != nil {
		return nil, err
	}

	logger.Info("subscription loaded", "profiles", len(profiles), "elapsed", time.Since(start))
	return profiles, nil
}

func parseSubscription(logger *slog.Logger, raw string) ([]Profile, error) {
	decoded, encoding := tryBase64Decode(strings.TrimSpace(raw))
	logger.Debug("subscription decoded", "encoding", encoding, "decoded_bytes", len(decoded))

	var profiles []Profile
	seen := make(map[string]bool)
	skipped := 0
	for _, line := range strings.Split(decoded, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !isProxyLink(line) {
			skipped++
			continue
		}
		p := linkToProfile(line)
		if p.Link == "" {
			continue
		}
		if seen[p.Link] {
			continue
		}
		seen[p.Link] = true
		profiles = append(profiles, p)
	}

	if skipped > 0 {
		logger.Debug("subscription skipped non-proxy lines", "count", skipped)
	}

	if len(profiles) == 0 {
		return nil, fmt.Errorf("subscription contains no valid links")
	}
	return profiles, nil
}

func tryBase64Decode(s string) (string, string) {
	clean := strings.NewReplacer("\n", "", "\r", "", " ", "").Replace(s)

	type variant struct {
		enc  *base64.Encoding
		name string
	}
	variants := []variant{
		{base64.StdEncoding, "base64"},
		{base64.RawStdEncoding, "base64-raw"},
		{base64.URLEncoding, "base64-url"},
		{base64.RawURLEncoding, "base64-url-raw"},
	}

	for _, v := range variants {
		if decoded, err := v.enc.DecodeString(clean); err == nil {
			if containsProxyLink(string(decoded)) {
				return string(decoded), v.name
			}
		}
	}
	return s, "plaintext"
}

func isProxyLink(line string) bool {
	lower := strings.ToLower(line)
	for _, scheme := range knownSchemes {
		if strings.HasPrefix(lower, scheme) {
			return true
		}
	}
	return false
}

func containsProxyLink(s string) bool {
	lower := strings.ToLower(s)
	for _, scheme := range knownSchemes {
		if strings.Contains(lower, scheme) {
			return true
		}
	}
	return false
}

func linkToProfile(link string) Profile {
	if strings.HasPrefix(link, "vmess://") {
		return parseVMessLink(link)
	}
	name := extractFragment(link)
	if name == "" {
		name = extractHost(link)
	}
	return Profile{Name: name, Link: link}
}

func parseVMessLink(link string) Profile {
	encoded := strings.TrimPrefix(link, "vmess://")
	encoded = strings.TrimSpace(encoded)

	data := tryBase64DecodeBytes(encoded)
	if data == nil {
		return Profile{Name: extractHost(link), Link: link}
	}

	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return Profile{Name: extractHost(link), Link: link}
	}

	name := ""
	if ps, ok := obj["ps"].(string); ok && ps != "" {
		name = ps
	} else if add, ok := obj["add"].(string); ok && add != "" {
		name = add
	}
	if name == "" {
		name = extractHost(link)
	}
	return Profile{Name: name, Link: link}
}

func tryBase64DecodeBytes(s string) []byte {
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if decoded, err := enc.DecodeString(s); err == nil {
			return decoded
		}
	}
	return nil
}

func extractFragment(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	return u.Fragment
}

func extractHost(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return link
	}
	if u.Hostname() != "" {
		return u.Hostname()
	}
	return link
}
