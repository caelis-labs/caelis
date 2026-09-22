package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// Execution is a native execution address. ID identifies a durable work handle;
// none of these per-execution fields may be substituted for that handle.
type Execution struct {
	InstanceID string `json:"instance_id"`
	SessionID  string `json:"session_id"`
	HandleID   string `json:"handle_id"`
	RunID      string `json:"run_id"`
	TurnID     string `json:"turn_id"`
}

// RequestSource is a persisted, authenticated user request. Assignment text,
// settings, notes and worker results never produce records of this type.
type RequestSource struct {
	ContentParts      []model.ContentPart `json:"content_parts,omitempty"`
	Kind              string              `json:"kind"`
	OriginalRequestID string              `json:"original_request_id,omitempty"`
	ClientID          string              `json:"client_id,omitempty"`
	ID                string              `json:"id"`
	PrincipalID       string              `json:"principal_id"`
	BotID             string              `json:"bot_id"`
	OperationID       string              `json:"operation_id"`
	Digest            string              `json:"digest"`
	Text              string              `json:"text"`
	Execution         Execution           `json:"execution"`
}

// Work is a Bot-owned durable work Session. Execution is the latest admitted
// execution, which can change when the same work is continued.
type Work struct {
	ClientID            string    `json:"client_id,omitempty"`
	ID                  string    `json:"id"`
	PrincipalID         string    `json:"principal_id"`
	BotID               string    `json:"bot_id"`
	SessionID           string    `json:"session_id"`
	WorkspaceKey        string    `json:"workspace_key"`
	SourceID            string    `json:"source_id"`
	Assignment          string    `json:"assignment"`
	CreationOperationID string    `json:"creation_operation_id"`
	CreationDigest      string    `json:"creation_digest"`
	Config              Config    `json:"config"`
	Execution           Execution `json:"execution"`
	Status              string    `json:"status"`
	Result              string    `json:"result,omitempty"`
}

// WorkID permanently associates a creation operation with one owned work.
func WorkID(botID, operationID string) string { return opaqueID("bot-work-", botID, operationID) }

func opaqueID(prefix string, parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return prefix + hex.EncodeToString(h.Sum(nil)[:16])
}

func validWorkID(id string) bool {
	if !strings.HasPrefix(id, "bot-work-") || len(id) != 41 {
		return false
	}
	_, err := hex.DecodeString(id[9:])
	return err == nil && strings.ToLower(id) == id
}

// WorkStore is the Control-owned authority directory. Its database is outside
// every model-visible file area. The shared AppServer operation ledger remains
// the only dispatch/idempotency owner; work creation anchors survive retention.
type WorkStore struct {
	db      *workSQL
	desktop desktopSignal
	// Sessions and InstanceID are installed by Host assembly before sharing.
	Sessions   session.Service
	InstanceID string
	StoreID    string
}

// RecordRequest must only be called by authenticated prompt admission. The
// operation digest must cover the complete accepted request, not only its text.
func (s *WorkStore) RecordRequest(ctx context.Context, principalID, botID, clientID, operationID, digest, text string, parts ...model.ContentPart) (RequestSource, error) {
	if principalID == "" || operationID == "" || digest == "" {
		return RequestSource{}, errorcode.New(errorcode.InvalidArgument, "bot: incomplete user request source")
	}
	if _, err := ConversationID(botID); err != nil {
		return RequestSource{}, err
	}
	source := RequestSource{ContentParts: append([]model.ContentPart(nil), parts...), Kind: "user_request", ID: opaqueID("bot-request-", principalID, botID, operationID), PrincipalID: principalID, BotID: botID, ClientID: clientID, OperationID: operationID, Digest: digest, Text: text}
	added, err := s.db.Put(ctx, "source", source.ID, source)
	if err != nil {
		return RequestSource{}, err
	}
	if !added {
		var previous RequestSource
		if err := s.db.Get(ctx, "source", source.ID, &previous); err != nil {
			return RequestSource{}, err
		}
		if previous.PrincipalID != principalID || previous.BotID != botID || previous.ClientID != clientID || previous.Digest != digest {
			return RequestSource{}, errorcode.New(errorcode.Conflict, "bot: request source conflicts")
		}
		return previous, nil
	}
	return source, nil
}

