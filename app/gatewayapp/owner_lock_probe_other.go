//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package gatewayapp

func probeOwnerLock(string) string { return diagnosticLockUnknown }
