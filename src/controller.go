package main

import (
	"fmt"
	"sync"
	"time"
)

// ConnectionSettings controls automatic lifecycle behavior for the current
// process session. Settings are intentionally not persisted.
type ConnectionSettings struct {
	// AutoConnect starts the selected profile when the controller is idle.
	AutoConnect bool `json:"auto_connect"`
	// AutoReconnect retries a connection after an unexpected session failure.
	AutoReconnect bool `json:"auto_reconnect"`
}

// connectionSettingsPatch is the partial form accepted by POST /settings.
// Pointers distinguish an omitted setting from an explicit false value.
type connectionSettingsPatch struct {
	AutoConnect   *bool `json:"auto_connect"`
	AutoReconnect *bool `json:"auto_reconnect"`
}

func defaultConnectionSettings() ConnectionSettings {
	return ConnectionSettings{AutoConnect: true, AutoReconnect: true}
}

func (p connectionSettingsPatch) apply(current ConnectionSettings) (ConnectionSettings, error) {
	if p.AutoConnect == nil && p.AutoReconnect == nil {
		return ConnectionSettings{}, fmt.Errorf("at least one connection setting is required")
	}
	if p.AutoConnect != nil {
		current.AutoConnect = *p.AutoConnect
	}
	if p.AutoReconnect != nil {
		current.AutoReconnect = *p.AutoReconnect
	}
	return current, nil
}

// connectionSettingsState is shared by the serialized controller and its
// reconnect loop. Closing changed wakes a reconnect wait without starting a
// second lifecycle goroutine.
type connectionSettingsState struct {
	mu      sync.RWMutex
	value   ConnectionSettings
	changed chan struct{}
}

func newConnectionSettingsState(value ConnectionSettings) *connectionSettingsState {
	return &connectionSettingsState{value: value, changed: make(chan struct{})}
}

func (s *connectionSettingsState) snapshot() ConnectionSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.value
}

func (s *connectionSettingsState) update(patch connectionSettingsPatch) (ConnectionSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := patch.apply(s.value)
	if err != nil {
		return ConnectionSettings{}, err
	}
	s.value = next
	close(s.changed)
	s.changed = make(chan struct{})
	return next, nil
}

func (s *connectionSettingsState) changeSignal() <-chan struct{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.changed
}

type connectionStatus string

const (
	statusDisconnected connectionStatus = "disconnected"
	statusConnecting   connectionStatus = "connecting"
	statusReconnecting connectionStatus = "reconnecting"
	statusConnected    connectionStatus = "connected"
	statusFailed       connectionStatus = "operation_failed"
)

func (s connectionStatus) valid() bool {
	switch s {
	case statusDisconnected, statusConnecting, statusReconnecting, statusConnected, statusFailed:
		return true
	default:
		return false
	}
}

type cmdKind int

const (
	cmdSwitch cmdKind = iota
	cmdStop
	cmdReconnect
	cmdSettings
)

// stopTimeout must exceed the disconnect timeout in client.go (30s) so the
// controller doesn't give up waiting while runWithReconnect is still tearing
// down the previous connection.
const stopTimeout = 35 * time.Second

type vpnCmd struct {
	kind     cmdKind
	profile  Profile
	settings connectionSettingsPatch
	done     chan error
}
