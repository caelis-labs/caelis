package collaboration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"hash/fnv"
	"sync"
)

const retainedMessages = 4096

// SharedMessage is an explicitly sent collaboration message in Session order.
// Private conversations, user input, tool traces and public results are not copied here.
type SharedMessage struct {
	Sequence uint64 `json:"sequence"`
	Message
}

// MessagePage is a bounded shared-log observation. Cursor is a read position,
// not a delivery acknowledgement. TruncatedBefore reports retention loss.
type MessagePage struct {
	Messages        []SharedMessage `json:"messages"`
	Cursor          uint64          `json:"cursor"`
	HasMore         bool            `json:"has_more"`
	TruncatedBefore uint64          `json:"truncated_before,omitempty"`
}

func prepareMessageStore(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS collaboration_messages (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, session TEXT NOT NULL, id TEXT NOT NULL, body TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS collaboration_messages_session ON collaboration_messages(session,seq);
 CREATE TABLE IF NOT EXISTS collaboration_readers (session TEXT NOT NULL, instance TEXT NOT NULL, cursor INTEGER NOT NULL, PRIMARY KEY(session,instance));
 CREATE TABLE IF NOT EXISTS collaboration_seen (session TEXT NOT NULL, instance TEXT NOT NULL, id TEXT NOT NULL, PRIMARY KEY(session,instance,id));
 CREATE TABLE IF NOT EXISTS collaboration_setups (session TEXT NOT NULL, remote TEXT NOT NULL, PRIMARY KEY(session,remote));
 CREATE TABLE IF NOT EXISTS collaboration_retention (session TEXT PRIMARY KEY, trimmed INTEGER NOT NULL);`)
	return err
}

func readerInstance(threads []Thread, member string) (string, error) {
	for _, t := range threads {
		if t.Handle == member {
			return t.Handle + "\x00" + t.ID + "\x00" + t.ParticipantID + "\x00" + t.Generation, nil
		}
	}
	return "", errors.New("participant is no longer attached")
}

func (s *Service) readerLock(i Identity) *sync.Mutex {
	h := fnv.New64a()
	_, _ = h.Write([]byte(i.Session + "\x00" + i.Member))
	return &s.readLocks[h.Sum64()%uint64(len(s.readLocks))]
}

func markMessagesSeen(ctx context.Context, tx *sql.Tx, sessionID, instance string, messages []Message) error {
	for _, m := range messages {
		// Mail predating the shared log has no invented history or suppression row.
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO collaboration_seen(session,instance,id) SELECT ?,?,id FROM collaboration_messages WHERE session=? AND id=?`, sessionID, instance, sessionID, m.ID)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) markDelivered(ctx context.Context, i Identity, instance string, messages []Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = markMessagesSeen(ctx, tx, i.Session, instance, messages); err != nil {
		return err
	}
	return tx.Commit()
}

// ReadMessages returns one unread page for the current immutable member instance.
// Default reads commit their position before returning; a lost response can be
// replayed by supplying an earlier cursor. Explicit replay never alters unread
// progress or delivery state and includes previously seen bodies. New members
// may read all retained explicit messages, including messages sent before joining.
func (s *Service) ReadMessages(ctx context.Context, i Identity, after *uint64, limit int) (MessagePage, error) {
	result := MessagePage{Messages: []SharedMessage{}}
	if limit == 0 {
		limit = 32
	}
	if limit < 1 || limit > 128 {
		return result, errors.New("message page limit must be 1 to 128")
	}
	lock := s.readerLock(i)
	lock.Lock()
	defer lock.Unlock()
	threads, err := s.members(ctx, i)
	if err != nil {
		return result, err
	}
	instance, err := readerInstance(threads, i.Member)
	if err != nil {
		return result, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	var cursor uint64
	err = tx.QueryRowContext(ctx, `SELECT cursor FROM collaboration_readers WHERE session=? AND instance=?`, i.Session, instance).Scan(&cursor)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if after != nil {
		cursor = *after
	}
	var trimmed uint64
	err = tx.QueryRowContext(ctx, `SELECT trimmed FROM collaboration_retention WHERE session=?`, i.Session).Scan(&trimmed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if cursor < trimmed {
		result.TruncatedBefore = trimmed
		cursor = trimmed
	}
	result.Cursor = cursor
	rows, err := tx.QueryContext(ctx, `SELECT m.seq,m.body,EXISTS(SELECT 1 FROM collaboration_seen s WHERE s.session=m.session AND s.instance=? AND s.id=m.id) FROM collaboration_messages m WHERE m.session=? AND m.seq>? ORDER BY m.seq`, instance, i.Session, cursor)
	if err != nil {
		return result, err
	}
	size := 0
	for rows.Next() {
		var seq uint64
		var body string
		var seen bool
		if err = rows.Scan(&seq, &body, &seen); err != nil {
			break
		}
		if seen && after == nil {
			result.Cursor = seq
			continue
		}
		if len(result.Messages) >= limit || size+len(body) > mailboxBatchBytes {
			result.HasMore = true
			break
		}
		var m Message
		if err = json.Unmarshal([]byte(body), &m); err != nil {
			break
		}
		result.Messages = append(result.Messages, SharedMessage{Sequence: seq, Message: m})
		size += len(body) + 64
		result.Cursor = seq
	}
	rowErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		return result, err
	}
	if rowErr != nil {
		return result, rowErr
	}
	if after == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO collaboration_readers(session,instance,cursor) VALUES(?,?,?) ON CONFLICT(session,instance) DO UPDATE SET cursor=excluded.cursor`, i.Session, instance, result.Cursor)
		if err != nil {
			return result, err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM collaboration_seen WHERE session=? AND instance=? AND id IN (SELECT id FROM collaboration_messages WHERE session=? AND seq<=?)`, i.Session, instance, i.Session, result.Cursor)
		if err != nil {
			return result, err
		}
	}
	// Authorization is checked again immediately before commit. Credentials and
	// lifecycle changes never make a database cursor into continuing authority.
	current, err := s.members(ctx, i)
	if err != nil {
		return result, err
	}
	if now, identityErr := readerInstance(current, i.Member); identityErr != nil || now != instance {
		return result, errors.New("reader instance was replaced")
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

func appendSharedMessage(ctx context.Context, tx *sql.Tx, i Identity, instance string, m Message, body string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO collaboration_messages(session,id,body) VALUES(?,?,?)`, i.Session, m.ID, body); err != nil {
		return err
	}
	if err := markMessagesSeen(ctx, tx, i.Session, instance, []Message{m}); err != nil {
		return err
	}
	var cutoff uint64
	err := tx.QueryRowContext(ctx, `SELECT seq FROM collaboration_messages WHERE session=? ORDER BY seq DESC LIMIT 1 OFFSET ?`, i.Session, retainedMessages).Scan(&cutoff)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO collaboration_retention(session,trimmed) VALUES(?,?) ON CONFLICT(session) DO UPDATE SET trimmed=excluded.trimmed`, i.Session, cutoff); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM collaboration_seen WHERE session=? AND id IN (SELECT id FROM collaboration_messages WHERE session=? AND seq<=?)`, i.Session, i.Session, cutoff); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM collaboration_messages WHERE session=? AND seq<=?`, i.Session, cutoff)
	return err
}
