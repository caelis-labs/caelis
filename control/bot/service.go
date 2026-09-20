package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
)

// Service persists configuration alongside canonical conversation facts, using
// the Session owner's atomic event/state transactions. AppServer owns principal
// authorization and the operation ledger; Host composition owns admission and
// model selection. No transcript or model context is stored separately here.
type Service struct {
	Sessions session.Service
}

// Identity returns a stable opaque identity for one principal-bound creation
// operation. Even after a terminal operation receipt expires, retrying creation
// cannot allocate a second Bot or a detached current conversation.
func Identity(principalID, operationID string) string {
	sum := sha256.Sum256([]byte(principalID + "\x00" + operationID))
	return "bot-" + hex.EncodeToString(sum[:16])
}

// ConversationID addresses the single private conversation associated with id.
// Names and filesystem locations never participate in this association.
func ConversationID(id string) (string, error) {
	if !strings.HasPrefix(id, "bot-") || len(id) != 36 {
		return "", errorcode.New(errorcode.InvalidArgument, "bot: invalid identity")
	}
	if _, err := hex.DecodeString(id[4:]); err != nil || strings.ToLower(id) != id {
		return "", errorcode.New(errorcode.InvalidArgument, "bot: invalid identity")
	}
	return "bot-chat-" + id[4:], nil
}

// ListBots returns only complete Bot records owned by ownerID. An empty ownerID
// is reserved for a trusted administrator's listing.
func (s *Service) ListBots(ctx context.Context, ownerID string) ([]Bot, error) {
	var out []Bot
	cursor := ""
	for {
		page, err := s.Sessions.ListSessions(ctx, session.ListSessionsRequest{UserID: ownerID, Cursor: cursor, Limit: 100})
		if err != nil {
			return nil, err
		}
		for _, summary := range page.Sessions {
			id, _ := summary.Metadata[MetadataID].(string)
			if id == "" {
				continue
			}
			loaded, err := s.Sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: session.SessionRef{SessionID: summary.SessionID}, Limit: 1})
			if err != nil {
				return nil, err
			}
			// An abandoned creation skeleton has no admitted conversation and is
			// not a second Bot. The operation ledger keeps its outcome unknown.
			if _, initialized := loaded.State[StateKey]; !initialized {
				continue
			}
			value, err := fromLoaded(loaded, id)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			return out, nil
		}
		cursor = page.NextCursor
	}
}

// GetBot reads configuration and revision from one atomic Session snapshot.
func (s *Service) GetBot(ctx context.Context, id string) (Bot, error) {
	sessionID, err := ConversationID(id)
	if err != nil {
		return Bot{}, err
	}
	loaded, err := s.Sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: session.SessionRef{SessionID: sessionID}, Limit: 1})
	if err != nil {
		return Bot{}, err
	}
	return fromLoaded(loaded, id)
}

func fromLoaded(loaded session.LoadedSession, id string) (Bot, error) {
	boundID, _ := loaded.Session.Metadata[MetadataID].(string)
	if !sessionvisibility.IsBotSession(loaded.Session) || boundID != id {
		return Bot{}, errorcode.New(errorcode.NotFound, "bot: identity not found")
	}
	stored, err := readRecord(loaded.State)
	if err != nil {
		return Bot{}, err
	}
	if stored.ID != id {
		return Bot{}, errors.New("bot: configuration identity does not match its conversation")
	}
	return Bot{ID: id, SessionID: loaded.Session.SessionID, Revision: loaded.Session.Revision, Config: stored.Config}, nil
}

// Save atomically commits configuration and its user-instruction event. The
// caller must serialize prompt admission and reject an already active Turn;
// the persistence fence additionally excludes in-flight Runtime writes. A
// nil previous value initializes a newly created private Session skeleton.
// Every Bot has the same private notebook contract regardless of when it was
// created. Admission appends a user event without compacting history.
func (s *Service) Save(ctx context.Context, active session.Session, id string, config Config, previous *Config, operationID, digest string) (session.Session, error) {
	boundID, _ := active.Metadata[MetadataID].(string)
	if !sessionvisibility.IsBotSession(active) || id != boundID {
		return session.Session{}, errors.New("bot: invalid conversation binding")
	}
	if previous != nil {
		// The accepted record stays the authority: an unsupported or mismatched
		// record fails closed instead of being replaced by an ordinary save.
		state, err := s.Sessions.SnapshotState(ctx, active.SessionRef)
		if err != nil {
			return active, err
		}
		stored, err := readRecord(state)
		if err != nil {
			return active, err
		}
		if stored.ID != id {
			return active, errors.New("bot: configuration identity does not match its conversation")
		}
	}
	var events []*session.Event
	if previous == nil || previous.Name != config.Name || previous.Description != config.Description {
		message := model.NewTextMessage(model.RoleUser, ConfigurationMessage(config))
		events = []*session.Event{{
			ID: "bot-config-" + operationID, IdempotencyKey: "bot-config-" + operationID,
			Type: session.EventTypeUser, Visibility: session.VisibilityCanonical,
			Actor:   session.ActorRef{Kind: session.ActorKindUser, ID: active.UserID},
			Message: &message,
		}}
	}
	store, ok := s.Sessions.(session.EventBatchStateService)
	if !ok {
		return active, errors.New("bot: atomic configuration store unavailable")
	}
	_, err := store.AppendEventsAndUpdateState(ctx, session.AppendEventsAndUpdateStateRequest{
		SessionRef: active.SessionRef, ExpectedRevision: &active.Revision,
		MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeConfiguration),
		TransactionID: "bot-config-" + operationID, MutationDigest: digest, Events: events,
		UpdateState: func(_ []*session.Event, state map[string]any) (map[string]any, error) {
			next := session.CloneState(state)
			if next == nil {
				next = map[string]any{}
			}
			next[StateKey] = record{Version: 1, ID: id, Config: config}
			return next, nil
		},
	})
	if err != nil && !session.IsCommitted(err) {
		return active, err
	}
	// Commit and reply are separate boundaries. Client cancellation after the
	// transaction must not roll back a live model pin for already durable
	// configuration, nor turn a read failure into evidence of a rejected save.
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	updated, readErr := s.Sessions.Session(readCtx, active.SessionRef)
	if readErr != nil {
		active.Revision = 0 // No current revision was observed; callers must refresh.
		return active, &session.CommittedError{Err: errors.Join(err, readErr)}
	}
	return updated, err
}