// BindRequest records the exact execution that admitted the request. Unknown
// admission is left unbound, so it can never authorize a later delegation.
func (s *WorkStore) BindRequest(ctx context.Context, source RequestSource, execution Execution) error {
	if execution.HandleID == "" || execution.RunID == "" || execution.TurnID == "" {
		return errors.New("bot: incomplete source execution")
	}
	previous, err := s.Request(ctx, source.PrincipalID, source.BotID, source.ID)
	if err != nil {
		return err
	}
	if previous.Execution.HandleID != "" && previous.Execution != execution {
		return errorcode.New(errorcode.Conflict, "bot: source execution already bound")
	}
	next := previous
	next.Execution = execution
	changed, err := s.db.compare(ctx, "source", source.ID, previous, next)
	if err == nil && !changed {
		return errorcode.New(errorcode.Conflict, "bot: source binding changed concurrently")
	}
	return err
}

// Request reads an exact owner/Bot-scoped source, without accepting caller text.
func (s *WorkStore) Request(ctx context.Context, principalID, botID, id string) (RequestSource, error) {
	var source RequestSource
	if err := s.db.Get(ctx, "source", id, &source); err != nil {
		return source, err
	}
	if source.PrincipalID != principalID || source.BotID != botID {
		return RequestSource{}, errorcode.New(errorcode.PermissionDenied, "bot: request source is not owned by this Bot")
	}
	return source, nil
}

// RequestForOperation resolves a trusted current-turn operation binding.
func (s *WorkStore) RequestForOperation(ctx context.Context, principalID, botID, operationID string) (RequestSource, error) {
	return s.Request(ctx, principalID, botID, opaqueID("bot-request-", principalID, botID, operationID))
}

// ReserveWork anchors creation before allocating a Session or starting work.
// An existing reservation must be recovered by reading, never dispatched again.
func (s *WorkStore) ReserveWork(ctx context.Context, work Work) (bool, error) {
	if !validWorkID(work.ID) || work.ID != WorkID(work.BotID, work.CreationOperationID) || work.CreationDigest == "" {
		return false, errorcode.New(errorcode.InvalidArgument, "bot: invalid work reservation")
	}
	source, err := s.Request(ctx, work.PrincipalID, work.BotID, work.SourceID)
	if err != nil {
		return false, err
	}
	if source.Execution.TurnID == "" {
		return false, errorcode.New(errorcode.FailedPrecondition, "bot: request admission is not confirmed")
	}
	added, err := s.db.Put(ctx, "work", work.ID, work)
	if err != nil || added {
		return added, err
	}
	old, err := s.GetWork(ctx, work.PrincipalID, work.BotID, work.ID)
	if err != nil {
		return false, err
	}
	if old.CreationDigest != work.CreationDigest {
		return false, errorcode.New(errorcode.Conflict, "bot: work creation conflicts")
	}
	return false, nil
}

// GetWork requires both principal and Bot ownership, even for known Session IDs.
func (s *WorkStore) GetWork(ctx context.Context, principalID, botID, id string) (Work, error) {
	var work Work
	if err := s.db.Get(ctx, "work", id, &work); err != nil {
		return work, err
	}
	if work.PrincipalID != principalID || work.BotID != botID {
		return Work{}, errorcode.New(errorcode.PermissionDenied, "bot: work is not owned by this Bot")
	}
	return s.observeWork(ctx, work)
}

// ListWork returns the caller's Bot-owned work handles.
func (s *WorkStore) ListWork(ctx context.Context, principalID, botID string) ([]Work, error) {
	rows, err := s.db.List(ctx, "work")
	if err != nil {
		return nil, err
	}
	out := []Work{}
	for _, row := range rows {
		var work Work
		if err := json.Unmarshal(row, &work); err != nil {
			return nil, err
		}
		if work.PrincipalID == principalID && work.BotID == botID {
			observed, err := s.observeWork(ctx, work)
			if err != nil {
				return nil, err
			}
			out = append(out, observed)
		}
	}
	return out, nil
}

// SaveWork records native execution observations. It never starts execution.
func (s *WorkStore) SaveWork(ctx context.Context, work Work) error {
	var previous Work
	err := s.db.Get(ctx, "work", work.ID, &previous)
	if err != nil {
		return err
	}
	if previous.PrincipalID != work.PrincipalID || previous.BotID != work.BotID || previous.SessionID != work.SessionID || previous.CreationDigest != work.CreationDigest || previous.SourceID != work.SourceID {
		return errorcode.New(errorcode.Conflict, "bot: immutable work binding changed")
	}
	work.Result = ""
	changed, err := s.db.compare(ctx, "work", work.ID, previous, work)
	if err == nil && !changed {
		return errorcode.New(errorcode.Conflict, "bot: work changed concurrently")
	}
	return err
}

// Close releases the store after all Host producers have drained.
func (s *WorkStore) Close() error { return s.db.Close() }
