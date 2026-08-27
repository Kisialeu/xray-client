package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

func lastProfilePath() string {
	return filepath.Join("/usr/local/etc/xray-cli", "last-profile")
}

func saveLastProfile(name string) {
	p := lastProfilePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		slog.Warn("save last profile: mkdir", "err", err)
		return
	}
	if err := os.WriteFile(p, []byte(name), 0o644); err != nil {
		slog.Warn("save last profile: write", "err", err)
	}
}

func loadLastProfile() string {
	data, err := os.ReadFile(lastProfilePath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
