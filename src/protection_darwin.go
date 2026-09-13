package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

const protectionAnchor = "com.apple/zzzz.xray-client"
const pfTokenFile = "/usr/local/etc/xray-cli/pf-token"

var protection struct {
	sync.Mutex
	active    bool
	endpoints []string
}

func protectionActive() bool {
	protection.Lock()
	defer protection.Unlock()
	return protection.active
}

func pfCommand(input string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/sbin/pfctl", args...)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("pfctl %v failed: %w", args, err)
	}
	return string(out), nil
}

func protectionRules(endpoints []string, iface string) (string, error) {
	var b strings.Builder
	b.WriteString("pass out quick on lo0 all no state\n")
	if iface != "" {
		if !regexp.MustCompile(`^utun[0-9]+$`).MatchString(iface) {
			return "", fmt.Errorf("invalid tunnel interface")
		}
		fmt.Fprintf(&b, "pass out quick on %s inet all no state\n", iface)
	}
	for _, endpoint := range endpoints {
		host, port, err := net.SplitHostPort(endpoint)
		if err != nil || net.ParseIP(host).To4() == nil || !regexp.MustCompile(`^[0-9]{1,5}$`).MatchString(port) {
			return "", fmt.Errorf("invalid protected endpoint")
		}
		fmt.Fprintf(&b, "pass out quick inet proto tcp to %s port %s flags S/SA keep state (if-bound)\n", host, port)
	}
	b.WriteString("pass out quick inet proto udp from any port 68 to any port 67 no state\nblock drop out quick all\n")
	return b.String(), nil
}

// Leaves the blocking policy installed on disconnect and process failure.
// Explicit --release-protection is required to restore direct connectivity.
func beginProtection(endpoints []string) error {
	protection.Lock()
	defer protection.Unlock()
	mainRules, err := pfCommand("", "-sr")
	if err != nil {
		return err
	}
	// Do not replace or silently coexist with a custom host filter policy.
	found := false
	for _, line := range strings.Split(mainRules, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "No ALTQ") || strings.HasPrefix(line, "ALTQ") {
			continue
		}
		if line == `anchor "com.apple/*" all` {
			found = true
			continue
		}
		return fmt.Errorf("protected mode requires the stock macOS PF filter anchor; custom rules found")
	}
	if !found {
		return fmt.Errorf("stock macOS PF anchor is not loaded")
	}
	protection.endpoints = endpoints
	rules, err := protectionRules(endpoints, "")
	if err != nil {
		return err
	}
	if _, err = pfCommand(rules, "-a", protectionAnchor, "-nf", "-"); err != nil {
		return err
	}
	if _, err = pfCommand(rules, "-a", protectionAnchor, "-f", "-"); err != nil {
		return err
	}
	if _, err = os.Stat(pfTokenFile); os.IsNotExist(err) {
		out, enableErr := pfCommand("", "-E")
		if enableErr != nil {
			return enableErr
		}
		match := regexp.MustCompile(`Token\s*:\s*([0-9]+)`).FindStringSubmatch(out)
		if len(match) != 2 {
			return fmt.Errorf("PF enabled but reference token was unavailable; protection rules retained")
		}
		if err = os.WriteFile(pfTokenFile, []byte(match[1]), 0600); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	// Existing states are intentionally preserved. Flushing the global PF
	// state table would terminate unrelated SSH and application connections.
	// New connections are filtered by this anchor; existing states expire
	// naturally or can be closed by the owning application.
	protection.active = true
	return nil
}

func protectInterface(iface string) error {
	protection.Lock()
	defer protection.Unlock()
	if !protection.active {
		return nil
	}
	rules, err := protectionRules(protection.endpoints, iface)
	if err != nil {
		return err
	}
	_, err = pfCommand(rules, "-a", protectionAnchor, "-f", "-")
	return err
}

func releaseProtection() error {
	if _, err := pfCommand("", "-a", protectionAnchor, "-F", "rules"); err != nil {
		return err
	}
	data, err := os.ReadFile(pfTokenFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !regexp.MustCompile(`^[0-9]+$`).Match(data) {
		return fmt.Errorf("invalid saved PF reference")
	}
	if _, err = pfCommand("", "-X", string(data)); err != nil {
		return err
	}
	return os.Remove(pfTokenFile)
}
