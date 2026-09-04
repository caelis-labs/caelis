package file

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const (
	workspaceKeyRepairKind    = "caelis.sdk.session.workspace-key-repair"
	workspaceKeyRepairVersion = 1
)

// WorkspaceKeyRepair describes one doctor-owned, idempotent compatibility
// rewrite. ExpectedWorkspaceKey prevents an unrelated identity from being
// overwritten when the store changed after inspection.
type WorkspaceKeyRepair struct {
	SessionID               string `json:"session_id"`
	ExpectedWorkspaceKey    string `json:"expected_workspace_key"`
	ReplacementWorkspaceKey string `json:"replacement_workspace_key"`
}

// WorkspaceKeyRepairReport summarizes durable changes made by one repair run.
type WorkspaceKeyRepairReport struct {
	RepairedSessions int
	RepairedTasks    int
}

type persistedWorkspaceKeyRepair struct {
	Kind    string               `json:"kind"`
	Version int                  `json:"version"`
	Repairs []WorkspaceKeyRepair `json:"repairs"`
}

// PendingWorkspaceKeyRepairs returns a durable repair plan left by an
// interrupted doctor run. Callers use this only to decide whether offline
// repair must resume; normal Session reads never consult the marker.
func (s *Store) PendingWorkspaceKeyRepairs(ctx context.Context) ([]WorkspaceKeyRepair, error) {
	if s == nil {
		return nil, errors.New("agent-sdk/session/file: store is not initialized")
	}
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	var repairs []WorkspaceKeyRepair
	err := s.withRootReadLockContext(ctx, func() error {
		loaded, err := s.readWorkspaceKeyRepairMarker()
		if err != nil {
			return err
		}
		repairs = loaded
		return nil
	})
	return repairs, err
}

// RepairWorkspaceKeys durably rewrites Session, fence, and Task workspace
// references. A persisted plan makes the operation resumable after a process
// exit. The method is intentionally an explicit administrative API and is not
// part of ordinary Session startup or reads.
func (s *Store) RepairWorkspaceKeys(
	ctx context.Context,
	repairs []WorkspaceKeyRepair,
) (WorkspaceKeyRepairReport, error) {
	if s == nil {
		return WorkspaceKeyRepairReport{}, errors.New("agent-sdk/session/file: store is not initialized")
	}
	if err := s.mu.LockContext(ctx); err != nil {
		return WorkspaceKeyRepairReport{}, err
	}
	defer s.mu.Unlock()

	var report WorkspaceKeyRepairReport
	err := s.withRootWriteLockContext(ctx, func() error {
		pending, err := s.readWorkspaceKeyRepairMarker()
		if err != nil {
			return err
		}
		if len(pending) > 0 {
			repairs = pending
		} else {
			repairs, err = normalizeWorkspaceKeyRepairs(repairs)
			if err != nil {
				return err
			}
			if len(repairs) == 0 {
				return nil
			}
			if err := s.validateWorkspaceKeyRepairs(repairs); err != nil {
				return err
			}
			if err := s.writeWorkspaceKeyRepairMarker(ctx, repairs); err != nil {
				return err
			}
		}

		if err := s.validateWorkspaceKeyRepairs(repairs); err != nil {
			return err
		}
		for _, repair := range repairs {
			changed, err := s.repairSessionWorkspaceKey(ctx, repair)
			if err != nil {
				return err
			}
			if changed {
				report.RepairedSessions++
			}
			count, err := s.repairTaskWorkspaceKeys(repair)
			if err != nil {
				return err
			}
			report.RepairedTasks += count
		}
		return s.removeWorkspaceKeyRepairMarker(ctx)
	})
	return report, err
}

func normalizeWorkspaceKeyRepairs(repairs []WorkspaceKeyRepair) ([]WorkspaceKeyRepair, error) {
	normalized := make([]WorkspaceKeyRepair, 0, len(repairs))
	seen := map[string]bool{}
	for _, repair := range repairs {
		repair.SessionID = strings.TrimSpace(repair.SessionID)
		repair.ExpectedWorkspaceKey = strings.TrimSpace(repair.ExpectedWorkspaceKey)
		repair.ReplacementWorkspaceKey = strings.TrimSpace(repair.ReplacementWorkspaceKey)
		if repair.SessionID == "" || repair.ExpectedWorkspaceKey == "" || repair.ReplacementWorkspaceKey == "" {
			return nil, errors.New("agent-sdk/session/file: workspace key repair requires Session ID and both workspace keys")
		}
		if repair.ExpectedWorkspaceKey == repair.ReplacementWorkspaceKey {
			continue
		}
		if seen[repair.SessionID] {
			return nil, fmt.Errorf("agent-sdk/session/file: duplicate workspace key repair for Session %q", repair.SessionID)
		}
		seen[repair.SessionID] = true
		normalized = append(normalized, repair)
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].SessionID < normalized[j].SessionID })
	return normalized, nil
}

