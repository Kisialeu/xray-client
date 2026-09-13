package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/xtls/xray-core/app/dispatcher"
	applog "github.com/xtls/xray-core/app/log"
	"github.com/xtls/xray-core/app/proxyman"
	commlog "github.com/xtls/xray-core/common/log"
	xraynet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf"

	_ "github.com/xtls/xray-core/app/dispatcher"
	_ "github.com/xtls/xray-core/app/proxyman/inbound"
	_ "github.com/xtls/xray-core/app/proxyman/outbound"
)

type xrayResult struct {
	instance *core.Instance
	address  string
	port     string
	protocol string
}

// buildXrayInstance creates an Xray core instance with a local SOCKS inbound
// and a protocol-specific outbound. When dialIP is supplied, the outbound
// connects to that resolved address while preserving the hostname for TLS SNI.
func buildXrayInstance(link string, socksAddr string, socksPort int, logLevel commlog.Severity, logType applog.LogType, dialIP ...string) (*xrayResult, error) {
	link = strings.TrimSpace(link)

	outbound, address, port, proto, err := buildOutbound(link)
	if err != nil {
		return nil, fmt.Errorf("build outbound: %w", err)
	}
	if len(dialIP) > 0 {
		var settings map[string]any
		if err := json.Unmarshal(*outbound.Settings, &settings); err != nil {
			return nil, err
		}
		for _, field := range []string{"vnext", "servers"} {
			if entries, ok := settings[field].([]any); ok {
				for _, entry := range entries {
					entry.(map[string]any)["address"] = dialIP[0]
				}
			}
		}
		raw, err := json.Marshal(settings)
		if err != nil {
			return nil, err
		}
		settingsJSON := json.RawMessage(raw)
		outbound.Settings = &settingsJSON
		if outbound.StreamSetting != nil && outbound.StreamSetting.TLSSettings != nil && outbound.StreamSetting.TLSSettings.ServerName == "" {
			outbound.StreamSetting.TLSSettings.ServerName = address
		}
	}

	obBuilt, err := outbound.Build()
	if err != nil {
		return nil, fmt.Errorf("build outbound config: %w", err)
	}

	inbound := buildSocksInbound(socksAddr, socksPort)
	ibBuilt, err := inbound.Build()
	if err != nil {
		return nil, fmt.Errorf("build inbound config: %w", err)
	}

	clientConfig := &core.Config{
		App: []*serial.TypedMessage{
			serial.ToTypedMessage(&applog.Config{
				ErrorLogType:  logType,
				AccessLogType: logType,
				ErrorLogLevel: logLevel,
				EnableDnsLog:  false,
			}),
			serial.ToTypedMessage(&dispatcher.Config{}),
			serial.ToTypedMessage(&proxyman.InboundConfig{}),
			serial.ToTypedMessage(&proxyman.OutboundConfig{}),
		},
		Inbound:  []*core.InboundHandlerConfig{ibBuilt},
		Outbound: []*core.OutboundHandlerConfig{obBuilt},
	}

	inst, err := core.New(clientConfig)
	if err != nil {
		return nil, fmt.Errorf("create xray instance: %w", err)
	}

	return &xrayResult{
		instance: inst,
		address:  address,
		port:     port,
		protocol: proto,
	}, nil
}

func buildSocksInbound(addr string, port int) *conf.InboundDetourConfig {
	p := conf.TransportProtocol("tcp")
	portU32 := uint32(port)
	in := &conf.InboundDetourConfig{
		Protocol: "socks",
		Tag:      "socks",
		StreamSetting: &conf.StreamConfig{
			Network: &p,
		},
		ListenOn: &conf.Address{Address: xraynet.ParseAddress(addr)},
		PortList: &conf.PortList{Range: []conf.PortRange{
			{From: portU32, To: portU32},
		}},
	}
	oset := json.RawMessage([]byte(`{"auth":"noauth","udp":true,"allowTransparent":false}`))
	in.Settings = &oset
	return in
}

func buildOutbound(link string) (*conf.OutboundDetourConfig, string, string, string, error) {
	lower := strings.ToLower(link)
	switch {
	case strings.HasPrefix(lower, "vless://"):
		return buildVLESSOutbound(link)
	case strings.HasPrefix(lower, "vmess://"):
		return buildVMessOutbound(link)
	case strings.HasPrefix(lower, "trojan://"):
		return buildTrojanOutbound(link)
	case strings.HasPrefix(lower, "ss://"):
		return buildSSOutbound(link)
	default:
		return nil, "", "", "", fmt.Errorf("unsupported proxy protocol")
	}
}

