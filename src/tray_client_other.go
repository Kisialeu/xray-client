//go:build !darwin

package main

import (
	"context"
	"log/slog"
)

func runTrayClient(_ context.Context, _ context.CancelFunc, _ *slog.Logger, _ string) {
	panic("--tray is only supported on macOS")
}
