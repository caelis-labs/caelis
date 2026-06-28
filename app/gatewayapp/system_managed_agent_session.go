package gatewayapp

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"sync"

	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

const (
	systemManagedAgentStateAgentID              = "gateway.system_agent.agent_id"
	systemManagedAgentStatePurpose              = "gateway.system_agent.purpose"
	systemManagedAgentStateParentSessionID      = "gateway.system_agent.parent_session_id"
	systemManagedAgentStateReuseKey             = "gateway.system_agent.reuse_key"
	systemManagedAgentStateCursorEventCount     = "gateway.system_agent.cursor.event_count"
	systemManagedAgentStateCursorLastEventID    = "gateway.system_agent.cursor.last_event_id"
	systemManagedAgentStateLastPromptEventID    = "gateway.system_agent.last_prompt_event_id"
	systemManagedAgentStateLastAssistantEventID = "gateway.system_agent.last_assistant_event_id"
)

const (
	legacyGuardianStateParentSessionID      = "gateway.guardian.parent_session_id"
	legacyGuardianStateReuseKey             = "gateway.guardian.reuse_key"
	legacyGuardianStateCursorEventCount     = "gateway.guardian.cursor.event_count"
	legacyGuardianStateCursorLastEventID    = "gateway.guardian.cursor.last_event_id"
	legacyGuardianStateLastPromptEventID    = "gateway.guardian.last_prompt_event_id"
	legacyGuardianStateLastAssistantEventID = "gateway.guardian.last_assistant_event_id"
)

// systemManagedAgentSessionCache owns hidden child-session reuse for
// system-managed agents. It preserves a stable validated prefix while allowing
// domain-specific context builders to append only their next prompt.
type systemManagedAgentSessionCache struct {
	service session.Service
	mu      sync.Mutex
	byKey   map[string]*systemManagedAgentSession
}

type systemManagedAgentSessionRequest struct {
	ParentKey     string
	ParentSession session.Session
	Spec          systemManagedAgentSpec
	Purpose       systemManagedAgentPurpose
	ReuseKey      string
}

type systemManagedAgentSession struct {
	mu       sync.Mutex
	session  session.Session
	agentID  string
	purpose  systemManagedAgentPurpose
	reuseKey string
	events   []*session.Event
	cursor   systemManagedAgentTranscriptCursor
	version  uint64
}

type systemManagedAgentTranscriptCursor struct {
	EventCount  int
	LastEventID string
}

type systemManagedAgentSessionSnapshot struct {
	Events  []*session.Event
	Delta   bool
	Cursor  systemManagedAgentTranscriptCursor
	Version uint64
}

func newSystemManagedAgentSessionCache(service session.Service) *systemManagedAgentSessionCache {
	return &systemManagedAgentSessionCache{
		service: service,
		byKey:   map[string]*systemManagedAgentSession{},
	}
}

func (c *systemManagedAgentSessionCache) sessionFor(ctx context.Context, req systemManagedAgentSessionRequest) (*systemManagedAgentSession, error) {
	if c == nil || c.service == nil {
		return nil, fmt.Errorf("system-managed agent requires session history")
	}
	req = normalizeSystemManagedAgentSessionRequest(req)
	cacheKey := systemManagedAgentSessionCacheKey(req)
	c.mu.Lock()
	defer c.mu.Unlock()
	item := c.byKey[cacheKey]
	if item == nil || item.reuseKey != req.ReuseKey {
		loaded, err := c.load(ctx, req)
		if err != nil {
			return nil, err
		}
		item = loaded
		c.byKey[cacheKey] = item
	}
	return item, nil
}

func (c *systemManagedAgentSessionCache) cached(req systemManagedAgentSessionRequest) *systemManagedAgentSession {
	if c == nil {
		return nil
	}
	req = normalizeSystemManagedAgentSessionRequest(req)
	cacheKey := systemManagedAgentSessionCacheKey(req)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.byKey[cacheKey]
}

func normalizeSystemManagedAgentSessionRequest(req systemManagedAgentSessionRequest) systemManagedAgentSessionRequest {
	req.ParentKey = strings.TrimSpace(req.ParentKey)
	if req.ParentKey == "" {
		req.ParentKey = strings.TrimSpace(req.ParentSession.SessionID)
	}
	if req.ParentKey == "" {
		req.ParentKey = "default"
	}
	req.Spec.ID = strings.TrimSpace(req.Spec.ID)
	if req.Purpose == "" {
		req.Purpose = req.Spec.Purpose
	}
	if req.Purpose == "" {
		req.Purpose = systemManagedAgentPurpose(req.Spec.ID)
	}
	req.ReuseKey = strings.TrimSpace(req.ReuseKey)
	return req
}

