package gatewayapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"strings"
	"sync"

	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

const (
	guardianStateParentSessionID      = "gateway.guardian.parent_session_id"
	guardianStateReuseKey             = "gateway.guardian.reuse_key"
	guardianStateCursorEventCount     = "gateway.guardian.cursor.event_count"
	guardianStateCursorLastEventID    = "gateway.guardian.cursor.last_event_id"
	guardianStateLastPromptEventID    = "gateway.guardian.last_prompt_event_id"
	guardianStateLastAssistantEventID = "gateway.guardian.last_assistant_event_id"
)

type guardianReviewSession struct {
	mu       sync.Mutex
	session  session.Session
	reuseKey string
	events   []*session.Event
	cursor   guardianTranscriptCursor
	version  uint64
}

type guardianTranscriptCursor struct {
	EventCount  int
	LastEventID string
}

func (r *guardianApprovalReviewer) reviewSessionFor(ctx context.Context, req gateway.ApprovalReviewRequest, activeSession session.Session) (*guardianReviewSession, error) {
	key := strings.TrimSpace(req.SessionRef.SessionID)
	if key == "" {
		key = strings.TrimSpace(activeSession.SessionID)
	}
	if key == "" {
		key = "default"
	}
	reuseKey := guardianReuseKey(req.Model, guardianPolicyPrompt())

	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.sessionsByParent[key]
	if item == nil || item.reuseKey != reuseKey {
		loaded, err := r.loadGuardianReviewSession(ctx, activeSession, reuseKey)
		if err != nil {
			return nil, err
		}
		item = loaded
		r.sessionsByParent[key] = item
	}
	return item, nil
}

func (r *guardianApprovalReviewer) loadGuardianReviewSession(ctx context.Context, activeSession session.Session, reuseKey string) (*guardianReviewSession, error) {
	guardianSession, err := r.startGuardianReviewSession(ctx, activeSession, reuseKey)
	if err != nil {
		return nil, err
	}
	state, err := r.sessions.SnapshotState(ctx, guardianSession.SessionRef)
	if err != nil {
		return nil, err
	}
	storedReuseKey := guardianStateString(state, guardianStateReuseKey)
	var events []*session.Event
	cursor := guardianTranscriptCursor{}
	if storedReuseKey == "" || storedReuseKey == reuseKey {
		events, err = r.sessions.Events(ctx, session.EventsRequest{SessionRef: guardianSession.SessionRef})
		if err != nil {
			return nil, err
		}
		cursor = guardianCursorFromState(state)
		if cursor.EventCount == 0 {
			cursor = guardianCursorFromEvents(events)
		}
		if storedReuseKey == "" {
			storedReuseKey = guardianReuseKeyFromEvents(events)
		}
	}
	if storedReuseKey != "" && storedReuseKey != reuseKey {
		events = nil
		cursor = guardianTranscriptCursor{}
	}
	return &guardianReviewSession{
		session:  guardianSession,
		reuseKey: reuseKey,
		events:   session.CloneEvents(events),
		cursor:   cursor,
	}, nil
}

