package codexauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const credentialSchemaVersion = 1

type storedCredentials struct {
	Version      int    `json:"version"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
	AccessToken  string `json:"access_token,omitempty"`
	ExpiresAt    int64  `json:"expires_at,omitempty"`
}

func (s storedCredentials) normalized() storedCredentials {
	if s.Version == 0 {
		s.Version = credentialSchemaVersion
	}
	s.RefreshToken = strings.TrimSpace(s.RefreshToken)
	s.AccountID = strings.TrimSpace(s.AccountID)
	s.AccessToken = strings.TrimSpace(s.AccessToken)
	if s.AccessToken == "" {
		s.ExpiresAt = 0
	}
	return s
}

func (s storedCredentials) valid() bool {
	s = s.normalized()
	return s.Version == credentialSchemaVersion && s.RefreshToken != "" && s.AccountID != ""
}

func (m *Manager) loadStoredLocked() error {
	if m.loaded && m.stored.valid() {
		return nil
	}
	stored, err := readStoredCredentials(m.credentialPath)
	if errors.Is(err, os.ErrNotExist) {
		m.loaded = true
		m.stored = storedCredentials{}
		return nil
	}
	if err != nil {
		return err
	}
	m.loaded = true
	m.stored = stored
	if access := accessCredentialsFromStored(stored); access.usableAt(m.now(), refreshSkew) {
		m.access = access
	}
	return nil
}

func accessCredentialsFromStored(stored storedCredentials) accessCredentials {
	stored = stored.normalized()
	access := accessCredentials{token: stored.AccessToken, accountID: stored.AccountID}
	if stored.ExpiresAt > 0 {
		access.expiresAt = time.Unix(stored.ExpiresAt, 0)
	}
	return access
}

func readStoredCredentials(path string) (storedCredentials, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return storedCredentials{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return storedCredentials{}, fmt.Errorf("codexauth: credential path is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return storedCredentials{}, fmt.Errorf("codexauth: read credentials: %w", err)
	}
	var stored storedCredentials
	if err := json.Unmarshal(data, &stored); err != nil {
		return storedCredentials{}, fmt.Errorf("codexauth: decode credentials: %w", errors.Join(ErrReauthenticationRequired, err))
	}
	stored = stored.normalized()
	if !stored.valid() {
		return storedCredentials{}, fmt.Errorf("codexauth: credential file is incomplete or unsupported: %w", ErrReauthenticationRequired)
	}
	return stored, nil
}

func writeStoredCredentials(path string, stored storedCredentials) error {
	stored = stored.normalized()
	if !stored.valid() {
		return fmt.Errorf("codexauth: refusing to persist incomplete credentials")
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return fmt.Errorf("codexauth: encode credentials: %w", err)
	}
	data = append(data, '\n')
	if err := ensureCredentialDirectory(path); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".auth.json.*.tmp")
	if err != nil {
		return fmt.Errorf("codexauth: create credential temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("codexauth: secure credential temporary file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("codexauth: write credentials: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("codexauth: sync credentials: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("codexauth: close credentials: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("codexauth: commit credentials: %w", err)
	}
	committed = true
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("codexauth: secure credential file: %w", err)
	}
	// Directory fsync makes the rename durable on Unix. Windows does not
	// support syncing a directory handle through os.File.Sync.
	if runtime.GOOS != "windows" {
		directory, err := os.Open(dir)
		if err != nil {
			return fmt.Errorf("codexauth: open credential directory for sync: %w", err)
		}
		syncErr := directory.Sync()
		closeErr := directory.Close()
		if syncErr != nil {
			return fmt.Errorf("codexauth: sync credential directory: %w", syncErr)
		}
		if closeErr != nil {
			return fmt.Errorf("codexauth: close credential directory: %w", closeErr)
		}
	}
	return nil
}

func ensureCredentialDirectory(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("codexauth: create credential directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("codexauth: secure credential directory: %w", err)
	}
	return nil
}
