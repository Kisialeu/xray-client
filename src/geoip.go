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

const geoIPBatchURL = "http://ip-api.com/batch?fields=countryCode"

var (
	countryCache   = make(map[string]string)
	countryCacheMu sync.RWMutex
)

func resolveCountries(hostports []string) map[string]string {
	countryCacheMu.RLock()
	result := make(map[string]string, len(hostports))
	var uncachedHP []string
	var uncachedHosts []string
	for _, hp := range hostports {
		host, _, _ := net.SplitHostPort(hp)
		if host == "" {
			host = hp
		}
		if cc, ok := countryCache[host]; ok {
			result[hp] = cc
		} else {
			uncachedHP = append(uncachedHP, hp)
			uncachedHosts = append(uncachedHosts, host)
		}
	}
	countryCacheMu.RUnlock()

	if len(uncachedHosts) == 0 {
		return result
	}

	codes := batchGeoIP(uncachedHosts)

	countryCacheMu.Lock()
	for i, hp := range uncachedHP {
		if i < len(codes) && codes[i] != "" {
			host, _, _ := net.SplitHostPort(hp)
			if host == "" {
				host = hp
			}
			countryCache[host] = codes[i]
			result[hp] = codes[i]
		}
	}
	countryCacheMu.Unlock()

	return result
}

func batchGeoIP(hosts []string) []string {
	ips := make([]string, len(hosts))
	for i, h := range hosts {
		if addrs, err := net.LookupHost(h); err == nil && len(addrs) > 0 {
			ips[i] = addrs[0]
		} else {
			ips[i] = h
		}
	}

	body, _ := json.Marshal(ips)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(geoIPBatchURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	var results []struct {
		CountryCode string `json:"countryCode"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil
	}

	codes := make([]string, len(results))
	for i, r := range results {
		codes[i] = r.CountryCode
	}
	return codes
}

func countryFlag(code string) string {
	if len(code) != 2 {
		return ""
	}
	code = strings.ToUpper(code)
	return string(rune(code[0])-'A'+0x1F1E6) + string(rune(code[1])-'A'+0x1F1E6)
}
