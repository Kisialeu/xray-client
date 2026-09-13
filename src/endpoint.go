package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var pinnedEndpoints sync.Map

func resolveEndpoint(host string) (*net.IPAddr, error) {
	if value, ok := pinnedEndpoints.Load(host); ok {
		return &net.IPAddr{IP: value.(net.IP)}, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() == nil {
			return nil, fmt.Errorf("IPv6 proxy endpoints are not supported by the IPv4 tunnel")
		}
		return &net.IPAddr{IP: ip.To4()}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	path := ""
	if subscriptionCacheDir != "" {
		path = filepath.Join(subscriptionCacheDir, fmt.Sprintf("endpoint-%x", sha256.Sum256([]byte(host))))
	}
	if err == nil && len(ips) > 0 {
		if path != "" {
			if os.MkdirAll(subscriptionCacheDir, 0700) == nil {
				f, e := os.CreateTemp(subscriptionCacheDir, ".endpoint-*")
				if e == nil {
					defer os.Remove(f.Name())
					_, e = f.WriteString(ips[0].String())
					closeErr := f.Close()
					if e == nil && closeErr == nil {
						_ = os.Rename(f.Name(), path)
					}
				}
			}
		}
		return &net.IPAddr{IP: ips[0]}, nil
	}
	if path != "" {
		if fi, e := os.Lstat(path); e == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0077 == 0 && time.Since(fi.ModTime()) < 7*24*time.Hour {
			data, e := os.ReadFile(path)
			if ip := net.ParseIP(strings.TrimSpace(string(data))); e == nil && ip.To4() != nil {
				return &net.IPAddr{IP: ip.To4()}, nil
			}
		}
	}
	return nil, fmt.Errorf("proxy hostname has no reachable IPv4 resolution or recent cached address")
}
