package main

import (
	"os"
	"path/filepath"
	"strings"
)

func lastProfilePath() string {
	return filepath.Join("/usr/local/etc/xray-cli", "last-profile")
}

func saveLastProfile(name string) {
	p := lastProfilePath()
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, []byte(name), 0o600)
}

func loadLastProfile() string {
	data, err := os.ReadFile(lastProfilePath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
