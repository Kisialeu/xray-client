//go:build !darwin

package main

import (
	"fmt"
	"os"
)

func warnIfNotRoot(_ bool) {
	if os.Getuid() == 0 {
		return
	}
	fmt.Fprintln(os.Stderr, "warning: xray-cli needs root to create a TUN device — run with sudo")
}
