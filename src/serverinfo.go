package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type ServerInfo struct {
	PublicIP   string `json:"public_ip"`
	Country    string `json:"country"`
	Flag       string `json:"flag"`
	Protocol   string `json:"protocol"`
	Server     string `json:"server"`
	DNSServer  string `json:"dns_server,omitempty"`
	IPLeak     bool   `json:"ip_leak"`
	LeakStatus string `json:"leak_status"`
}

const publicIPURL = "https://api.ipify.org"

func gatherServerInfo(profile Profile) ServerInfo {
	info := ServerInfo{
		Protocol:   extractProtocol(profile.Link),
		Server:     extractHostPort(profile.Link),
		LeakStatus: "unknown",
	}

	client := &http.Client{Timeout: 5 * time.Second}

	if resp, err := client.Get(publicIPURL); err == nil {
		defer resp.Body.Close()
		if body, err := io.ReadAll(resp.Body); err == nil {
			info.PublicIP = strings.TrimSpace(string(body))
		}
	}

	if info.PublicIP != "" {
		info.Country, info.Flag = geoIPSingle(client, info.PublicIP)

		// An entry IP need not equal egress. A single IPv4 observation
		// cannot establish DNS or IPv6 leak protection.
	}

	if addrs, err := net.LookupHost("whoami.akamai.net"); err == nil && len(addrs) > 0 {
		info.DNSServer = addrs[0]
	}

	return info
}

func geoIPSingle(client *http.Client, ip string) (country, flag string) {
	resp, err := client.Get("https://ipinfo.io/" + ip + "/json")
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	var result struct {
		Country string `json:"country"`
	}
	if json.NewDecoder(resp.Body).Decode(&result) == nil && result.Country != "" {
		return result.Country, countryFlag(result.Country)
	}
	return "", ""
}

func extractProtocol(link string) string {
	if strings.HasPrefix(link, "vmess://") {
		return "VMess"
	}
	u, err := url.Parse(link)
	if err != nil {
		return "unknown"
	}
	switch u.Scheme {
	case "vless":
		return "VLESS"
	case "trojan":
		return "Trojan"
	case "ss":
		return "Shadowsocks"
	default:
		return u.Scheme
	}
}
