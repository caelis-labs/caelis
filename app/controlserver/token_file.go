package controlserver

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const bearerTokenFilename = "control-http.token"

// DefaultTokenFile returns the persistent bearer credential path used by the
// product Control server when CAELIS_CONTROL_TOKEN is not configured.
func DefaultTokenFile(storeDir string) string {
	return filepath.Join(filepath.Clean(storeDir), bearerTokenFilename)
}

// LoadOrCreateBearerToken loads one platform-secured token file or atomically
// publishes its first creation. Existing malformed or insecure files fail
// closed and are never replaced or chmodded into apparent safety.
func LoadOrCreateBearerToken(path string) (string, error) {
	path = filepath.Clean(path)
	if path == "." || path == string(filepath.Separator) {
		return "", errors.New("controlserver: bearer token file path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("controlserver: create bearer token directory: %w", err)
	}
	if token, err := readBearerTokenFile(path); err == nil {
		return token, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return createBearerTokenFile(path)
}

func createBearerTokenFile(path string) (token string, err error) {
	if existing, readErr := readBearerTokenFile(path); readErr == nil {
		return existing, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return "", readErr
	}
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", fmt.Errorf("controlserver: generate bearer token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("controlserver: create bearer token temporary file: %w", err)
	}
	temporaryPath := file.Name()
	defer func() {
		_ = os.Remove(temporaryPath)
	}()
	if err := secureTokenFile(file); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("controlserver: secure bearer token temporary file: %w", err)
	}
	if _, err := file.WriteString(token + "\n"); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("controlserver: write bearer token file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("controlserver: sync bearer token file: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("controlserver: close bearer token temporary file: %w", err)
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return readBearerTokenFile(path)
		}
		return "", fmt.Errorf("controlserver: publish bearer token file: %w", err)
	}
	if err := syncTokenDirectory(filepath.Dir(path)); err != nil {
		return "", err
	}
	loaded, err := readBearerTokenFile(path)
	if err != nil {
		return "", err
	}
	if loaded != token {
		return "", errors.New("controlserver: bearer token changed during creation")
	}
	return loaded, nil
}

func readBearerTokenFile(path string) (string, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("controlserver: bearer token file must be a regular non-symlink file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("controlserver: open bearer token file: %w", err)
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("controlserver: stat bearer token file: %w", err)
	}
	if !os.SameFile(before, after) || !after.Mode().IsRegular() {
		return "", errors.New("controlserver: bearer token file changed while opening")
	}
	if err := validateTokenFileSecurity(file, after); err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(file, 129))
	if err != nil {
		return "", fmt.Errorf("controlserver: read bearer token file: %w", err)
	}
	if len(data) > 128 || len(data) < 2 || data[len(data)-1] != '\n' || bytes.Contains(data[:len(data)-1], []byte{'\n'}) {
		return "", errors.New("controlserver: bearer token file has invalid contents")
	}
	token := string(data[:len(data)-1])
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != token {
		return "", errors.New("controlserver: bearer token file has invalid contents")
	}
	return token, nil
}
