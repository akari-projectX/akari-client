//go:build !linux && !windows && !darwin

package device

// MachineID is unavailable on this platform.
func MachineID() string { return "" }
