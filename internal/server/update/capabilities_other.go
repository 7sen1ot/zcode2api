//go:build !linux

package update

func checkUpdateCapabilities(string) error { return nil }
