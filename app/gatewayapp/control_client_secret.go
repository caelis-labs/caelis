package gatewayapp

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const controlClientCursorSecretFile = "control-client-cursor.key"

func loadOrCreateControlClientCursorSecret(storeDir string) ([]byte, error) {
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		return nil, fmt.Errorf("gatewayapp: create store directory for control client cursor: %w", err)
	}
	path := filepath.Join(storeDir, controlClientCursorSecretFile)
	for {
		secret, err := os.ReadFile(path)
		if err == nil {
			if len(secret) != 32 {
				return nil, fmt.Errorf("gatewayapp: control client cursor secret has invalid size %d", len(secret))
			}
			if chmodErr := os.Chmod(path, 0o600); chmodErr != nil {
				return nil, fmt.Errorf("gatewayapp: secure control client cursor secret: %w", chmodErr)
			}
			return append([]byte(nil), secret...), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("gatewayapp: read control client cursor secret: %w", err)
		}

		secret = make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, secret); err != nil {
			return nil, fmt.Errorf("gatewayapp: generate control client cursor secret: %w", err)
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("gatewayapp: create control client cursor secret: %w", err)
		}
		writeErr := writeAndSyncSecret(file, secret)
		closeErr := file.Close()
		if writeErr != nil {
			return nil, writeErr
		}
		if closeErr != nil {
			return nil, fmt.Errorf("gatewayapp: close control client cursor secret: %w", closeErr)
		}
		return append([]byte(nil), secret...), nil
	}
}

func writeAndSyncSecret(file *os.File, secret []byte) error {
	if _, err := file.Write(secret); err != nil {
		return fmt.Errorf("gatewayapp: write control client cursor secret: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("gatewayapp: sync control client cursor secret: %w", err)
	}
	return nil
}
