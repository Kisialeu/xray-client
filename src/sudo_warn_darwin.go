package main

import (
	"fmt"
	"os"
	"os/exec"
)

func warnIfNotRoot(tray bool) {
	if os.Getuid() == 0 {
		return
	}
	if tray {
		//nolint:gosec
		_ = exec.Command("osascript", "-e",
			`display dialog "XRay VPN must be run with sudo to create a TUN device.\n\nRun: sudo xray-cli ..." `+
				`with title "XRay VPN — Permission Required" buttons {"OK"} default button "OK" with icon caution`,
		).Run()
	} else {
		fmt.Fprintln(os.Stderr, "warning: xray-cli needs root to create a TUN device — run with sudo")
	}
}
