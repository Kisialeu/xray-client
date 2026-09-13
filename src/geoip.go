package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	countryCache = make(map[string]string)
	cacheMu      sync.RWMutex
)

type apiResponseStruct struct {
	CountryCode string `json:"countryCode"`
}

// resolveCountries processes a slice of hostport strings and returns a map from each
// hostport to its resolved country code. Results are cached under the hostname.
func resolveCountries(hostports []string) map[string]string {
	httpClient := &http.Client{Timeout: 5 * time.Second}
	result := make(map[string]string, len(hostports))

	// Track hostnames that still need resolution.
	unresolved := make(map[string][]string)

	// ---------- First pass: fill result from cache ----------
	cacheMu.RLock()
	for _, hp := range hostports {
		if cached, ok := countryCache[hp]; ok {
			result[hp] = cached
			cacheMu.RUnlock()
			continue
		}
		// Extract hostname from hostport.
		host, _, err := net.SplitHostPort(hp)
		if err != nil || host == "" {
			result[hp] = ""
			cacheMu.RUnlock()
			continue
		}
		if cached, ok := countryCache[host]; ok {
			result[hp] = cached
			cacheMu.RUnlock()
			continue
		}
		// Remember hostname for later resolution.
		unresolved[host] = append(unresolved[host], hp)
	}
	cacheMu.RUnlock()

	// ---------- Resolve uncached hostnames ----------
	for hostname, hps := range unresolved {
		// Resolve hostname to IP addresses.
		ips, err := net.LookupHost(hostname)
		if err != nil || len(ips) == 0 {
			for _, hp := range hps {
				result[hp] = ""
			}
			continue
		}
		if len(ips) > 100 {
			ips = ips[:100] // enforce 100‑IP batch limit
		}
		// Prepare batch request.
		jsonData, _ := json.Marshal(ips)
		resp, err := httpClient.Post(
			"http://ip-api.com/batch?fields=countryCode",
			"application/json",
			bytes.NewReader(jsonData),
		)
		if err == nil && resp.StatusCode == 200 {
			defer resp.Body.Close()
			var batchResp []apiResponseStruct
			if err := json.NewDecoder(resp.Body).Decode(&batchResp); err == nil && len(batchResp) > 0 {
				country := batchResp[0].CountryCode
				for _, hp := range hps {
					result[hp] = country
				}
				// Cache the result under the hostname.
				cacheMu.Lock()
				countryCache[hostname] = country
				cacheMu.Unlock()
			}
		}
	}

	return result
}

// profileFlag returns a flag string for a profile. It uses the country code
// produced by profileCountryCode and converts it to an emoji flag.
func profileFlag(p Profile) string {
	if cc := profileCountryCode(p); cc != "" {
		return countryFlag(cc)
	}
	return ""
}

// profileCountryCode maps known profile names to ISO‑3166‑1 alpha‑2 country codes.
// The function expects profile names in any case/whitespace; it recognises
// German‑language names used in the project (Бавария → DE, Швейцария → CH,
// Астана → KZ) as well as the English aliases DE, CH, KZ.
func profileCountryCode(p Profile) string {
	name := strings.ToUpper(strings.TrimSpace(p.Name))
	switch name {
	case "БАВАРИЯ", "DE":
		return "DE"
	case "ШВЕЙЦАРИЯ", "CH":
		return "CH"
	case "АСТАНА", "KZ":
		return "KZ"
	default:
		return ""
	}
}

// countryFlag returns the emoji flag for a 2‑letter uppercase country code.
// It validates the input before converting each character to a Unicode regional
// indicator symbol.
func countryFlag(code string) string {
	if len(code) != 2 {
		return ""
	}
	for _, r := range code {
		if r < 'A' || r > 'Z' {
			return ""
		}
	}
	const flagBase rune = 0x1F1E6
	first := flagBase + (rune(code[0]) - 'A')
	second := flagBase + (rune(code[1]) - 'A')
	return string(first) + string(second)
}