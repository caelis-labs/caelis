package gatewayapp

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

const (
	controlClientCursorSecretFile       = "cursor.key"
	legacyControlClientCursorSecretFile = "control-client-cursor.key"
)

func loadOrCreateControlClientCursorSecret(storeDir string) ([]byte, error) {
	controlDir := controlStoreRoot(storeDir)
	if err := os.MkdirAll(controlDir, 0o700); err != nil {
		return nil, fmt.Errorf("gatewayapp: create store directory for control client cursor: %w", err)
	}
	if err := requireSecureControlStoreDirectory(controlDir); err != nil {
		return nil, err
	}
	if err := os.Chmod(controlDir, 0o700); err != nil {
		return nil, fmt.Errorf("gatewayapp: secure store directory for control client cursor: %w", err)
	}
	path := filepath.Join(controlDir, controlClientCursorSecretFile)
	legacyPath := filepath.Join(storeDir, legacyControlClientCursorSecretFile)
	for {
		secret, err := readControlClientCursorSecret(path)
		if err == nil {
			if len(secret) != 32 {
				legacy, legacyErr := readControlClientCursorSecret(legacyPath)
				if legacyErr == nil && len(legacy) == 32 {
					if err := os.Remove(path); err != nil {
						return nil, fmt.Errorf("gatewayapp: remove incomplete control client cursor secret: %w", err)
					}
					if err := syncControlStoreDirectory(controlDir); err != nil {
						return nil, fmt.Errorf("gatewayapp: sync removal of incomplete control client cursor secret: %w", err)
					}
					continue
				}
				return nil, fmt.Errorf("gatewayapp: control client cursor secret has invalid size %d", len(secret))
			}
			if chmodErr := os.Chmod(path, 0o600); chmodErr != nil {
				return nil, fmt.Errorf("gatewayapp: secure control client cursor secret: %w", chmodErr)
			}
			if err := removeRetiredControlClientCursorSecret(legacyPath); err != nil {
				return nil, err
			}
			return append([]byte(nil), secret...), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("gatewayapp: read control client cursor secret: %w", err)
		}

		secret, err = readControlClientCursorSecret(legacyPath)
		migratingLegacy := err == nil
		if migratingLegacy {
			if len(secret) != 32 {
				return nil, fmt.Errorf("gatewayapp: legacy control client cursor secret has invalid size %d", len(secret))
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("gatewayapp: read legacy control client cursor secret: %w", err)
		} else {
			secret = make([]byte, 32)
			if _, err := io.ReadFull(rand.Reader, secret); err != nil {
				return nil, fmt.Errorf("gatewayapp: generate control client cursor secret: %w", err)
			}
		}
		file, err := os.CreateTemp(controlDir, ".cursor.key.*.tmp")
		if err != nil {
			return nil, fmt.Errorf("gatewayapp: stage control client cursor secret: %w", err)
		}
		tempPath := file.Name()
		removeTemp := true
		defer func() {
			if removeTemp {
				_ = os.Remove(tempPath)
			}
		}()
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("gatewayapp: secure staged control client cursor secret: %w", err)
		}
		writeErr := writeAndSyncSecret(file, secret)
		closeErr := file.Close()
		if writeErr != nil {
			return nil, writeErr
		}
		if closeErr != nil {
			return nil, fmt.Errorf("gatewayapp: close control client cursor secret: %w", closeErr)
		}
		if err := os.Link(tempPath, path); errors.Is(err, os.ErrExist) {
			if removeErr := os.Remove(tempPath); removeErr != nil {
				return nil, fmt.Errorf("gatewayapp: remove redundant staged control client cursor secret: %w", removeErr)
			}
			removeTemp = false
			continue
		} else if err != nil {
			return nil, fmt.Errorf("gatewayapp: install control client cursor secret: %w", err)
		}
		if err := syncControlStoreDirectory(controlDir); err != nil {
			return nil, fmt.Errorf("gatewayapp: sync installed control client cursor secret: %w", err)
		}
		if removeErr := os.Remove(tempPath); removeErr != nil {
			return nil, fmt.Errorf("gatewayapp: remove staged control client cursor secret: %w", removeErr)
		}
		removeTemp = false
		if migratingLegacy {
			if err := removeRetiredControlClientCursorSecret(legacyPath); err != nil {
				return nil, err
			}
		}
		return append([]byte(nil), secret...), nil
	}
}

func writeAndSyncSecret(file *os.File, secret []byte) error {
	written, err := file.Write(secret)
	if err != nil {
		return fmt.Errorf("gatewayapp: write control client cursor secret: %w", err)
	}
	if written != len(secret) {
		return fmt.Errorf("gatewayapp: write control client cursor secret: %w", io.ErrShortWrite)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("gatewayapp: sync control client cursor secret: %w", err)
	}
	return nil
}

func readControlClientCursorSecret(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("gatewayapp: control client cursor secret is not a secure regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, errors.New("gatewayapp: control client cursor secret changed while opening")
	}
	return io.ReadAll(io.LimitReader(file, 33))
}

func removeRetiredControlClientCursorSecret(path string) error {
	if err := os.Remove(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("gatewayapp: remove retired control client cursor secret: %w", err)
	}
	if err := syncControlStoreDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("gatewayapp: sync retired control client cursor secret removal: %w", err)
	}
	return nil
}

func syncControlStoreDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
