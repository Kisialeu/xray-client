//go:build !darwin

package main

import "log/slog"

type dnsOverride struct{}

func overrideDNS(_ []string, _ *slog.Logger) *dnsOverride { return nil }
func (d *dnsOverride) restore(_ *slog.Logger)              {}
