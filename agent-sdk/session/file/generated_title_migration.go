package file

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const (
	legacyGeneratedTitlePageLimit = 32
	legacyGeneratedTitleMaxEvents = 256
)

// migrateLegacyGeneratedTitles repairs the one historical generated-title
// shape that cut valid UTF-8 at the old 80-byte boundary. A durable root marker
// makes the scan a one-time store migration; each repair uses the regular WAL
// transaction so the canonical document and derived index stay synchronized.
func (s *Store) migrateLegacyGeneratedTitles(ctx context.Context) error {
	complete, err := s.generatedTitleMigrationComplete()
	if err != nil || complete {
		return err
	}
	paths, err := s.legacyGeneratedTitleDocumentPaths()
	if err != nil {
		return err
	}
	incomplete := false
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		doc, err := s.readDocumentAt(path)
		if err != nil {
			incomplete = true
			continue
		}
		// The historical byte slice could only introduce replacement runes at
		// the end after JSON normalized its invalid UTF-8.
		if !strings.HasSuffix(doc.Session.Title, "\uFFFD") {
			continue
		}
		title, repair, err := s.repairedLegacyGeneratedTitle(ctx, path, doc.Session.Title)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			incomplete = true
			continue
		}
		if !repair {
			continue
		}
		doc.Session.Title = title
		s.pathCache[pathCacheKey(doc.Session.SessionID, doc.Session.WorkspaceKey)] = path
		if err := s.writeRecoverableDocumentTransaction(ctx, doc, nil); err != nil {
			return err
		}
	}
	if incomplete {
		// A compatibility repair must not make unrelated index reads
		// unavailable. Leave the marker absent so a later process can retry.
		return nil
	}
	return s.markGeneratedTitleMigrationComplete()
}

func (s *Store) legacyGeneratedTitleDocumentPaths() ([]string, error) {
	db, err := s.openSessionIndex()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(`SELECT title, path FROM sessions`)
	if err != nil {
		return nil, fmt.Errorf("agent-sdk/session/file: list legacy generated-title candidates: %w", err)
	}
	defer rows.Close()

	paths := make([]string, 0)
	for rows.Next() {
		var title, path string
		if err := rows.Scan(&title, &path); err != nil {
			return nil, fmt.Errorf("agent-sdk/session/file: scan legacy generated-title candidate: %w", err)
		}
		if utf8.ValidString(title) && !strings.HasSuffix(title, "\uFFFD") {
			continue
		}
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(s.normalizedRootDir(), path)
		}
		paths = append(paths, filepath.Clean(path))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("agent-sdk/session/file: scan legacy generated-title candidates: %w", err)
	}
	return paths, nil
}

func (s *Store) repairedLegacyGeneratedTitle(ctx context.Context, documentPath string, persistedTitle string) (string, bool, error) {
	afterSeq := uint64(0)
	scanned := 0
	for scanned < legacyGeneratedTitleMaxEvents {
		limit := min(legacyGeneratedTitlePageLimit, legacyGeneratedTitleMaxEvents-scanned)
		page, err := s.readEventLogPage(ctx, documentPath, session.EventPageRequest{
			AfterSeq:   afterSeq,
			Limit:      limit,
			Visibility: session.EventPageAllDurable,
		})
		if err != nil {
			return "", false, err
		}
		scanned += len(page.Events)
		for _, event := range page.Events {
			generated := session.GeneratedSessionTitle(event)
			if generated == "" {
				continue
			}
			legacy, corrupted := legacyCorruptedGeneratedTitle(event)
			if !corrupted || persistedTitle != legacy {
				return "", false, nil
			}
			return generated, generated != persistedTitle, nil
		}
		if !page.HasMore {
			return "", false, nil
		}
		if page.NextSeq <= afterSeq {
			return "", false, fmt.Errorf("agent-sdk/session/file: legacy generated-title scan made no progress")
		}
		afterSeq = page.NextSeq
	}
	return "", false, nil
}

func legacyCorruptedGeneratedTitle(event *session.Event) (string, bool) {
	const legacyTitleBytes = 80

	text := strings.TrimSpace(session.EventDisplayText(event))
	if len(text) <= legacyTitleBytes {
		return "", false
	}
	legacy := text[:legacyTitleBytes]
	if utf8.ValidString(legacy) {
		return "", false
	}
	// encoding/json replaced each invalid byte from the historical string
	// with RuneError before the document could be read again.
	return string([]rune(legacy)), true
}

func (s *Store) generatedTitleMigrationMarkerPath() string {
	return filepath.Join(s.normalizedRootDir(), generatedTitleMigrationMarkerFilename)
}

func (s *Store) generatedTitleMigrationComplete() (bool, error) {
	_, err := os.Stat(s.generatedTitleMigrationMarkerPath())
	switch {
	case err == nil:
		return true, nil
	case os.IsNotExist(err):
		return false, nil
	default:
		return false, err
	}
}

func (s *Store) markGeneratedTitleMigrationComplete() error {
	root := s.normalizedRootDir()
	path := s.generatedTitleMigrationMarkerPath()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := file.WriteString("complete\n"); err != nil {
		_ = file.Close()
		return err
	}
	if err := s.durability.SyncFile(file); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return s.durability.SyncDirectory(root)
}
