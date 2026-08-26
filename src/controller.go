package main

import "time"

type cmdKind int

const (
	cmdSwitch cmdKind = iota
	cmdStop
)

// stopTimeout must exceed the disconnect timeout in client.go (30s) so the
// controller doesn't give up waiting while runWithReconnect is still tearing
// down the previous connection.
const stopTimeout = 35 * time.Second

type vpnCmd struct {
	kind    cmdKind
	profile Profile
	done    chan error
}
