package file

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func (s *Store) resolveWritePath(sess session.Session) (string, error) {
	key := sess.SessionID
	if path, ok := s.pathCache[key]; ok && strings.TrimSpace(path) != "" {
		return path, nil
	}
	if entry, err := s.lookupSessionIndex(sess.SessionID); err == nil {
		path, err := s.indexEntryPath(entry)
		if err != nil {
			return "", err
		}
		s.pathCache[key] = path
		return path, nil
	} else if !errors.Is(err, session.ErrSessionNotFound) {
		return "", err
	}
	if workspaceDirName(sess.WorkspaceKey) == ".." {
		return "", fmt.Errorf("%w: workspace directory escapes Session Store", session.ErrInvalidSession)
	}
	path := s.newDocumentPath(sess)
	// Distinct opaque IDs can sanitize to the same rollout name in the same
	// second. Only the index or recovery may adopt an occupied path.
	for _, occupied := range []string{path, eventLogPath(path), transactionPath(path)} {
		if _, err := os.Lstat(occupied); err == nil {
			return "", fmt.Errorf("%w: Session rollout path already exists", session.ErrInvalidSession)
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	return path, nil
}

func (s *Store) resolveDocumentPath(sessionID string, workspaceKey string) (string, error) {
	key := sessionID
	if path, ok := s.pathCache[key]; ok && strings.TrimSpace(path) != "" {
		return path, nil
	}
	entry, err := s.lookupSessionIndex(sessionID)
	if err != nil {
		return "", err
	}
	if key := strings.TrimSpace(workspaceKey); key != "" && entry.Session.WorkspaceKey != key {
		return "", session.ErrSessionNotFound
	}
	path, err := s.indexEntryPath(entry)
	if err != nil {
		return "", err
	}
	s.pathCache[key] = path
	return path, nil
}

func (s *Store) findDocumentPath(sessionID string, workspaceKey string) (string, error) {
	searchRoot := s.rootDir
	requireUnique := true
	if key := strings.TrimSpace(workspaceKey); key != "" {
		if workspaceDirName(key) == ".." {
			return "", session.ErrInvalidSession
		}
		searchRoot = filepath.Join(searchRoot, workspaceDirName(key))
		requireUnique = false
	}
	found := make([]string, 0, 1)
	walkErr := filepath.WalkDir(searchRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !d.Type().IsRegular() || !strings.HasSuffix(d.Name(), "-"+sanitizeSessionID(sessionID)+".json") {
			return nil
		}
		if rolloutPathKey(s.rootDir, path) == sanitizeSessionID(sessionID) {
			found = append(found, path)
			if !requireUnique {
				return fs.SkipAll
			}
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		if os.IsNotExist(walkErr) {
			return "", session.ErrSessionNotFound
		}
		return "", walkErr
	}
	switch len(found) {
	case 0:
		return "", session.ErrSessionNotFound
	case 1:
		return found[0], nil
	default:
		return "", fmt.Errorf(
			"agent-sdk/session/file: session %q matches multiple workspaces; workspace key is required: %w",
			strings.TrimSpace(sessionID),
			session.ErrAmbiguousSession,
		)
	}
}

// rolloutPathKey extracts the filename component reserved by a writer.
// Sanitization is lossy. This key can guard reuse of an unindexed rollout,
// but cannot supply a SessionRef or a listing row.
func rolloutPathKey(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	// The workspace key "." puts the date directory directly under the root.
	if len(parts) == 5 && parts[0] == workspaceDirName(parts[0]) {
		parts = parts[1:]
	}
	if len(parts) != 4 || !filepath.IsLocal(rel) {
		return ""
	}
	const layout = "2006-01-02T15-04-05"
	name, ok := strings.CutPrefix(parts[3], "rollout-")
	if !ok {
		return ""
	}
	name, ok = strings.CutSuffix(name, ".json")
	if !ok || len(name) <= len(layout)+1 || name[len(layout)] != '-' {
		return ""
	}
	at, err := time.Parse(layout, name[:len(layout)])
	key := name[len(layout)+1:]
	if err != nil || at.Format("2006/01/02") != strings.Join(parts[:3], "/") || sanitizeSessionID(key) != key {
		return ""
	}
	return key
}

func (s *Store) newDocumentPath(session session.Session) string {
	at := session.CreatedAt
	if at.IsZero() {
		at = s.now()
	}
	dayDir := filepath.Join(
		s.rootDir,
		workspaceDirName(session.WorkspaceKey),
		at.UTC().Format("2006"),
		at.UTC().Format("01"),
		at.UTC().Format("02"),
	)
	name := fmt.Sprintf(
		"rollout-%s-%s.json",
		at.UTC().Format("2006-01-02T15-04-05"),
		sanitizeSessionID(session.SessionID),
	)
	return filepath.Join(dayDir, name)
}
