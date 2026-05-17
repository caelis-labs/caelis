package file

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/session"
)

func (s *Store) resolveWritePath(sess session.Session) (string, error) {
	key := pathCacheKey(sess.SessionID, sess.WorkspaceKey)
	if path, ok := s.pathCache[key]; ok && strings.TrimSpace(path) != "" {
		return path, nil
	}
	if path, err := s.findDocumentPath(sess.SessionID, sess.WorkspaceKey); err == nil {
		s.pathCache[key] = path
		return path, nil
	} else if !errors.Is(err, session.ErrSessionNotFound) {
		return "", err
	}
	return s.newDocumentPath(sess), nil
}

func (s *Store) resolveDocumentPath(sessionID string, workspaceKey string) (string, error) {
	if strings.TrimSpace(workspaceKey) == "" {
		return s.findDocumentPath(sessionID, workspaceKey)
	}
	key := pathCacheKey(sessionID, workspaceKey)
	if path, ok := s.pathCache[key]; ok && strings.TrimSpace(path) != "" {
		return path, nil
	}
	path, err := s.findDocumentPath(sessionID, workspaceKey)
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
		if d.IsDir() || d.Name() == indexFilename || filepath.Ext(d.Name()) != ".json" {
			return nil
		}
		if strings.HasSuffix(d.Name(), "-"+sanitizeSessionID(sessionID)+".json") {
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
			"impl/session/file: session %q matches multiple workspaces; workspace key is required: %w",
			strings.TrimSpace(sessionID),
			session.ErrAmbiguousSession,
		)
	}
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *Store) listDocumentPaths() ([]string, error) {
	paths := make([]string, 0)
	err := filepath.WalkDir(s.rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || d.Name() == indexFilename || filepath.Ext(d.Name()) != ".json" {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
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
