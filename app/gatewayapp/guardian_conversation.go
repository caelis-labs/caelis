package gatewayapp

import (
	"fmt"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// guardianConversationManager keeps validated Guardian dialogue in process
// memory. Entries are isolated by the main Session ID and are never written to
// session.Service.
type guardianConversationManager struct {
	mu        sync.Mutex
	bySession map[string]guardianConversation
}

type guardianConversation struct {
	events        []*session.Event
	parentCompact guardianParentCompactIdentity
	parentCursor  guardianParentCanonicalCursor
	version       uint64
}

// guardianParentCompactIdentity identifies the canonical compact event whose
// summary forms the current parent-context baseline. The summarized-through
// fields distinguish the compact coverage from the compact event position.
type guardianParentCompactIdentity struct {
	EventID              string
	EventSeq             uint64
	SummarizedThroughID  string
	SummarizedThroughSeq uint64
}

// guardianParentCanonicalCursor identifies the last canonical parent event
// incorporated into a successfully validated Guardian turn.
type guardianParentCanonicalCursor struct {
	EventID  string
	EventSeq uint64
}

type guardianConversationSnapshot struct {
	Events        []*session.Event
	ParentCompact guardianParentCompactIdentity
	ParentCursor  guardianParentCanonicalCursor
	Version       uint64
}

type guardianConversationCommit struct {
	SessionID       string
	ExpectedVersion uint64
	ParentCompact   guardianParentCompactIdentity
	ParentCursor    guardianParentCanonicalCursor
	User            *session.Event
	Assistant       *session.Event
	ContextEvents   []*session.Event
}

func newGuardianConversationManager() *guardianConversationManager {
	return &guardianConversationManager{bySession: map[string]guardianConversation{}}
}

func (m *guardianConversationManager) snapshot(sessionID string) (guardianConversationSnapshot, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return guardianConversationSnapshot{}, fmt.Errorf("guardian conversation requires parent session ID")
	}
	if m == nil {
		return guardianConversationSnapshot{}, fmt.Errorf("guardian conversation manager is nil")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	conversation := m.bySession[sessionID]
	return guardianConversationSnapshot{
		Events:        session.CloneEvents(conversation.events),
		ParentCompact: conversation.parentCompact,
		ParentCursor:  conversation.parentCursor,
		Version:       conversation.version,
	}, nil
}

// commitValidated records exactly one already-validated Guardian user/assistant
// exchange. When ContextEvents carries the staging runtime's model-visible
// context, that validated context replaces the prior events so transparent
// runtime compaction is retained. A version mismatch loses the optimistic race
// without changing state. A newer parent compact identity atomically replaces
// the old exchange history so prompts derived from different compact baselines
// are not mixed.
func (m *guardianConversationManager) commitValidated(req guardianConversationCommit) (committed bool, rebased bool, err error) {
	if m == nil {
		return false, false, fmt.Errorf("guardian conversation manager is nil")
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	if req.SessionID == "" {
		return false, false, fmt.Errorf("guardian conversation requires parent session ID")
	}
	parentCompact, err := normalizeGuardianParentCompactIdentity(req.ParentCompact)
	if err != nil {
		return false, false, err
	}
	parentCursor, err := normalizeGuardianParentCanonicalCursor(req.ParentCursor)
	if err != nil {
		return false, false, err
	}
	if err := validateGuardianParentPosition(parentCompact, parentCursor); err != nil {
		return false, false, err
	}
	user, assistant, err := validatedGuardianConversationPair(req.User, req.Assistant)
	if err != nil {
		return false, false, err
	}
	contextEvents, err := validatedGuardianConversationContext(req.ContextEvents, user, assistant)
	if err != nil {
		return false, false, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	conversation := m.bySession[req.SessionID]
	if conversation.version != req.ExpectedVersion {
		return false, false, nil
	}
	compactChanged, err := guardianParentCompactAdvanced(conversation.parentCompact, parentCompact)
	if err != nil {
		return false, false, err
	}
	if err := validateGuardianParentCursorAdvance(conversation.parentCursor, parentCursor); err != nil {
		return false, false, err
	}

	nextEvents := conversation.events
	if compactChanged {
		rebased = len(nextEvents) > 0
		nextEvents = nil
	}
	if len(contextEvents) > 0 {
		nextEvents = contextEvents
	} else {
		nextEvents = append(session.CloneEvents(nextEvents), user, assistant)
	}
	if m.bySession == nil {
		m.bySession = map[string]guardianConversation{}
	}
	m.bySession[req.SessionID] = guardianConversation{
		events:        nextEvents,
		parentCompact: parentCompact,
		parentCursor:  parentCursor,
		version:       conversation.version + 1,
	}
	return true, rebased, nil
}

func validatedGuardianConversationContext(
	events []*session.Event,
	currentUser *session.Event,
	currentAssistant *session.Event,
) ([]*session.Event, error) {
	if len(events) == 0 {
		return nil, nil
	}
	validated := make([]*session.Event, 0, len(events))
	start := 0
	if session.EventTypeOf(events[0]) == session.EventTypeCompact {
		checkpoint := session.CanonicalizeEvent(events[0])
		if !session.IsCanonicalHistoryEvent(checkpoint) || strings.TrimSpace(session.EventText(checkpoint)) == "" {
			return nil, fmt.Errorf("guardian conversation compact checkpoint must be canonical and non-empty")
		}
		validated = append(validated, checkpoint)
		start = 1
	}
	if (len(events)-start)%2 != 0 {
		return nil, fmt.Errorf("guardian conversation context must contain complete user/assistant pairs")
	}
	for index := start; index < len(events); index += 2 {
		user, assistant, err := validatedGuardianConversationPair(events[index], events[index+1])
		if err != nil {
			return nil, err
		}
		validated = append(validated, user, assistant)
	}
	if len(validated)-start < 2 || !sameGuardianConversationEvent(validated[len(validated)-2], currentUser) ||
		!sameGuardianConversationEvent(validated[len(validated)-1], currentAssistant) {
		return nil, fmt.Errorf("guardian conversation context must end with the current validated pair")
	}
	return validated, nil
}

func sameGuardianConversationEvent(left *session.Event, right *session.Event) bool {
	if left == nil || right == nil {
		return false
	}
	return session.EventTypeOf(left) == session.EventTypeOf(right) &&
		left.Message != nil && right.Message != nil && left.Message.Role == right.Message.Role &&
		session.EventText(left) == session.EventText(right)
}

// forget releases one main Session's non-persistent Guardian conversation.
func (m *guardianConversationManager) forget(sessionID string) {
	if m == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.bySession, sessionID)
}

func validatedGuardianConversationPair(user *session.Event, assistant *session.Event) (*session.Event, *session.Event, error) {
	validatedUser, err := validatedGuardianConversationEvent("user", user, session.EventTypeUser, model.RoleUser)
	if err != nil {
		return nil, nil, err
	}
	validatedAssistant, err := validatedGuardianConversationEvent("assistant", assistant, session.EventTypeAssistant, model.RoleAssistant)
	if err != nil {
		return nil, nil, err
	}
	return validatedUser, validatedAssistant, nil
}

func validatedGuardianConversationEvent(
	label string,
	event *session.Event,
	wantType session.EventType,
	wantRole model.Role,
) (*session.Event, error) {
	if event == nil {
		return nil, fmt.Errorf("guardian conversation %s event is nil", label)
	}
	validated := session.CanonicalizeEvent(event)
	if !session.IsCanonicalHistoryEvent(validated) || session.EventTypeOf(validated) != wantType {
		return nil, fmt.Errorf("guardian conversation %s event must be canonical %s", label, wantType)
	}
	if validated.Message == nil || validated.Message.Role != wantRole {
		return nil, fmt.Errorf("guardian conversation %s event must carry a %s message", label, wantRole)
	}
	if strings.TrimSpace(session.EventText(validated)) == "" {
		return nil, fmt.Errorf("guardian conversation %s event must carry non-empty text", label)
	}
	return validated, nil
}

func normalizeGuardianParentCompactIdentity(identity guardianParentCompactIdentity) (guardianParentCompactIdentity, error) {
	identity.EventID = strings.TrimSpace(identity.EventID)
	identity.SummarizedThroughID = strings.TrimSpace(identity.SummarizedThroughID)
	if identity == (guardianParentCompactIdentity{}) {
		return identity, nil
	}
	if identity.EventID == "" || identity.EventSeq == 0 {
		return guardianParentCompactIdentity{}, fmt.Errorf("guardian parent compact identity requires canonical event seq and ID")
	}
	if identity.SummarizedThroughSeq > identity.EventSeq {
		return guardianParentCompactIdentity{}, fmt.Errorf("guardian parent compact coverage seq %d exceeds compact event seq %d", identity.SummarizedThroughSeq, identity.EventSeq)
	}
	if identity.SummarizedThroughSeq == 0 && identity.SummarizedThroughID != "" {
		return guardianParentCompactIdentity{}, fmt.Errorf("guardian parent compact coverage ID requires coverage seq")
	}
	return identity, nil
}

func normalizeGuardianParentCanonicalCursor(cursor guardianParentCanonicalCursor) (guardianParentCanonicalCursor, error) {
	cursor.EventID = strings.TrimSpace(cursor.EventID)
	if cursor == (guardianParentCanonicalCursor{}) {
		return cursor, nil
	}
	if cursor.EventID == "" || cursor.EventSeq == 0 {
		return guardianParentCanonicalCursor{}, fmt.Errorf("guardian parent cursor requires canonical event seq and ID")
	}
	return cursor, nil
}

func validateGuardianParentPosition(compact guardianParentCompactIdentity, cursor guardianParentCanonicalCursor) error {
	if compact == (guardianParentCompactIdentity{}) || cursor == (guardianParentCanonicalCursor{}) {
		return nil
	}
	if cursor.EventSeq == compact.EventSeq && cursor.EventID != compact.EventID {
		return fmt.Errorf("guardian parent cursor and compact event disagree at seq %d", cursor.EventSeq)
	}
	if cursor.EventSeq < compact.EventSeq {
		if compact.SummarizedThroughSeq == 0 || cursor.EventSeq <= compact.SummarizedThroughSeq {
			return fmt.Errorf(
				"guardian parent cursor seq %d is not an uncovered successor of compact coverage seq %d",
				cursor.EventSeq,
				compact.SummarizedThroughSeq,
			)
		}
	}
	return nil
}

func guardianParentCompactAdvanced(current guardianParentCompactIdentity, next guardianParentCompactIdentity) (bool, error) {
	if current == next {
		return false, nil
	}
	if current == (guardianParentCompactIdentity{}) {
		return true, nil
	}
	if next == (guardianParentCompactIdentity{}) {
		return false, fmt.Errorf("guardian parent compact identity regressed from seq %d", current.EventSeq)
	}
	if next.EventSeq < current.EventSeq || next.SummarizedThroughSeq < current.SummarizedThroughSeq {
		return false, fmt.Errorf("guardian parent compact identity regressed from seq %d to %d", current.EventSeq, next.EventSeq)
	}
	if next.EventSeq == current.EventSeq {
		return false, fmt.Errorf("guardian parent compact identity changed at canonical seq %d", current.EventSeq)
	}
	return true, nil
}

func validateGuardianParentCursorAdvance(current guardianParentCanonicalCursor, next guardianParentCanonicalCursor) error {
	if current == (guardianParentCanonicalCursor{}) {
		return nil
	}
	if next == (guardianParentCanonicalCursor{}) || next.EventSeq < current.EventSeq {
		return fmt.Errorf("guardian parent cursor regressed from seq %d", current.EventSeq)
	}
	if next.EventSeq == current.EventSeq && next.EventID != current.EventID {
		return fmt.Errorf("guardian parent cursor changed ID at canonical seq %d", current.EventSeq)
	}
	return nil
}
