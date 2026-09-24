//go:build !linux && !windows

package collector

// machineID has no portable source outside Linux and Windows.
func machineID() string { return "" }
