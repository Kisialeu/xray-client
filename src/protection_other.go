//go:build !darwin

package main

import "fmt"

func beginProtection(_ []string) error {
	return fmt.Errorf("protected mode is currently supported only on macOS")
}
func protectInterface(_ string) error { return nil }
func releaseProtection() error {
	return fmt.Errorf("protected mode is currently supported only on macOS")
}
