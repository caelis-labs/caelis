package file

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/session"
)

func (s *Store) listFromDocuments(req session.ListSessionsRequest) (session.SessionList, error) {
	index, err := s.rebuildSessionIndexFromDocuments()
	if err != nil {
		return session.SessionList{}, err
	}
	return s.listFromSessionIndex(index, req), nil
}

func (s *Store) rebuildSessionIndexFromDocuments() (persistedSessionIndex, error) {
	paths, err := s.listDocumentPaths()
	if err != nil {
		return persistedSessionIndex{}, err
	}

	entries := make([]persistedSessionIndexEntry, 0, len(paths))
	for _, path := range paths {
		doc, err := s.readDocumentAt(path)
		if err != nil {
			return persistedSessionIndex{}, err
		}
		s.pathCache[pathCacheKey(doc.Session.SessionID, doc.Session.WorkspaceKey)] = path
		entries = append(entries, s.sessionIndexEntry(doc.Session, path))
	}
	index := persistedSessionIndex{Sessions: entries}
	if err := s.writeSessionIndex(index); err != nil {
		return persistedSessionIndex{}, err
	}
	index.Kind = indexKind
	index.Version = indexVersion
	index.Sessions = cloneSessionIndexEntries(entries)
	return index, nil
}

func (s *Store) listFromSessionIndex(index persistedSessionIndex, req session.ListSessionsRequest) session.SessionList {
	summaries := make([]session.SessionSummary, 0, len(index.Sessions))
	appName := strings.TrimSpace(req.AppName)
	userID := strings.TrimSpace(req.UserID)
	workspaceKey := strings.TrimSpace(req.WorkspaceKey)
	for _, entry := range index.Sessions {
		summary := session.CloneSessionSummaries([]session.SessionSummary{entry.Session})[0]
		if appName != "" && summary.AppName != appName {
			continue
		}
		if userID != "" && summary.UserID != userID {
			continue
		}
		if workspaceKey != "" && summary.WorkspaceKey != workspaceKey {
			continue
		}
		if path := s.indexEntryPath(entry); path != "" {
			s.pathCache[pathCacheKey(summary.SessionID, summary.WorkspaceKey)] = path
		}
		summaries = append(summaries, summary)
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt)
	})
	if req.Limit > 0 && len(summaries) > req.Limit {
		summaries = summaries[:req.Limit]
	}
	return session.SessionList{Sessions: session.CloneSessionSummaries(summaries)}
}

func (s *Store) sessionIndexPath() string {
	return filepath.Join(s.rootDir, indexFilename)
}

func (s *Store) readSessionIndex() (persistedSessionIndex, error) {
	data, err := os.ReadFile(s.sessionIndexPath())
	if err != nil {
		return persistedSessionIndex{}, err
	}
	var index persistedSessionIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return persistedSessionIndex{}, fmt.Errorf("impl/session/file: decode session index: %w", err)
	}
	if index.Kind != indexKind || index.Version != indexVersion {
		return persistedSessionIndex{}, fmt.Errorf(
			"impl/session/file: unsupported session index %q version %d",
			index.Kind,
			index.Version,
		)
	}
	index.Sessions = cloneSessionIndexEntries(index.Sessions)
	return index, nil
}

func (s *Store) writeSessionIndex(index persistedSessionIndex) error {
	index.Kind = indexKind
	index.Version = indexVersion
	index.Sessions = cloneSessionIndexEntries(index.Sessions)
	sort.Slice(index.Sessions, func(i, j int) bool {
		return index.Sessions[i].Session.UpdatedAt.After(index.Sessions[j].Session.UpdatedAt)
	})
	data, err := json.Marshal(index)
	if err != nil {
		return fmt.Errorf("impl/session/file: encode session index: %w", err)
	}
	data = append(data, '\n')
	path := s.sessionIndexPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func (s *Store) upsertSessionIndex(sess session.Session, documentPath string) error {
	index, err := s.readSessionIndex()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			index = persistedSessionIndex{}
		} else {
			index, err = s.rebuildSessionIndexFromDocuments()
			if err != nil {
				return err
			}
		}
	}
	entry := s.sessionIndexEntry(sess, documentPath)
	key := pathCacheKey(sess.SessionID, sess.WorkspaceKey)
	replaced := false
	for i := range index.Sessions {
		if pathCacheKey(index.Sessions[i].Session.SessionID, index.Sessions[i].Session.WorkspaceKey) == key {
			index.Sessions[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		index.Sessions = append(index.Sessions, entry)
	}
	return s.writeSessionIndex(index)
}

func (s *Store) sessionIndexEntry(sess session.Session, documentPath string) persistedSessionIndexEntry {
	relPath := documentPath
	if rel, err := filepath.Rel(s.rootDir, documentPath); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		relPath = rel
	}
	return persistedSessionIndexEntry{
		Session: session.SessionSummary{
			SessionRef: sess.SessionRef,
			CWD:        sess.CWD,
			Title:      sess.Title,
			Metadata:   session.CloneState(sess.Metadata),
			UpdatedAt:  sess.UpdatedAt,
		},
		Path: relPath,
	}
}

func (s *Store) indexEntryPath(entry persistedSessionIndexEntry) string {
	path := strings.TrimSpace(entry.Path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(s.rootDir, path)
}

func cloneSessionIndexEntries(entries []persistedSessionIndexEntry) []persistedSessionIndexEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]persistedSessionIndexEntry, len(entries))
	for i, entry := range entries {
		out[i] = persistedSessionIndexEntry{
			Session: session.CloneSessionSummaries([]session.SessionSummary{entry.Session})[0],
			Path:    strings.TrimSpace(entry.Path),
		}
	}
	return out
}
