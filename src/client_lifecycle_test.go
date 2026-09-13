package main

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestClientConnectRejectsWhileDisconnecting(t *testing.T) {
	client := &Client{state: stateDisconnecting}
	err := client.Connect("vless://invalid")
	if err == nil || !strings.Contains(err.Error(), "disconnect in progress") {
		t.Fatalf("Connect error = %v, want disconnect-in-progress error", err)
	}
}

func TestClientDisconnectTimeoutAllowsConnectAfterTunnelStops(t *testing.T) {
	client := &Client{cfg: Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, state: stateDisconnecting}
	tunnelStopped := make(chan error, 1)

	if err := client.Connect("invalid"); err == nil || !strings.Contains(err.Error(), "disconnect in progress") {
		t.Fatalf("Connect during pending stop = %v", err)
	}

	done := make(chan struct{})
	go func() {
		client.resetAfterTunnelStop(tunnelStopped)
		close(done)
	}()
	tunnelStopped <- nil

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("lifecycle gate was not released after tunnel stop")
	}

	client.mu.Lock()
	state := client.state
	client.mu.Unlock()
	if state != stateIdle {
		t.Fatalf("state after tunnel stop = %v, want idle", state)
	}

	// This link is intentionally unusable, but it is a real connection attempt:
	// the assertion ensures failure is no longer caused by the lifecycle gate.
	if err := client.Connect("vless://invalid"); err != nil && strings.Contains(err.Error(), "disconnect in progress") {
		t.Fatalf("Connect remained blocked after tunnel stop: %v", err)
	}
}
