package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func acquireSessionLock() (*os.File, error) {
	const dir = "/usr/local/etc/xray-cli"
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	fd, err := unix.Open(dir+"/session.lock", unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "session.lock")
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another VPN session is running; stop it before starting or releasing protection")
	}
	return f, nil
}