func buildVLESSOutbound(link string) (*conf.OutboundDetourConfig, string, string, string, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, "", "", "", fmt.Errorf("parse vless url: %w", err)
	}
	q := u.Query()

	address := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "443"
	}
	uuid := u.User.Username()
	flow := q.Get("flow")
	security := q.Get("security")
	transport := q.Get("type")
	if transport == "" {
		transport = "tcp"
	}

	out := &conf.OutboundDetourConfig{
		Protocol: "vless",
		Tag:      "proxy",
	}

	p := conf.TransportProtocol(transport)
	s := &conf.StreamConfig{
		Network:  &p,
		Security: security,
	}

	switch transport {
	case "tcp":
		s.TCPSettings = &conf.TCPConfig{
			HeaderConfig: json.RawMessage(`{"type":"none"}`),
		}
	case "ws":
		s.WSSettings = &conf.WebSocketConfig{
			Path:    q.Get("path"),
			Headers: map[string]string{"Host": q.Get("host")},
		}
	case "grpc":
		s.GRPCSettings = &conf.GRPCConfig{
			ServiceName: q.Get("serviceName"),
			Authority:   q.Get("authority"),
		}
	case "xhttp":
		mode := q.Get("mode")
		if mode == "" {
			mode = "auto"
		}
		s.XHTTPSettings = &conf.SplitHTTPConfig{
			Host: q.Get("host"),
			Path: q.Get("path"),
			Mode: mode,
		}
	case "httpupgrade":
		s.HTTPUPGRADESettings = &conf.HttpUpgradeConfig{
			Host: q.Get("host"),
			Path: q.Get("path"),
		}
	}

	fp := q.Get("fp")
	if fp == "" {
		fp = "chrome"
	}

	switch security {
	case "tls":
		s.TLSSettings = &conf.TLSConfig{
			Fingerprint: fp,
			ServerName:  q.Get("sni"),
		}
		if alpn := q.Get("alpn"); alpn != "" {
			alpns := conf.StringList(strings.Split(alpn, ","))
			s.TLSSettings.ALPN = &alpns
		}
	case "reality":
		s.REALITYSettings = &conf.REALITYConfig{
			Fingerprint: fp,
			ServerName:  q.Get("sni"),
			PublicKey:   q.Get("pbk"),
			ShortId:     q.Get("sid"),
			SpiderX:     q.Get("spx"),
		}
	}

	out.StreamSetting = s
	oset := outboundJSON("vnext", address, port, map[string]any{"users": []any{map[string]any{"id": uuid, "flow": flow, "encryption": "none"}}})
	out.Settings = &oset

	return out, address, port, "VLESS", nil
}

func buildVMessOutbound(link string) (*conf.OutboundDetourConfig, string, string, string, error) {
	encoded := strings.TrimPrefix(link, "vmess://")
	encoded = strings.TrimSpace(encoded)

	data, err := tryB64Decode(encoded)
	if err != nil {
		return nil, "", "", "", fmt.Errorf("vmess: decode: %w", err)
	}

	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, "", "", "", fmt.Errorf("vmess: invalid json: %w", err)
	}

	str := func(key string) string {
		if v, ok := obj[key].(string); ok {
			return v
		}
		if v, ok := obj[key].(float64); ok {
			return strconv.Itoa(int(v))
		}
		return ""
	}

	address := str("add")
	port := str("port")
	id := str("id")
	aid := str("aid")
	if aid == "" {
		aid = "0"
	}
	netType := str("net")
	if netType == "" {
		netType = "tcp"
	}
	tls := str("tls")
	sni := str("sni")
	host := str("host")
	path := str("path")
	fp := str("fp")
	if fp == "" {
		fp = "chrome"
	}

	out := &conf.OutboundDetourConfig{
		Protocol: "vmess",
		Tag:      "proxy",
	}

	p := conf.TransportProtocol(netType)
	s := &conf.StreamConfig{
		Network:  &p,
		Security: tls,
	}

	switch netType {
	case "tcp":
		s.TCPSettings = &conf.TCPConfig{
			HeaderConfig: json.RawMessage(`{"type":"none"}`),
		}
	case "ws":
		s.WSSettings = &conf.WebSocketConfig{
			Path:    path,
			Headers: map[string]string{"Host": host},
		}
	case "grpc":
		s.GRPCSettings = &conf.GRPCConfig{ServiceName: path}
	}

	if tls == "tls" {
		s.TLSSettings = &conf.TLSConfig{
			Fingerprint: fp,
			ServerName:  sni,
		}
	}

	out.StreamSetting = s
	alterID, _ := strconv.Atoi(aid)
	oset := outboundJSON("vnext", address, port, map[string]any{"users": []any{map[string]any{"id": id, "alterId": alterID, "security": "auto"}}})
	out.Settings = &oset

	return out, address, port, "VMess", nil
}