func (r *guardianApprovalReviewer) startGuardianReviewSession(ctx context.Context, activeSession session.Session, reuseKey string) (session.Session, error) {
	if r == nil || r.sessions == nil {
		return session.Session{}, fmt.Errorf("approval reviewer requires session history")
	}
	projection := systemManagedAgentSessionForParent(activeSession, guardianSystemManagedAgentSpec(), map[string]any{
		guardianStateReuseKey: strings.TrimSpace(reuseKey),
	})
	metadata := maps.Clone(projection.Metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["parent_session_id"] = strings.TrimSpace(activeSession.SessionID)
	metadata["purpose"] = "approval_review"
	return r.sessions.StartSession(ctx, session.StartSessionRequest{
		AppName:            activeSession.AppName,
		UserID:             activeSession.UserID,
		Workspace:          session.WorkspaceRef{Key: activeSession.WorkspaceKey, CWD: activeSession.CWD},
		PreferredSessionID: strings.TrimSpace(projection.SessionID),
		Metadata:           metadata,
	})
}

func guardianReviewSessionID(activeSession session.Session, reuseKey string) string {
	parentID := strings.TrimSpace(activeSession.SessionID)
	if parentID == "" {
		parentID = "approval-review"
	}
	reuseKey = strings.TrimSpace(reuseKey)
	if reuseKey == "" {
		return parentID + "-approval-review"
	}
	return parentID + "-approval-review-" + reuseKey
}

func (s *guardianReviewSession) snapshot() ([]*session.Event, guardianPromptMode, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mode := guardianPromptMode{}
	if len(s.events) > 0 && s.cursor.EventCount > 0 {
		mode = guardianPromptMode{Delta: true, Cursor: s.cursor}
	}
	return session.CloneEvents(s.events), mode, s.version
}

func (s *guardianReviewSession) commit(
	ctx context.Context,
	service session.Service,
	version uint64,
	cursor guardianTranscriptCursor,
	promptEvent *session.Event,
	assistantEvent *session.Event,
) (*gateway.ApprovalReviewTrace, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.version != version {
		return nil, false, nil
	}
	if service == nil {
		return nil, false, fmt.Errorf("approval reviewer requires session history")
	}
	promptToStore := session.CloneEvent(promptEvent)
	annotateGuardianPromptState(promptToStore, s.reuseKey, cursor)
	storedPrompt, err := service.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: s.session.SessionRef,
		Event:      promptToStore,
	})
	if err != nil {
		return nil, false, err
	}
	storedAssistant, err := service.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: s.session.SessionRef,
		Event:      session.CloneEvent(assistantEvent),
	})
	if err != nil {
		return nil, false, err
	}
	s.events = append(s.events, session.CloneEvent(storedPrompt), session.CloneEvent(storedAssistant))
	s.cursor = cursor
	s.version++
	if err := service.UpdateState(ctx, s.session.SessionRef, func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		if next == nil {
			next = map[string]any{}
		}
		next[guardianStateParentSessionID] = guardianStateString(s.session.Metadata, "parent_session_id")
		next[guardianStateReuseKey] = strings.TrimSpace(s.reuseKey)
		next[guardianStateCursorEventCount] = cursor.EventCount
		next[guardianStateCursorLastEventID] = strings.TrimSpace(cursor.LastEventID)
		next[guardianStateLastPromptEventID] = strings.TrimSpace(storedPrompt.ID)
		next[guardianStateLastAssistantEventID] = strings.TrimSpace(storedAssistant.ID)
		return next, nil
	}); err != nil {
		return nil, false, err
	}
	trace := &gateway.ApprovalReviewTrace{
		SessionID:        strings.TrimSpace(s.session.SessionID),
		PromptEventID:    strings.TrimSpace(storedPrompt.ID),
		AssistantEventID: strings.TrimSpace(storedAssistant.ID),
	}
	return trace, true, nil
}

func annotateGuardianPromptState(event *session.Event, reuseKey string, cursor guardianTranscriptCursor) {
	if event == nil {
		return
	}
	if event.Meta == nil {
		event.Meta = map[string]any{}
	}
	event.Meta[guardianStateReuseKey] = strings.TrimSpace(reuseKey)
	event.Meta[guardianStateCursorEventCount] = cursor.EventCount
	event.Meta[guardianStateCursorLastEventID] = strings.TrimSpace(cursor.LastEventID)
}

func guardianReuseKey(model model.LLM, policy string) string {
	hash := sha256.New()
	if model != nil {
		hash.Write([]byte(model.Name()))
	}
	hash.Write([]byte{0})
	hash.Write([]byte(policy))
	return hex.EncodeToString(hash.Sum(nil))
}

func guardianCursorFromState(state map[string]any) guardianTranscriptCursor {
	return guardianTranscriptCursor{
		EventCount:  guardianStateInt(state, guardianStateCursorEventCount),
		LastEventID: guardianStateString(state, guardianStateCursorLastEventID),
	}
}

func guardianCursorFromEvents(events []*session.Event) guardianTranscriptCursor {
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event == nil || session.EventTypeOf(event) != session.EventTypeUser {
			continue
		}
		cursor := guardianTranscriptCursor{
			EventCount:  guardianStateInt(event.Meta, guardianStateCursorEventCount),
			LastEventID: guardianStateString(event.Meta, guardianStateCursorLastEventID),
		}
		if cursor.EventCount > 0 {
			return cursor
		}
	}
	return guardianTranscriptCursor{}
}

func guardianReuseKeyFromEvents(events []*session.Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		if event := events[i]; event != nil {
			if reuseKey := guardianStateString(event.Meta, guardianStateReuseKey); reuseKey != "" {
				return reuseKey
			}
		}
	}
	return ""
}

func guardianStateString(state map[string]any, key string) string {
	if len(state) == 0 {
		return ""
	}
	value, _ := state[key].(string)
	return strings.TrimSpace(value)
}

func guardianStateInt(state map[string]any, key string) int {
	if len(state) == 0 {
		return 0
	}
	switch value := state[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}