func systemManagedAgentSessionCacheKey(req systemManagedAgentSessionRequest) string {
	return strings.Join([]string{
		strings.TrimSpace(req.ParentKey),
		strings.TrimSpace(req.Spec.ID),
		strings.TrimSpace(string(req.Purpose)),
	}, "\x00")
}

func (c *systemManagedAgentSessionCache) load(ctx context.Context, req systemManagedAgentSessionRequest) (*systemManagedAgentSession, error) {
	childSession, err := c.start(ctx, req)
	if err != nil {
		return nil, err
	}
	state, err := c.service.SnapshotState(ctx, childSession.SessionRef)
	if err != nil {
		return nil, err
	}
	storedReuseKey := systemManagedAgentStateString(state, systemManagedAgentStateReuseKey)
	var events []*session.Event
	cursor := systemManagedAgentTranscriptCursor{}
	if storedReuseKey == "" || storedReuseKey == req.ReuseKey {
		events, err = c.service.Events(ctx, session.EventsRequest{SessionRef: childSession.SessionRef})
		if err != nil {
			return nil, err
		}
		cursor = systemManagedAgentCursorFromState(state)
		if cursor.EventCount == 0 {
			cursor = systemManagedAgentCursorFromEvents(events)
		}
		if storedReuseKey == "" {
			storedReuseKey = systemManagedAgentReuseKeyFromEvents(events)
		}
	}
	if storedReuseKey != "" && storedReuseKey != req.ReuseKey {
		events = nil
		cursor = systemManagedAgentTranscriptCursor{}
	}
	return &systemManagedAgentSession{
		session:  childSession,
		agentID:  req.Spec.ID,
		purpose:  req.Purpose,
		reuseKey: req.ReuseKey,
		events:   session.CloneEvents(events),
		cursor:   cursor,
	}, nil
}

func (c *systemManagedAgentSessionCache) start(ctx context.Context, req systemManagedAgentSessionRequest) (session.Session, error) {
	projection := systemManagedAgentSessionForParent(req.ParentSession, req.Spec, map[string]any{
		systemManagedAgentStateReuseKey: strings.TrimSpace(req.ReuseKey),
		"system_managed_purpose":        strings.TrimSpace(string(req.Purpose)),
	})
	metadata := maps.Clone(projection.Metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata[systemManagedAgentStateAgentID] = strings.TrimSpace(req.Spec.ID)
	metadata[systemManagedAgentStatePurpose] = strings.TrimSpace(string(req.Purpose))
	metadata[systemManagedAgentStateParentSessionID] = strings.TrimSpace(req.ParentSession.SessionID)
	metadata[systemManagedAgentStateReuseKey] = strings.TrimSpace(req.ReuseKey)
	return c.service.StartSession(ctx, session.StartSessionRequest{
		AppName:            req.ParentSession.AppName,
		UserID:             req.ParentSession.UserID,
		Workspace:          session.WorkspaceRef{Key: req.ParentSession.WorkspaceKey, CWD: req.ParentSession.CWD},
		PreferredSessionID: strings.TrimSpace(projection.SessionID),
		Metadata:           metadata,
	})
}

func (s *systemManagedAgentSession) snapshot() systemManagedAgentSessionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return systemManagedAgentSessionSnapshot{
		Events:  session.CloneEvents(s.events),
		Delta:   len(s.events) > 0 && s.cursor.EventCount > 0,
		Cursor:  s.cursor,
		Version: s.version,
	}
}

