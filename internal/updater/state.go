package updater

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type stateFile struct {
	CurrentVersion string    `json:"current_version,omitempty"`
	LatestVersion  string    `json:"latest_version,omitempty"`
	InstallMethod  string    `json:"install_method,omitempty"`
	Available      bool      `json:"available,omitempty"`
	LastCheckedAt  time.Time `json:"last_checked_at,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
}

func (m *Manager) cachedResult(current string, method string) (Result, bool) {
	state, ok := m.loadState()
	if !ok {
		return Result{}, false
	}
	if strings.TrimSpace(state.CurrentVersion) != current || strings.TrimSpace(state.InstallMethod) != method {
		return Result{}, false
	}
	if state.LastCheckedAt.IsZero() || m.now().Sub(state.LastCheckedAt) >= dailyCheckInterval {
		return Result{}, false
	}
	return Result{
		CurrentVersion: current,
		LatestVersion:  strings.TrimSpace(state.LatestVersion),
		InstallMethod:  method,
		Available:      state.Available,
		LastCheckedAt:  state.LastCheckedAt,
	}, true
}

func (m *Manager) loadState() (stateFile, bool) {
	path := m.statePath()
	if path == "" {
		return stateFile{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return stateFile{}, false
	}
	var state stateFile
	if err := json.Unmarshal(data, &state); err != nil {
		return stateFile{}, false
	}
	return state, true
}

func (m *Manager) saveState(state stateFile) error {
	path := m.statePath()
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func (m *Manager) statePath() string {
	root := strings.TrimSpace(m.cfg.StoreDir)
	if root == "" {
		return ""
	}
	return filepath.Join(root, "updates", "state.json")
}

func (m *Manager) acquireUpdateLock() (func(), bool, error) {
	path := m.lockPath()
	if path == "" {
		return func() {}, true, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, writeErr := fmt.Fprintln(file, m.now().Format(time.RFC3339Nano))
			closeErr := file.Close()
			if writeErr != nil {
				_ = os.Remove(path)
				return nil, false, writeErr
			}
			if closeErr != nil {
				_ = os.Remove(path)
				return nil, false, closeErr
			}
			return func() { _ = os.Remove(path) }, true, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, false, err
		}
		if !m.removeStaleLock(path) {
			return func() {}, false, nil
		}
	}
	return func() {}, false, nil
}

func (m *Manager) removeStaleLock(path string) bool {
	if !m.isUpdateLockStale(path) {
		return false
	}
	return os.Remove(path) == nil
}

// IsUpdateLockHeld reports whether another update is currently in progress.
func (m *Manager) IsUpdateLockHeld() bool {
	path := m.lockPath()
	if path == "" {
		return false
	}
	if _, err := os.Stat(path); err != nil {
		return false
	}
	return !m.isUpdateLockStale(path)
}

func (m *Manager) isUpdateLockStale(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	lockedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data)))
	if err != nil {
		return true
	}
	return m.now().Sub(lockedAt) >= updateLockMaxAge
}

func (m *Manager) lockPath() string {
	root := strings.TrimSpace(m.cfg.StoreDir)
	if root == "" {
		return ""
	}
	return filepath.Join(root, "updates", "update.lock")
}
