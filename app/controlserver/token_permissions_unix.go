//go:build unix

package controlserver

import (
	"fmt"
	"os"
	"syscall"
)

func secureTokenFile(file *os.File) error {
	return file.Chmod(0o600)
}

func validateTokenFileSecurity(_ *os.File, info os.FileInfo) error {
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("controlserver: bearer token file mode is %04o, want 0600", info.Mode().Perm())
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("controlserver: bearer token file owner is unavailable")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("controlserver: bearer token file is not owned by the current user")
	}
	return nil
}

func syncTokenDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("controlserver: open bearer token directory for sync: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return fmt.Errorf("controlserver: sync bearer token directory: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("controlserver: close bearer token directory: %w", closeErr)
	}
	return nil
}