func buildTrojanOutbound(link string) (*conf.OutboundDetourConfig, string, string, string, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, "", "", "", fmt.Errorf("parse trojan url: %w", err)
	}
	q := u.Query()

	address := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "443"
	}
	password := u.User.Username()

	transport := q.Get("type")
	if transport == "" {
		transport = "tcp"
	}
	security := q.Get("security")
	if security == "" {
		security = "tls"
	}

	out := &conf.OutboundDetourConfig{
		Protocol: "trojan",
		Tag:      "proxy",
	}

	p := conf.TransportProtocol(transport)
	s := &conf.StreamConfig{
		Network:  &p,
		Security: security,
	}

	switch transport {
	case "tcp":
		s.TCPSettings = &conf.TCPConfig{
			HeaderConfig: json.RawMessage(`{"type":"none"}`),
		}
	case "ws":
		s.WSSettings = &conf.WebSocketConfig{
			Path:    q.Get("path"),
			Headers: map[string]string{"Host": q.Get("host")},
		}
	case "grpc":
		s.GRPCSettings = &conf.GRPCConfig{ServiceName: q.Get("serviceName")}
	}

	sni := q.Get("sni")
	if sni == "" {
		sni = address
	}
	fp := q.Get("fp")
	if fp == "" {
		fp = "chrome"
	}

	switch security {
	case "tls":
		s.TLSSettings = &conf.TLSConfig{
			Fingerprint: fp,
			ServerName:  sni,
		}
	case "reality":
		s.REALITYSettings = &conf.REALITYConfig{
			Fingerprint: fp,
			ServerName:  sni,
			PublicKey:   q.Get("pbk"),
			ShortId:     q.Get("sid"),
		}
	}

	out.StreamSetting = s
	oset := outboundJSON("servers", address, port, map[string]any{"password": password})
	out.Settings = &oset

	return out, address, port, "Trojan", nil
}

func buildSSOutbound(link string) (*conf.OutboundDetourConfig, string, string, string, error) {
	raw := strings.TrimPrefix(link, "ss://")
	if idx := strings.Index(raw, "#"); idx >= 0 {
		raw = raw[:idx]
	}

	var method, password, address, port string

	if idx := strings.Index(raw, "@"); idx >= 0 {
		decoded, err := tryB64Decode(raw[:idx])
		if err != nil {
			return nil, "", "", "", fmt.Errorf("ss: decode userinfo: %w", err)
		}
		parts := strings.SplitN(string(decoded), ":", 2)
		if len(parts) != 2 {
			return nil, "", "", "", fmt.Errorf("ss: invalid userinfo")
		}
		method, password = parts[0], parts[1]
		h, p, err := net.SplitHostPort(raw[idx+1:])
		if err != nil {
			return nil, "", "", "", fmt.Errorf("ss: invalid host:port: %w", err)
		}
		address, port = h, p
	} else {
		decoded, err := tryB64Decode(raw)
		if err != nil {
			return nil, "", "", "", fmt.Errorf("ss: decode: %w", err)
		}
		if atIdx := strings.Index(string(decoded), "@"); atIdx >= 0 {
			parts := strings.SplitN(string(decoded[:atIdx]), ":", 2)
			if len(parts) != 2 {
				return nil, "", "", "", fmt.Errorf("ss: invalid format")
			}
			method, password = parts[0], parts[1]
			h, p, err := net.SplitHostPort(string(decoded[atIdx+1:]))
			if err != nil {
				return nil, "", "", "", fmt.Errorf("ss: invalid host:port: %w", err)
			}
			address, port = h, p
		}
	}

	if address == "" {
		return nil, "", "", "", fmt.Errorf("ss: could not parse address")
	}

	out := &conf.OutboundDetourConfig{
		Protocol: "shadowsocks",
		Tag:      "proxy",
	}

	p := conf.TransportProtocol("tcp")
	out.StreamSetting = &conf.StreamConfig{Network: &p}

	oset := outboundJSON("servers", address, port, map[string]any{"method": method, "password": password})
	out.Settings = &oset

	return out, address, port, "Shadowsocks", nil
}

func outboundJSON(field, address, port string, fields map[string]any) json.RawMessage {
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return json.RawMessage(`null`)
	}
	fields["address"], fields["port"] = address, p
	raw, _ := json.Marshal(map[string]any{field: []any{fields}})
	return raw
}

func tryB64Decode(s string) ([]byte, error) {
	clean := strings.NewReplacer("\n", "", "\r", "", " ", "").Replace(s)
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if d, err := enc.DecodeString(clean); err == nil {
			return d, nil
		}
	}
	return nil, fmt.Errorf("base64 decode failed")
}
