package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"time"
)

// startPprofServer starts a localhost-only pprof debug HTTP server, gated by
// the XRAY_DEBUG_PPROF environment variable. It is intentionally NOT wired
// into main() automatically - call maybeStartPprof(ctx, logger) once from
// main(), after logger construction, e.g.:
//
//	maybeStartPprof(ctx, logger)
//
// Off by default. Enable with:
//
//	XRAY_DEBUG_PPROF=1 sudo ./xray-cli --config config.yaml
//
// Optionally override the bind address (default 127.0.0.1:6060):
//
//	XRAY_DEBUG_PPROF_ADDR=127.0.0.1:6061 XRAY_DEBUG_PPROF=1 sudo ./xray-cli ...
//
// Binds to loopback only - never 0.0.0.0 - since this exposes heap contents
// and goroutine stacks; do not enable in any build reachable from outside
// the local machine.
func maybeStartPprof(ctx context.Context, logger *slog.Logger) {
	if os.Getenv("XRAY_DEBUG_PPROF") == "" {
		return
	}

	addr := os.Getenv("XRAY_DEBUG_PPROF_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6060"
	}

	host, _, err := net.SplitHostPort(addr)
	if err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		logger.Error("pprof debug server refused: address must be loopback", "addr", addr)
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		// No overall WriteTimeout: CPU/trace profiles run for caller-specified
		// durations (e.g. ?seconds=30) and a short timeout would truncate them.
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		logger.Error("pprof debug server: listen failed", "addr", addr, "err", err)
		return
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	go func() {
		logger.Warn("pprof debug server enabled - local-only, disable in production", "addr", addr)
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			logger.Error("pprof debug server stopped", "err", err)
		}
	}()
}
