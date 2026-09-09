// Package collaboration owns Session-scoped Agent discovery and mailboxes.
// Taking or dispatching a message removes it. Delivery is best effort: there
// are no acknowledgements, automatic retries, or exactly-once guarantees.
package collaboration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// MaxResponseBytes is the shared HTTP response limit. Mailbox batches reserve
// space for the WaitThread response envelope.
const MaxResponseBytes = 4 << 20
const mailboxBatchBytes = MaxResponseBytes - (64 << 10)
const deliveryTimeout = 10 * time.Second

// Identity is supplied by the Host, never by model arguments.
type Identity struct {
	Session string
	Member  string
}

// Thread describes a participant in the owning work Session.
type Thread struct {
	ID         string `json:"id"`
	SessionID  string `json:"-"`
	Revision   uint64 `json:"revision"`
	Handle     string `json:"handle"`
	Name       string `json:"name,omitempty"`
	State      string `json:"state"`
	CanDeliver bool   `json:"-"`
}

// Message carries trusted sender identity and ordinary collaboration text.
type Message struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Text    string `json:"message"`
	ReplyTo string `json:"reply_to,omitempty"`
}

// Backend resolves canonical membership and dispatches through the execution
// owner. It must honor context cancellation and must not wait for the
// recipient's work to complete.
type Backend interface {
	List(context.Context, string) ([]Thread, error)
	Deliver(context.Context, string, Message) error
}

// Service owns the persistent pending mailbox and process-bound credentials.
type Service struct {
	db          *sql.DB
	backend     Backend
	mu          sync.Mutex
	credentials map[string]*Grant
	now         func() time.Time
}

// Open uses the Host-owned Control database. The Host closes the service only
// after its Run loop and active callers have drained.
func Open(path string, backend Backend) (*Service, error) {
	if backend == nil {
		return nil, errors.New("collaboration backend is required")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA busy_timeout=5000; CREATE TABLE IF NOT EXISTS collaboration_mailbox (
	 seq INTEGER PRIMARY KEY AUTOINCREMENT, session TEXT NOT NULL, recipient TEXT NOT NULL, body TEXT NOT NULL
	); CREATE INDEX IF NOT EXISTS collaboration_recipient ON collaboration_mailbox(session, recipient, seq);`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Service{db: db, backend: backend, credentials: map[string]*Grant{}, now: time.Now}, nil
}

// Close releases the service's database connection.
func (s *Service) Close() error {
	s.mu.Lock()
	for _, grant := range s.credentials {
		grant.revoked = true
	}
	clear(s.credentials)
	s.mu.Unlock()
	return s.db.Close()
}

func (s *Service) members(ctx context.Context, i Identity) ([]Thread, error) {
	if token, ok := ctx.Value(credentialContextKey{}).(string); ok {
		if _, _, err := s.Authenticate(ctx, token); err != nil {
			return nil, err
		}
	}
	threads, err := s.backend.List(ctx, i.Session)
	if err != nil {
		return nil, err
	}
	for _, t := range threads {
		if t.Handle == i.Member {
			return threads, nil
		}
	}
	return nil, errors.New("participant is no longer attached")
}

// List returns only the caller's Session participants.
func (s *Service) List(ctx context.Context, i Identity) ([]Thread, error) { return s.members(ctx, i) }

// Send appends once. Repeating a call intentionally sends another message.
func (s *Service) Send(ctx context.Context, i Identity, to, text, replyTo string) (Message, error) {
	threads, err := s.members(ctx, i)
	if err != nil {
		return Message{}, err
	}
	if len(text) == 0 || len(text) > 65536 {
		return Message{}, errors.New("message must contain 1 to 65536 bytes")
	}
	if replyTo != "" {
		if parsed, err := uuid.Parse(replyTo); err != nil || parsed.String() != replyTo {
			return Message{}, errors.New("reply_to must be a canonical message UUID")
		}
	}
	found := false
	for _, t := range threads {
		if t.Handle == to {
			found = true
		}
	}
	if !found || to == i.Member {
		return Message{}, errors.New("recipient must be another Session participant")
	}
	m := Message{ID: uuid.NewString(), From: i.Member, To: to, Text: text, ReplyTo: replyTo}
	body, err := json.Marshal(m)
	if err != nil {
		return Message{}, err
	}
	if len(body)+2 > mailboxBatchBytes {
		return Message{}, errors.New("encoded message exceeds mailbox response budget")
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO collaboration_mailbox(session,recipient,body) VALUES(?,?,?)`, i.Session, to, string(body))
	return m, err
}

