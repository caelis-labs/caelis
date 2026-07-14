package kernel

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func (g *Gateway) releaseActive(sessionID string, handle *turnHandle) {
	g.mu.Lock()
	defer g.mu.Unlock()
	current, ok := g.active[sessionID]
	if !ok || current != handle {
		return
	}
	delete(g.active, sessionID)
}

func (g *Gateway) CurrentSession(bindingKey string) (session.SessionRef, bool) {
	if g == nil || strings.TrimSpace(bindingKey) == "" {
		return session.SessionRef{}, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	binding, ok := g.bindingLocked(strings.TrimSpace(bindingKey))
	if !ok || strings.TrimSpace(binding.current.SessionID) == "" {
		return session.SessionRef{}, false
	}
	return binding.current, true
}

func (g *Gateway) ClearBinding(bindingKey string) {
	if g == nil || strings.TrimSpace(bindingKey) == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.bindings, strings.TrimSpace(bindingKey))
}

func (g *Gateway) bind(bindingKey string, ref session.SessionRef, desc BindingDescriptor) {
	if g == nil || strings.TrimSpace(bindingKey) == "" || strings.TrimSpace(ref.SessionID) == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	key := strings.TrimSpace(bindingKey)
	now := g.clock()
	current, ok := g.bindingLocked(key)
	if !ok || current.current.SessionID != ref.SessionID {
		current = sessionBinding{
			current: ref,
			boundAt: now,
		}
	}
	current.current = ref
	current.updatedAt = now
	current.surface = firstNonEmpty(strings.TrimSpace(desc.Surface), current.surface, key)
	current.actorKind = firstNonEmpty(strings.TrimSpace(desc.ActorKind), current.actorKind)
	current.actorID = firstNonEmpty(strings.TrimSpace(desc.ActorID), current.actorID)
	current.owner = firstNonEmpty(strings.TrimSpace(desc.Owner), current.owner)
	if !desc.ExpiresAt.IsZero() {
		current.expiresAt = desc.ExpiresAt
	}
	g.bindings[key] = current
}

func (g *Gateway) resolveResumeTarget(req ResumeSessionRequest, sessions []session.SessionSummary) (session.SessionSummary, error) {
	target := strings.TrimSpace(req.SessionID)
	if target != "" {
		return resolveSessionSummary(sessions, target)
	}
	exclude := strings.TrimSpace(req.ExcludeSessionID)
	if exclude == "" {
		if current, ok := g.CurrentSession(req.BindingKey); ok {
			exclude = current.SessionID
		}
	}
	for _, session := range sessions {
		if strings.TrimSpace(session.SessionID) == "" || session.SessionID == exclude {
			continue
		}
		return session, nil
	}
	return session.SessionSummary{}, &Error{
		Kind:        KindNotFound,
		Code:        CodeNoResumableSession,
		UserVisible: true,
		Message:     "gateway: no resumable session found",
	}
}

func resolveSessionSummary(sessions []session.SessionSummary, target string) (session.SessionSummary, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return session.SessionSummary{}, &Error{
			Kind:        KindValidation,
			Code:        CodeInvalidRequest,
			UserVisible: true,
			Message:     "gateway: session id is required",
		}
	}
	var exact *session.SessionSummary
	var prefixMatches []session.SessionSummary
	for _, session := range sessions {
		id := strings.TrimSpace(session.SessionID)
		if id == "" {
			continue
		}
		if id == target {
			matched := session
			exact = &matched
			break
		}
		if strings.HasPrefix(id, target) {
			prefixMatches = append(prefixMatches, session)
		}
	}
	if exact != nil {
		return *exact, nil
	}
	switch len(prefixMatches) {
	case 1:
		return prefixMatches[0], nil
	case 0:
		return session.SessionSummary{}, &Error{
			Kind:        KindNotFound,
			Code:        CodeSessionNotFound,
			UserVisible: true,
			Message:     "gateway: session not found",
		}
	default:
		return session.SessionSummary{}, &Error{
			Kind:        KindConflict,
			Code:        CodeSessionAmbiguous,
			UserVisible: true,
			Message:     "gateway: session id is ambiguous",
		}
	}
}

func (g *Gateway) interruptTarget(req InterruptRequest) (session.SessionRef, error) {
	return g.sessionTarget(req.SessionRef, req.BindingKey)
}

func (g *Gateway) sessionTarget(ref session.SessionRef, bindingKey string) (session.SessionRef, error) {
	if strings.TrimSpace(ref.SessionID) != "" {
		return ref, nil
	}
	if current, ok := g.CurrentSession(bindingKey); ok {
		return current, nil
	}
	if strings.TrimSpace(bindingKey) != "" {
		return session.SessionRef{}, &Error{
			Kind:        KindNotFound,
			Code:        CodeBindingNotFound,
			UserVisible: true,
			Message:     "gateway: binding not found",
		}
	}
	return session.SessionRef{}, &Error{
		Kind:        KindValidation,
		Code:        CodeInvalidRequest,
		UserVisible: true,
		Message:     "gateway: session ref or binding key is required",
	}
}

func (g *Gateway) hasActiveHandle(sessionID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	handle, ok := g.active[strings.TrimSpace(sessionID)]
	return ok && handle != nil
}

func (g *Gateway) noteActiveHandleLocked(sessionID string, handle *turnHandle) {
	if handle == nil {
		return
	}
	for key, binding := range g.bindings {
		if binding.current.SessionID != sessionID {
			continue
		}
		binding.lastHandleID = handle.HandleID()
		binding.lastRunID = handle.RunID()
		binding.lastTurnID = handle.TurnID()
		g.bindings[key] = binding
	}
}

func (g *Gateway) noteSessionCursor(sessionID string, cursor string) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(cursor) == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for key, binding := range g.bindings {
		if binding.current.SessionID != sessionID {
			continue
		}
		binding.lastEventCursor = strings.TrimSpace(cursor)
		g.bindings[key] = binding
	}
}

func (g *Gateway) bindingLocked(bindingKey string) (sessionBinding, bool) {
	binding, ok := g.bindings[strings.TrimSpace(bindingKey)]
	if !ok {
		return sessionBinding{}, false
	}
	if !binding.expiresAt.IsZero() && !binding.expiresAt.After(g.clock()) {
		delete(g.bindings, strings.TrimSpace(bindingKey))
		return sessionBinding{}, false
	}
	return binding, true
}