func (s *systemManagedAgentSession) commit(
	ctx context.Context,
	service session.Service,
	version uint64,
	cursor systemManagedAgentTranscriptCursor,
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
	annotateSystemManagedAgentPromptState(promptToStore, s.agentID, s.purpose, s.reuseKey, cursor)
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
		next[systemManagedAgentStateAgentID] = strings.TrimSpace(s.agentID)
		next[systemManagedAgentStatePurpose] = strings.TrimSpace(string(s.purpose))
		next[systemManagedAgentStateParentSessionID] = firstNonEmpty(
			systemManagedAgentStateString(s.session.Metadata, systemManagedAgentStateParentSessionID),
			systemManagedAgentStateString(s.session.Metadata, "parent_session_id"),
		)
		next[systemManagedAgentStateReuseKey] = strings.TrimSpace(s.reuseKey)
		next[systemManagedAgentStateCursorEventCount] = cursor.EventCount
		next[systemManagedAgentStateCursorLastEventID] = strings.TrimSpace(cursor.LastEventID)
		next[systemManagedAgentStateLastPromptEventID] = strings.TrimSpace(storedPrompt.ID)
		next[systemManagedAgentStateLastAssistantEventID] = strings.TrimSpace(storedAssistant.ID)
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

func annotateSystemManagedAgentPromptState(
	event *session.Event,
	agentID string,
	purpose systemManagedAgentPurpose,
	reuseKey string,
	cursor systemManagedAgentTranscriptCursor,
) {
	if event == nil {
		return
	}
	if event.Meta == nil {
		event.Meta = map[string]any{}
	}
	event.Meta[systemManagedAgentStateAgentID] = strings.TrimSpace(agentID)
	event.Meta[systemManagedAgentStatePurpose] = strings.TrimSpace(string(purpose))
	event.Meta[systemManagedAgentStateReuseKey] = strings.TrimSpace(reuseKey)
	event.Meta[systemManagedAgentStateCursorEventCount] = cursor.EventCount
	event.Meta[systemManagedAgentStateCursorLastEventID] = strings.TrimSpace(cursor.LastEventID)
}

func systemManagedAgentCursorFromState(state map[string]any) systemManagedAgentTranscriptCursor {
	return systemManagedAgentTranscriptCursor{
		EventCount:  systemManagedAgentStateInt(state, systemManagedAgentStateCursorEventCount),
		LastEventID: systemManagedAgentStateString(state, systemManagedAgentStateCursorLastEventID),
	}
}

func systemManagedAgentCursorFromEvents(events []*session.Event) systemManagedAgentTranscriptCursor {
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event == nil || session.EventTypeOf(event) != session.EventTypeUser {
			continue
		}
		cursor := systemManagedAgentTranscriptCursor{
			EventCount:  systemManagedAgentStateInt(event.Meta, systemManagedAgentStateCursorEventCount),
			LastEventID: systemManagedAgentStateString(event.Meta, systemManagedAgentStateCursorLastEventID),
		}
		if cursor.EventCount > 0 {
			return cursor
		}
	}
	return systemManagedAgentTranscriptCursor{}
}

func systemManagedAgentReuseKeyFromEvents(events []*session.Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		if event := events[i]; event != nil {
			if reuseKey := systemManagedAgentStateString(event.Meta, systemManagedAgentStateReuseKey); reuseKey != "" {
				return reuseKey
			}
		}
	}
	return ""
}

func systemManagedAgentStateString(state map[string]any, key string) string {
	if len(state) == 0 {
		return ""
	}
	value, _ := state[key].(string)
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return trimmed
	}
	if legacyKey := legacySystemManagedAgentStateKey(key); legacyKey != "" {
		value, _ = state[legacyKey].(string)
		return strings.TrimSpace(value)
	}
	return ""
}

func systemManagedAgentStateInt(state map[string]any, key string) int {
	if len(state) == 0 {
		return 0
	}
	if value, ok := state[key]; ok {
		return systemManagedAgentStateIntValue(value)
	}
	if legacyKey := legacySystemManagedAgentStateKey(key); legacyKey != "" {
		return systemManagedAgentStateIntValue(state[legacyKey])
	}
	return 0
}

func systemManagedAgentStateIntValue(value any) int {
	switch value := value.(type) {
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

func legacySystemManagedAgentStateKey(key string) string {
	switch key {
	case systemManagedAgentStateParentSessionID:
		return legacyGuardianStateParentSessionID
	case systemManagedAgentStateReuseKey:
		return legacyGuardianStateReuseKey
	case systemManagedAgentStateCursorEventCount:
		return legacyGuardianStateCursorEventCount
	case systemManagedAgentStateCursorLastEventID:
		return legacyGuardianStateCursorLastEventID
	case systemManagedAgentStateLastPromptEventID:
		return legacyGuardianStateLastPromptEventID
	case systemManagedAgentStateLastAssistantEventID:
		return legacyGuardianStateLastAssistantEventID
	default:
		return ""
	}
}
