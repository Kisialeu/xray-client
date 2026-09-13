package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const subscriptionTimeout = 30 * time.Second

var subscriptionCacheDir string

func subscriptionURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil {
		return nil, fmt.Errorf("invalid subscription URL")
	}
	local := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && local != nil && local.IsLoopback()) {
		return nil, fmt.Errorf("subscription requires HTTPS (HTTP allowed only for loopback)")
	}
	return u, nil
}

var knownSchemes = []string{"vless://", "vmess://", "trojan://", "ss://", "ssr://"}

type subscriptionResult struct {
	profiles []Profile
	dns      []string
}

func fetchSubscription(logger *slog.Logger, rawURL string) (*subscriptionResult, error) {
	u, err := subscriptionURL(rawURL)
	if err != nil {
		return nil, err
	}
	result, err := fetchSubscriptionRemote(logger, u)
	if subscriptionCacheDir == "" {
		return result, err
	}
	if err == nil {
		err = validateProfiles(result.profiles)
	}
	key := fmt.Sprintf("%x.json", sha256.Sum256([]byte(rawURL)))
	path := filepath.Join(subscriptionCacheDir, key)
	type cached struct {
		Profiles []Profile
		DNS      []string
	}
	if err != nil {
		fi, statErr := os.Lstat(path)
		if statErr == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0077 == 0 && time.Since(fi.ModTime()) < 7*24*time.Hour {
			data, readErr := os.ReadFile(path)
			var old cached
			if readErr == nil && json.Unmarshal(data, &old) == nil && validateProfiles(old.Profiles) == nil {
				logger.Warn("subscription unavailable; using private cached profiles (maximum age 7 days)")
				return &subscriptionResult{profiles: old.Profiles, dns: old.DNS}, nil
			}
		}
		return nil, err
	}
	if mkdirErr := os.MkdirAll(subscriptionCacheDir, 0700); mkdirErr != nil {
		return result, nil
	}
	data, _ := json.Marshal(cached{result.profiles, result.dns})
	f, writeErr := os.CreateTemp(subscriptionCacheDir, ".subscription-*")
	if writeErr == nil {
		defer os.Remove(f.Name())
		_, writeErr = f.Write(data)
		closeErr := f.Close()
		if writeErr == nil && closeErr == nil {
			_ = os.Rename(f.Name(), path)
		}
	}
	return result, nil
}

func fetchSubscriptionRemote(logger *slog.Logger, u *url.URL) (*subscriptionResult, error) {
	logger.Info("fetching subscription", "host", u.Hostname())

	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("invalid subscription request")
	}
	req.Header.Set("User-Agent", "v2rayNG/1.0")

	start := time.Now()
	resp, err := (&http.Client{Timeout: subscriptionTimeout, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 || r.URL.Scheme != u.Scheme || r.URL.Host != u.Host {
			return fmt.Errorf("subscription redirect rejected")
		}
		return nil
	}}).Do(req)
	if err != nil {
		logger.Error("subscription fetch failed", "host", u.Hostname(), "elapsed", time.Since(start))
		return nil, fmt.Errorf("subscription request failed (network, TLS, timeout, or rejected redirect)")
	}
	defer resp.Body.Close()

	logger.Debug("subscription response", "status", resp.StatusCode, "elapsed", time.Since(start))

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("subscription returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return nil, fmt.Errorf("read subscription body: %w", err)
	}
	if len(body) > 1<<20 {
		return nil, fmt.Errorf("subscription exceeds 1 MiB")
	}
	logger.Debug("subscription body", "bytes", len(body))

	profiles, bodyDNS, err := parseSubscription(logger, string(body))
	if err != nil {
		return nil, err
	}

	dns := parseHeaderDNS(resp.Header.Get("X-DNS"))
	if len(dns) == 0 {
		dns = bodyDNS
	}
	if len(dns) > 0 {
		logger.Info("subscription provides DNS", "servers", dns)
	}

	logger.Info("subscription loaded", "profiles", len(profiles), "elapsed", time.Since(start))
	return &subscriptionResult{profiles: profiles, dns: dns}, nil
}

func parseHeaderDNS(header string) []string {
	if header == "" {
		return nil
	}
	var servers []string
	for _, s := range strings.Split(header, ",") {
		if s = strings.TrimSpace(s); s != "" {
			servers = append(servers, s)
		}
	}
	return servers
}

func parseSubscription(logger *slog.Logger, raw string) ([]Profile, []string, error) {
	decoded, encoding := tryBase64Decode(strings.TrimSpace(raw))
	logger.Debug("subscription decoded", "encoding", encoding, "decoded_bytes", len(decoded))

	var profiles []Profile
	var dns []string
	seen := make(map[string]bool)
	skipped := 0
	for _, line := range strings.Split(decoded, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#dns:") {
			dns = parseHeaderDNS(strings.TrimPrefix(line, "#dns:"))
			continue
		}
		if strings.HasPrefix(line, "#") {
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
		logger.Warn("subscription skipped unsupported or non-proxy lines", "count", skipped)
	}

	if len(profiles) == 0 {
		return nil, nil, fmt.Errorf("subscription contains no valid links")
	}
	return profiles, dns, nil
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
