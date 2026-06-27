//go:build !darwin

package main

import (
	"context"
	"log/slog"
)

func runTray(_ context.Context, _ context.CancelFunc, _ *slog.Logger, _ *state, _ Profile, _ []Profile, _ int) {
	panic("--tray is only supported on macOS")
}
