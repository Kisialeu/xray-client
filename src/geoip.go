package main

import (
	"net"
	"net/url"
	"strings"
	"sync"
)

var (
	countryCache   = make(map[string]string)
	countryCacheMu sync.RWMutex
)

// Never disclose subscription endpoints to a geolocation provider.
func resolveCountries(hostports []string) map[string]string {
	countryCacheMu.RLock()
	defer countryCacheMu.RUnlock()
	result := make(map[string]string)
	for _, hp := range hostports {
		host, _, err := net.SplitHostPort(hp)
		if err != nil {
			host = hp
		}
		if cc := countryCache[host]; cc != "" {
			result[hp] = cc
		}
	}
	return result
}

func countryFlag(code string) string {
	if len(code) != 2 {
		return ""
	}
	code = strings.ToUpper(code)
	if code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z' {
		return ""
	}
	return string(rune(code[0])-'A'+0x1F1E6) + string(rune(code[1])-'A'+0x1F1E6)
}

// profileCountryCode derives the display location from metadata already in a
// subscription. It never resolves or submits the endpoint to a third party.
func profileCountryCode(p Profile) string {
	text := strings.ToLower(p.Name + " " + p.Link)
	for token, code := range map[string]string{
		"germany": "DE", "deutschland": "DE", "berlin": "DE", "frankfurt": "DE", ".de": "DE",
		"switzerland": "CH", "swiss": "CH", "zurich": "CH", "geneva": "CH", ".ch": "CH",
		"finland": "FI", "helsinki": "FI", ".fi": "FI",
		"kazakhstan": "KZ", "astana": "KZ", ".kz": "KZ",
		"netherlands": "NL", "amsterdam": "NL", ".nl": "NL",
		"france": "FR", "paris": "FR", ".fr": "FR",
		"uk": "GB", "united kingdom": "GB", "london": "GB", ".uk": "GB",
		"usa": "US", "united states": "US", "new york": "US", ".us": "US",
		"canada": "CA", "toronto": "CA", ".ca": "CA",
	} {
		if strings.Contains(text, token) {
			return code
		}
	}
	if u, err := url.Parse(p.Link); err == nil {
		host := strings.ToLower(u.Hostname())
		for suffix, code := range map[string]string{".de": "DE", ".ch": "CH", ".fi": "FI", ".kz": "KZ", ".nl": "NL", ".fr": "FR", ".uk": "GB", ".us": "US", ".ca": "CA"} {
			if strings.HasSuffix(host, suffix) {
				return code
			}
		}
	}
	return ""
}

func profileFlag(p Profile) string { return countryFlag(profileCountryCode(p)) }
