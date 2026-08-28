package main

import (
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

type dnsOverride struct {
	service     string
	originalDNS []string
}

func activeNetworkService() (string, error) {
	out, err := exec.Command("route", "-n", "get", "default").Output()
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

	out, err = exec.Command("networksetup", "-listallhardwareports").Output()
	if err != nil {
		return "", fmt.Errorf("listallhardwareports: %w", err)
	}
	lines := strings.Split(string(out), "\n")
	for i, line := range lines {
		if strings.Contains(line, "Device: "+ifaceName) && i > 0 {
			prev := lines[i-1]
			if strings.HasPrefix(prev, "Hardware Port: ") {
				return strings.TrimPrefix(prev, "Hardware Port: "), nil
			}
		}
	}
	return "", fmt.Errorf("network service for interface %s not found", ifaceName)
}

func getDNSServers(service string) ([]string, error) {
	out, err := exec.Command("networksetup", "-getdnsservers", service).Output()
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
	if err := exec.Command("networksetup", args...).Run(); err != nil {
		logger.Warn("DNS override: failed to set DNS", "err", err)
		return nil
	}

	_ = exec.Command("dscacheutil", "-flushcache").Run()
	_ = exec.Command("killall", "-HUP", "mDNSResponder").Run()

	logger.Info("DNS overridden", "service", service, "servers", servers, "original", original)
	return &dnsOverride{service: service, originalDNS: original}
}

func (d *dnsOverride) restore(logger *slog.Logger) {
	if d == nil {
		return
	}
	var err error
	if len(d.originalDNS) == 0 {
		err = exec.Command("networksetup", "-setdnsservers", d.service, "empty").Run()
	} else {
		args := append([]string{"-setdnsservers", d.service}, d.originalDNS...)
		err = exec.Command("networksetup", args...).Run()
	}
	if err != nil {
		logger.Warn("DNS restore failed", "service", d.service, "err", err)
		return
	}

	_ = exec.Command("dscacheutil", "-flushcache").Run()
	_ = exec.Command("killall", "-HUP", "mDNSResponder").Run()

	logger.Info("DNS restored", "service", d.service, "original", d.originalDNS)
}
