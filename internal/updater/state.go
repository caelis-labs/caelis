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

type updateLockRecord struct {
	PID      int       `json:"pid"`
	LockedAt time.Time `json:"locked_at"`
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
		release, locked, err := m.tryCreateUpdateLock(path)
		if err != nil || locked {
			return release, locked, err
		}
		reclaimed, err := m.removeStaleLock(path)
		if err != nil {
			return nil, false, err
		}
		if !reclaimed {
			return func() {}, false, nil
		}
	}
	return func() {}, false, nil
}

func (m *Manager) tryCreateUpdateLock(path string) (func(), bool, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	data, encodeErr := m.encodeUpdateLock(m.currentPID())
	if encodeErr != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, false, encodeErr
	}
	_, writeErr := file.Write(data)
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

func (m *Manager) transferUpdateLockToParent() error {
	path := m.lockPath()
	if path == "" {
		return nil
	}
	parent := m.parentPID()
	current := m.currentPID()
	if parent <= 0 || parent == current {
		return nil
	}
	return m.replaceUpdateLock(path, parent)
}

func (m *Manager) replaceUpdateLock(path string, pid int) error {
	data, err := m.encodeUpdateLock(pid)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func (m *Manager) encodeUpdateLock(pid int) ([]byte, error) {
	if pid <= 0 {
		return nil, fmt.Errorf("update lock pid %d is invalid", pid)
	}
	data, err := json.Marshal(updateLockRecord{PID: pid, LockedAt: m.now()})
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func (m *Manager) removeStaleLock(path string) (bool, error) {
	if !m.isUpdateLockStale(path) {
		return false, nil
	}
	err := os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("remove stale update lock: %w", err)
	}
	return true, nil
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
	record, ok := parseUpdateLock(data)
	if !ok {
		return true
	}
	// Legacy timestamp locks cannot prove a live owner. Treat them as abandoned
	// so an interrupted Windows update can be retried after upgrade or reboot.
	if record.PID <= 0 {
		return true
	}
	if !m.ownerProcessExists(record.PID) {
		return true
	}
	return !record.LockedAt.IsZero() && m.now().Sub(record.LockedAt) >= updateLockMaxAge
}

func parseUpdateLock(data []byte) (updateLockRecord, bool) {
	text := strings.TrimSpace(string(data))
	if text == "" {
		return updateLockRecord{}, false
	}
	var record updateLockRecord
	if err := json.Unmarshal([]byte(text), &record); err == nil && record.PID > 0 {
		return record, true
	}
	lockedAt, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		lockedAt, err = time.Parse(time.RFC3339, text)
	}
	if err != nil {
		return updateLockRecord{}, false
	}
	return updateLockRecord{LockedAt: lockedAt}, true
}

func (m *Manager) currentPID() int {
	if m.cfg.PID == nil {
		return os.Getpid()
	}
	return m.cfg.PID()
}

func (m *Manager) parentPID() int {
	if m.cfg.ParentPID == nil {
		return os.Getppid()
	}
	return m.cfg.ParentPID()
}

func (m *Manager) ownerProcessExists(pid int) bool {
	if m.cfg.ProcessExists == nil {
		return processExists(pid)
	}
	return m.cfg.ProcessExists(pid)
}

func (m *Manager) lockPath() string {
	root := strings.TrimSpace(m.cfg.StoreDir)
	if root == "" {
		return ""
	}
	return filepath.Join(root, "updates", "update.lock")
}
