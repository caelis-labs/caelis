package bot

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	_ "modernc.org/sqlite"
)

// OpenWorkStore opens Bot authority tables in the already secured Host Control
// database. Host assembly must initialize its operation store before this call.
func OpenWorkStore(path string) (*WorkStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA busy_timeout=5000; PRAGMA synchronous=FULL;
 CREATE TABLE IF NOT EXISTS bot_authority (
 kind TEXT NOT NULL, id TEXT NOT NULL, body TEXT NOT NULL, PRIMARY KEY(kind,id));
 CREATE UNIQUE INDEX IF NOT EXISTS bot_pending_reminder_change ON bot_authority(json_extract(body,'$.call.client_id'),json_extract(body,'$.call.arguments.id'))
 WHERE kind='desktop' AND json_extract(body,'$.call.action')='reminders' AND json_extract(body,'$.call.arguments.operation') IN ('save','remove') AND json_extract(body,'$.call.state') IN ('queued','claimed');`); err != nil {
		_ = db.Close()
		return nil, err
	}
	store := &WorkStore{db: &workSQL{db: db}}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		_ = db.Close()
		return nil, err
	}
	identity := struct {
		ID string `json:"id"`
	}{"control-store-" + hex.EncodeToString(nonce[:])}
	if _, err := store.db.Put(context.Background(), "identity", "host", identity); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.db.Get(context.Background(), "identity", "host", &identity); err != nil {
		_ = db.Close()
		return nil, err
	}
	store.StoreID = identity.ID
	return store, nil
}

type workSQL struct{ db *sql.DB }

func (s *workSQL) Put(ctx context.Context, kind, id string, value any) (bool, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO bot_authority(kind,id,body) VALUES(?,?,?) ON CONFLICT(kind,id) DO NOTHING`, kind, id, string(data))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
func (s *workSQL) Get(ctx context.Context, kind, id string, out any) error {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT body FROM bot_authority WHERE kind=? AND id=?`, kind, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return errorcode.New(errorcode.NotFound, "bot: authority record not found")
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(data), out)
}
func (s *workSQL) List(ctx context.Context, kind string) ([]json.RawMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT body FROM bot_authority WHERE kind=? ORDER BY id`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []json.RawMessage
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(data))
	}
	return out, rows.Err()
}
func (s *workSQL) Replace(ctx context.Context, kind, id string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE bot_authority SET body=? WHERE kind=? AND id=?`, string(data), kind, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return errorcode.New(errorcode.NotFound, "bot: authority record not found")
	}
	return err
}
func (s *workSQL) Close() error { return s.db.Close() }

func (s *workSQL) compare(ctx context.Context, kind, id string, old, next any) (bool, error) {
	before, err := json.Marshal(old)
	if err != nil {
		return false, err
	}
	after, err := json.Marshal(next)
	if err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE bot_authority SET body=? WHERE kind=? AND id=? AND body=?`, string(after), kind, id, string(before))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// compareActive makes dispatch admission and lease validation one SQLite write.
// Exit and claim therefore have a definite order even on concurrent transports.
func (s *workSQL) compareActive(ctx context.Context, kind, id string, old, next any, client Client) (bool, error) {
	before, err := json.Marshal(old)
	if err != nil {
		return false, err
	}
	after, err := json.Marshal(next)
	if err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE bot_authority SET body=? WHERE kind=? AND id=? AND body=? AND EXISTS (
 SELECT 1 FROM bot_authority c WHERE c.kind='client' AND c.id=?
 AND json_extract(c.body,'$.client.principal_id')=? AND json_extract(c.body,'$.client.bot_id')=?
 AND json_extract(c.body,'$.client.activation_id')=? AND json_extract(c.body,'$.client.instance_id')=?
 AND json_extract(c.body,'$.client.active')=1 AND julianday(json_extract(c.body,'$.client.expires_at'))>julianday('now'))`, string(after), kind, id, string(before), client.ID, client.PrincipalID, client.BotID, client.ActivationID, client.InstanceID)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}