func (s *Store) validateWorkspaceKeyRepairs(repairs []WorkspaceKeyRepair) error {
	for _, repair := range repairs {
		doc, err := s.readDocument(repair.SessionID)
		if err != nil {
			return fmt.Errorf("agent-sdk/session/file: inspect workspace key repair for Session %q: %w", repair.SessionID, err)
		}
		current := strings.TrimSpace(doc.Session.WorkspaceKey)
		if current != repair.ExpectedWorkspaceKey && current != repair.ReplacementWorkspaceKey {
			return fmt.Errorf(
				"agent-sdk/session/file: Session %q workspace key changed from expected value",
				repair.SessionID,
			)
		}
		keys, err := s.taskWorkspaceKeys(repair.SessionID)
		if err != nil {
			return err
		}
		for _, key := range keys {
			if key != repair.ExpectedWorkspaceKey && key != repair.ReplacementWorkspaceKey {
				return fmt.Errorf("agent-sdk/session/file: Session %q has a Task with an unexpected workspace key", repair.SessionID)
			}
		}
	}
	return nil
}

func (s *Store) repairSessionWorkspaceKey(ctx context.Context, repair WorkspaceKeyRepair) (bool, error) {
	doc, err := s.readDocument(repair.SessionID)
	if err != nil {
		return false, err
	}
	if doc.Session.WorkspaceKey == repair.ReplacementWorkspaceKey {
		return false, nil
	}
	doc.Session.WorkspaceKey = repair.ReplacementWorkspaceKey
	if doc.Fence != nil {
		doc.Fence.SessionRef.WorkspaceKey = repair.ReplacementWorkspaceKey
	}
	if err := s.writeRecoverableDocumentTransaction(ctx, doc, nil, nil); err != nil {
		return false, fmt.Errorf("agent-sdk/session/file: repair Session %q workspace key: %w", repair.SessionID, err)
	}
	delete(s.pathCache, pathCacheKey(repair.SessionID, repair.ExpectedWorkspaceKey))
	return true, nil
}

func (s *Store) taskWorkspaceKeys(sessionID string) ([]string, error) {
	db, err := s.openSessionIndex()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT DISTINCT workspace_key FROM tasks WHERE session_id = ?`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("agent-sdk/session/file: inspect Task workspace keys: %w", err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("agent-sdk/session/file: scan Task workspace key: %w", err)
		}
		keys = append(keys, strings.TrimSpace(key))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("agent-sdk/session/file: scan Task workspace keys: %w", err)
	}
	return keys, nil
}

func (s *Store) repairTaskWorkspaceKeys(repair WorkspaceKeyRepair) (int, error) {
	db, err := s.openSessionIndex()
	if err != nil {
		return 0, err
	}
	defer db.Close()
	result, err := db.Exec(
		`UPDATE tasks SET workspace_key = ? WHERE session_id = ? AND workspace_key = ?`,
		repair.ReplacementWorkspaceKey,
		repair.SessionID,
		repair.ExpectedWorkspaceKey,
	)
	if err != nil {
		return 0, fmt.Errorf("agent-sdk/session/file: repair Task workspace keys: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("agent-sdk/session/file: count repaired Task workspace keys: %w", err)
	}
	if count > 0 {
		if err := s.durability.SyncDirectory(filepath.Dir(s.sessionIndexPath())); err != nil {
			return int(count), &session.CommittedError{Err: err}
		}
	}
	return int(count), nil
}

func (s *Store) workspaceKeyRepairMarkerPath() string {
	return filepath.Join(s.normalizedRootDir(), workspaceKeyRepairMarkerFilename)
}

func (s *Store) readWorkspaceKeyRepairMarker() ([]WorkspaceKeyRepair, error) {
	raw, err := os.ReadFile(s.workspaceKeyRepairMarkerPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var marker persistedWorkspaceKeyRepair
	if err := json.Unmarshal(raw, &marker); err != nil {
		return nil, fmt.Errorf("agent-sdk/session/file: decode workspace key repair marker: %w", err)
	}
	if marker.Kind != workspaceKeyRepairKind || marker.Version != workspaceKeyRepairVersion {
		return nil, fmt.Errorf("agent-sdk/session/file: unsupported workspace key repair marker %q version %d", marker.Kind, marker.Version)
	}
	return normalizeWorkspaceKeyRepairs(marker.Repairs)
}

func (s *Store) writeWorkspaceKeyRepairMarker(ctx context.Context, repairs []WorkspaceKeyRepair) error {
	marker := persistedWorkspaceKeyRepair{Kind: workspaceKeyRepairKind, Version: workspaceKeyRepairVersion, Repairs: repairs}
	raw, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	path := s.workspaceKeyRepairMarkerPath()
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := s.durability.SyncFile(tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replaceFile(ctx, s.diagnostics, fileOperationReplaceRepairMarker, tmpPath, path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	return s.durability.SyncDirectory(filepath.Dir(path))
}

func (s *Store) removeWorkspaceKeyRepairMarker(ctx context.Context) error {
	path := s.workspaceKeyRepairMarkerPath()
	if err := removeFile(ctx, s.diagnostics, fileOperationRemoveRepairMarker, path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return s.durability.SyncDirectory(filepath.Dir(path))
}