// Receive atomically removes a bounded batch from the caller's mailbox.
// A lost response loses those messages; the service deliberately never retries.
func (s *Service) Receive(ctx context.Context, i Identity) ([]Message, error) {
	if _, err := s.members(ctx, i); err != nil {
		return nil, err
	}
	return s.take(ctx, i, 32)
}

func (s *Service) take(ctx context.Context, i Identity, limit int) ([]Message, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT seq,body FROM collaboration_mailbox WHERE session=? AND recipient=? ORDER BY seq LIMIT ?`, i.Session, i.Member, limit)
	if err != nil {
		return nil, err
	}
	out := []Message{}
	var last int64
	encodedBytes := 2
	for rows.Next() {
		var body string
		var seq int64
		if err = rows.Scan(&seq, &body); err != nil {
			break
		}
		var m Message
		if err = json.Unmarshal([]byte(body), &m); err != nil {
			break
		}
		encoded, marshalErr := json.Marshal(m)
		if marshalErr != nil {
			err = marshalErr
			break
		}
		nextBytes := encodedBytes + len(encoded)
		if len(out) > 0 {
			nextBytes++
		}
		if nextBytes > mailboxBatchBytes {
			if len(out) == 0 {
				err = errors.New("stored message exceeds mailbox response budget")
			}
			break
		}
		encodedBytes = nextBytes
		last = seq
		out = append(out, m)
	}
	rowErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if rowErr != nil {
		return nil, rowErr
	}
	if len(out) > 0 {
		if _, err = tx.ExecContext(ctx, `DELETE FROM collaboration_mailbox WHERE session=? AND recipient=? AND seq<=?`, i.Session, i.Member, last); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// Run attempts delivery at safe points identified by the backend. Every
// attempted message is removed before dispatch, including failed dispatches.
// At most one bounded attempt runs per recipient; unrelated recipients proceed
// independently. Run joins these attempts before returning.
func (s *Service) Run(ctx context.Context, report func(error)) {
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	type completion struct {
		target Identity
		err    error
	}
	finished := make(chan completion)
	active := map[Identity]bool{}
	var workers sync.WaitGroup
	defer workers.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case result := <-finished:
			delete(active, result.target)
			if result.err != nil && report != nil {
				report(result.err)
			}
		case <-tick.C:
			targets, err := s.pendingRecipients(ctx)
			if err != nil {
				if report != nil {
					report(err)
				}
				continue
			}
			for _, target := range targets {
				if active[target] {
					continue
				}
				active[target] = true
				workers.Add(1)
				go func() {
					defer workers.Done()
					attempt, cancel := context.WithTimeout(ctx, deliveryTimeout)
					defer cancel()
					err := s.deliverRecipient(attempt, target)
					select {
					case finished <- completion{target, err}:
					case <-ctx.Done():
					}
				}()
			}
		}
	}
}

func (s *Service) pendingRecipients(ctx context.Context) ([]Identity, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT session,recipient FROM collaboration_mailbox`)
	if err != nil {
		return nil, err
	}
	var targets []Identity
	for rows.Next() {
		var i Identity
		if err = rows.Scan(&i.Session, &i.Member); err != nil {
			break
		}
		targets = append(targets, i)
	}
	rowErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if rowErr != nil {
		return nil, rowErr
	}
	return targets, nil
}

func (s *Service) deliverRecipient(ctx context.Context, i Identity) error {
	threads, err := s.backend.List(ctx, i.Session)
	if err != nil {
		return err
	}
	for _, t := range threads {
		if t.Handle != i.Member || !t.CanDeliver {
			continue
		}
		messages, err := s.take(ctx, i, 1)
		if err != nil {
			return err
		}
		for _, m := range messages {
			// The message is already consumed. Timeout does not prove non-execution
			// and never causes a retry or a second mailbox insertion.
			if err := s.backend.Deliver(ctx, i.Session, m); err != nil {
				return fmt.Errorf("deliver message %s (removed; remote outcome may be unknown): %w", m.ID, err)
			}
		}
		break
	}
	return nil
}
