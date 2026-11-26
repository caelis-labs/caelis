//go:build !windows

// Package atomicfile owns the platform-specific final replacement primitive
// shared by durable stores.
package atomicfile

import "os"

// Replace atomically replaces destination with source when the platform
// filesystem supports the operation.
func Replace(source, destination string) error {
	return os.Rename(source, destination)
}
