package main

import (
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type ServerInfo struct {
	PublicIP  string `json:"public_ip"`
	Country   string `json:"country"`
	Flag      string `json:"flag"`
	Protocol  string `json:"protocol"`
	Server    string `json:"server"`
	DNSServer string `json:"dns_server,omitempty"`
	IPLeak    bool   `json:"ip_leak"`
}

const publicIPURL = "https://api.ipify.org"

func gatherServerInfo(profile Profile) ServerInfo {
	info := ServerInfo{
		Protocol: extractProtocol(profile.Link),
		Server:   extractHostPort(profile.Link),
	}

	client := &http.Client{Timeout: 5 * time.Second}

	if resp, err := client.Get(publicIPURL); err == nil {
		defer resp.Body.Close()
		if body, err := io.ReadAll(resp.Body); err == nil {
			info.PublicIP = strings.TrimSpace(string(body))
		}
	}

	if info.PublicIP != "" {
		codes := batchGeoIP([]string{info.PublicIP})
		if len(codes) > 0 && codes[0] != "" {
			info.Country = codes[0]
			info.Flag = countryFlag(codes[0])
		}

		serverHost, _, _ := net.SplitHostPort(info.Server)
		if serverHost == "" {
			serverHost = info.Server
		}
		if addrs, err := net.LookupHost(serverHost); err == nil {
			for _, addr := range addrs {
				if addr == info.PublicIP {
					info.IPLeak = true
					break
				}
			}
		}
	}

	if addrs, err := net.LookupHost("whoami.akamai.net"); err == nil && len(addrs) > 0 {
		info.DNSServer = addrs[0]
	}

	return info
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
