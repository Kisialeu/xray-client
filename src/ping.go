package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

const pingTimeout = 3 * time.Second

type PingResult struct {
	Name      string `json:"name"`
	LatencyMs int    `json:"latency_ms"`
	Country   string `json:"country,omitempty"`
	Flag      string `json:"flag,omitempty"`
}

func pingProfiles(profiles []Profile) []PingResult {
	results := make([]PingResult, len(profiles))

	hostports := make([]string, len(profiles))
	for i, p := range profiles {
		hostports[i] = extractHostPort(p.Link)
	}

	var nonEmpty []string
	for _, hp := range hostports {
		if hp != "" {
			nonEmpty = append(nonEmpty, hp)
		}
	}
	countries := resolveCountries(nonEmpty)

	var wg sync.WaitGroup
	for i, p := range profiles {
		wg.Add(1)
		go func(idx int, prof Profile) {
			defer wg.Done()
			hp := hostports[idx]
			results[idx] = PingResult{Name: prof.Name, LatencyMs: -1}

			if cc := countries[hp]; cc != "" {
				results[idx].Country = cc
				results[idx].Flag = countryFlag(cc)
			}

			if hp == "" {
				return
			}
			start := time.Now()
			conn, err := net.DialTimeout("tcp", hp, pingTimeout)
			if err != nil {
				return
			}
			conn.Close()
			results[idx].LatencyMs = int(time.Since(start).Milliseconds())
		}(i, p)
	}

	wg.Wait()
	return results
}

func extractHostPort(link string) string {
	if strings.HasPrefix(link, "vmess://") {
		return extractVMessHostPort(link)
	}
	u, err := url.Parse(link)
	if err != nil || u.Host == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(u.Host); err != nil {
		return net.JoinHostPort(u.Host, "443")
	}
	return u.Host
}

func extractVMessHostPort(link string) string {
	encoded := strings.TrimPrefix(link, "vmess://")
	encoded = strings.TrimSpace(encoded)

	data := tryBase64DecodeBytes(encoded)
	if data == nil {
		return ""
	}

	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return ""
	}

	addr, _ := obj["add"].(string)
	if addr == "" {
		return ""
	}

	port := "443"
	switch p := obj["port"].(type) {
	case string:
		if p != "" {
			port = p
		}
	case float64:
		port = fmt.Sprintf("%d", int(p))
	}

	return net.JoinHostPort(addr, port)
}
