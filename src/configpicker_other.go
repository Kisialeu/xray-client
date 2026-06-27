//go:build !darwin

package main

// PickConfigFile is a no-op on non-darwin platforms — there is no native
// file picker wired up here for other OSes. Always returns "", which the
// caller treats the same as the user cancelling a picker on macOS: fall
// back to the existing --help-and-exit error path.
func PickConfigFile() string {
	return ""
}
