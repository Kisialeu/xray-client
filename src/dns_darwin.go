package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type dnsOverride struct {
	service     string
	originalDNS []string
	journal     string
}

func dnsCommand(name string, args ...string) *exec.Cmd {
	// Commands run with a bounded lifetime even if SystemConfiguration hangs.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	cmd := exec.CommandContext(ctx, name, args...)
	go func() { <-ctx.Done(); cancel() }()
	return cmd
}

func activeNetworkService() (string, error) {
	out, err := dnsCommand("/sbin/route", "-n", "get", "default").Output()
	if err != nil {
		return "", fmt.Errorf("route get default: %w", err)
	}
	var ifaceName string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "interface:") {
			ifaceName = strings.TrimSpace(strings.TrimPrefix(line, "interface:"))
			break
		}
	}
	if ifaceName == "" {
		return "", fmt.Errorf("no default interface found")
	}

	out, err = dnsCommand("/usr/sbin/networksetup", "-listnetworkserviceorder").Output()
	if err != nil {
		return "", fmt.Errorf("listallhardwareports: %w", err)
	}
	service := ""
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "(") && !strings.HasPrefix(line, "(Hardware") {
			if i := strings.Index(line, ") "); i > 0 {
				service = line[i+2:]
			}
		}
		if strings.Contains(line, "Device: "+ifaceName+")") && service != "" {
			return service, nil
		}
	}
	return "", fmt.Errorf("network service for interface %s not found", ifaceName)
}

func getDNSServers(service string) ([]string, error) {
	out, err := dnsCommand("/usr/sbin/networksetup", "-getdnsservers", service).Output()
	if err != nil {
		return nil, err
	}
	s := strings.TrimSpace(string(out))
	if strings.Contains(s, "There aren't any DNS Servers set") {
		return nil, nil
	}
	var servers []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			servers = append(servers, line)
		}
	}
	return servers, nil
}

func overrideDNS(servers []string, logger *slog.Logger) *dnsOverride {
	if len(servers) == 0 {
		return nil
	}
	for _, server := range servers {
		if net.ParseIP(server).To4() == nil {
			logger.Error("DNS override requires IPv4 resolver addresses")
			return nil
		}
	}
	journal := ""
	if subscriptionCacheDir != "" {
		journal = filepath.Join(subscriptionCacheDir, "dns-state.json")
		if data, err := os.ReadFile(journal); err == nil {
			var saved struct {
				Service string
				DNS     []string
			}
			if json.Unmarshal(data, &saved) != nil || saved.Service == "" {
				logger.Error("invalid DNS recovery journal")
				return nil
			}
			old := &dnsOverride{service: saved.Service, originalDNS: saved.DNS, journal: journal}
			old.restore(logger)
			if _, err := os.Stat(journal); !os.IsNotExist(err) {
				return nil
			}
		}
	}

	service, err := activeNetworkService()
	if err != nil {
		logger.Warn("DNS override: failed to detect network service", "err", err)
		return nil
	}

	original, err := getDNSServers(service)
	if err != nil {
		logger.Warn("DNS override: failed to get current DNS", "err", err)
		return nil
	}

	args := append([]string{"-setdnsservers", service}, servers...)
	if journal != "" {
		if err := os.MkdirAll(subscriptionCacheDir, 0700); err != nil {
			return nil
		}
		data, _ := json.Marshal(struct {
			Service string
			DNS     []string
		}{service, original})
		f, err := os.OpenFile(journal, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return nil
		}
		_, err = f.Write(data)
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			return nil
		}
	}
	if err := dnsCommand("/usr/sbin/networksetup", args...).Run(); err != nil {
		logger.Warn("DNS override: failed to set DNS", "err", err)
		return nil
	}

	_ = dnsCommand("/usr/bin/dscacheutil", "-flushcache").Run()
	_ = dnsCommand("/usr/bin/killall", "-HUP", "mDNSResponder").Run()

	logger.Info("DNS overridden", "service", service, "servers", servers, "original", original)
	return &dnsOverride{service: service, originalDNS: original, journal: journal}
}

func (d *dnsOverride) restore(logger *slog.Logger) {
	if d == nil {
		return
	}
	var err error
	if len(d.originalDNS) == 0 {
		err = dnsCommand("/usr/sbin/networksetup", "-setdnsservers", d.service, "empty").Run()
	} else {
		args := append([]string{"-setdnsservers", d.service}, d.originalDNS...)
		err = dnsCommand("/usr/sbin/networksetup", args...).Run()
	}
	if err != nil {
		logger.Warn("DNS restore failed", "service", d.service, "err", err)
		return
	}

	if d.journal != "" {
		_ = os.Remove(d.journal)
	}
	_ = dnsCommand("/usr/bin/dscacheutil", "-flushcache").Run()
	_ = dnsCommand("/usr/bin/killall", "-HUP", "mDNSResponder").Run()

	logger.Info("DNS restored", "service", d.service, "original", d.originalDNS)
}
