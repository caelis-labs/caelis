package gatewayapp

import (
	"fmt"
	"sort"
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
	events []*session.Event

	parentCursor guardianParentCanonicalCursor
	version      uint64
	fork         *guardianConversationFork
}

// guardianConversationFork pins one model step to a common reusable prefix.
// Validated branches join back into the conversation in original ToolCall
// order regardless of review completion order.
type guardianConversationFork struct {
	ref          guardianConversationForkRef
	base         guardianConversationSnapshot
	parentEvents []*session.Event
	branches     map[int]guardianConversationBranch

	parentCursor guardianParentCanonicalCursor
	hasParent    bool
}

type guardianConversationBranch struct {
	turn          []*session.Event
	contextPrefix []*session.Event
}

type guardianConversationForkRef struct {
	Key       string
	Index     int
	CallCount int
}

// guardianParentCanonicalCursor identifies the last canonical parent event
// incorporated into a successfully validated Guardian turn.
type guardianParentCanonicalCursor struct {
	EventID  string
	EventSeq uint64
}

type guardianConversationSnapshot struct {
	Events       []*session.Event
	ParentEvents []*session.Event

	ParentCursor guardianParentCanonicalCursor
	SourceCursor guardianParentCanonicalCursor
	Version      uint64
}

type guardianConversationCommit struct {
	PrefixEvents    []*session.Event
	TurnID          string
	SessionID       string
	ExpectedVersion uint64
	Fork            guardianConversationForkRef

	ParentCursor  guardianParentCanonicalCursor
	User          *session.Event
	Assistant     *session.Event
	ContextEvents []*session.Event
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
		Events:       session.CloneEvents(conversation.events),
		ParentCursor: conversation.parentCursor,
		Version:      conversation.version,
	}, nil
}

// fork returns the pinned base for one concurrent model-step branch. The first
// branch captures the current joined conversation; later branches in the same
// step receive an exact clone of that base even if siblings finish first.
func (m *guardianConversationManager) fork(
	sessionID string,
	ref guardianConversationForkRef,
	parentEvents []*session.Event,
	cursors ...guardianParentCanonicalCursor,
) (guardianConversationSnapshot, error) {
	var sourceCursor guardianParentCanonicalCursor
	if len(cursors) > 0 {
		sourceCursor = cursors[0]
	}
	ref, err := normalizeGuardianConversationForkRef(ref)
	if err != nil {
		return guardianConversationSnapshot{}, err
	}
	if ref == (guardianConversationForkRef{}) {
		snapshot, snapshotErr := m.snapshot(sessionID)
		snapshot.ParentEvents = session.CloneEvents(parentEvents)
		snapshot.SourceCursor = sourceCursor
		return snapshot, snapshotErr
	}
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
	if conversation.fork == nil || conversation.fork.ref.Key != ref.Key {
		conversation.fork = &guardianConversationFork{
			ref: ref,
			base: guardianConversationSnapshot{
				Events:       session.CloneEvents(conversation.events),
				ParentCursor: conversation.parentCursor,
				SourceCursor: sourceCursor,
				Version:      conversation.version,
			},
			parentEvents: session.CloneEvents(parentEvents),
			branches:     map[int]guardianConversationBranch{},
		}
		if m.bySession == nil {
			m.bySession = map[string]guardianConversation{}
		}
		m.bySession[sessionID] = conversation
	} else if conversation.fork.ref.CallCount != ref.CallCount {
		return guardianConversationSnapshot{}, fmt.Errorf("guardian conversation fork call count changed within model step")
	}
	base := conversation.fork.base
	base.Events = session.CloneEvents(base.Events)
	base.ParentEvents = session.CloneEvents(conversation.fork.parentEvents)
	return base, nil
}

func normalizeGuardianConversationForkRef(ref guardianConversationForkRef) (guardianConversationForkRef, error) {
	ref.Key = strings.TrimSpace(ref.Key)
	if ref == (guardianConversationForkRef{}) {
		return ref, nil
	}
	if ref.Key == "" || ref.CallCount < 2 || ref.Index < 0 || ref.Index >= ref.CallCount {
		return guardianConversationForkRef{}, fmt.Errorf("guardian conversation fork requires a valid model step key, index, and call count")
	}
	return ref, nil
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

// commitTurn keeps a complete tool loop. Shared source input is joined once;
// concurrent approval branches are joined in their original model-call order.
func (m *guardianConversationManager) commitTurn(req guardianConversationCommit) (bool, bool, error) {
	if m == nil || req.SessionID == "" || req.Assistant == nil {
		return false, false, fmt.Errorf("invalid Guardian turn")
	}
	if _, err := normalizeGuardianConversationForkRef(req.Fork); err != nil {
		return false, false, err
	}
	if _, err := normalizeGuardianParentCanonicalCursor(req.ParentCursor); err != nil {
		return false, false, err
	}
	if _, _, err := validatedGuardianConversationPair(req.User, req.Assistant); err != nil {
		return false, false, err
	}
	var turn []*session.Event
	if len(req.ContextEvents) > 0 {
		start := -1
		for i, e := range req.ContextEvents {
			if sameGuardianConversationEvent(e, req.User) {
				start = i
			}
		}
		if start < 0 {
			return false, false, fmt.Errorf("guardian context lost current request")
		}
		turn = session.CloneEvents(req.ContextEvents[start:])
	} else {
		turn = session.CloneEvents([]*session.Event{req.User, req.Assistant})
	}
	if len(turn) < 2 || !sameGuardianConversationEvent(turn[len(turn)-1], req.Assistant) {
		return false, false, fmt.Errorf("incomplete Guardian turn")
	}
	if turn[0].Meta == nil {
		turn[0].Meta = map[string]any{}
	}
	for _, e := range turn {
		if e.Meta == nil {
			e.Meta = map[string]any{}
		}
		e.Meta[guardianTurnKey] = req.TurnID
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.bySession[req.SessionID]
	if req.Fork.Key == "" {
		if c.version != req.ExpectedVersion {
			return false, false, nil
		}
		if err := validateGuardianParentCursorAdvance(c.parentCursor, req.ParentCursor); err != nil {
			return false, false, err
		}
		c.fork = nil
		c.events = append(session.CloneEvents(req.PrefixEvents), turn...)
		c.version++
	} else {
		f := c.fork
		if f == nil || f.ref.Key != req.Fork.Key || f.ref.CallCount != req.Fork.CallCount || f.base.Version != req.ExpectedVersion {
			return false, false, nil
		}
		if _, ok := f.branches[req.Fork.Index]; ok {
			return false, false, fmt.Errorf("guardian branch already committed")
		}
		if err := validateGuardianParentCursorAdvance(f.base.ParentCursor, req.ParentCursor); err != nil {
			return false, false, err
		}
		if f.hasParent && f.parentCursor != req.ParentCursor {
			return false, false, fmt.Errorf("guardian fork source cursor changed")
		}
		f.hasParent = true
		f.parentCursor = req.ParentCursor
		f.branches[req.Fork.Index] = guardianConversationBranch{contextPrefix: session.CloneEvents(req.PrefixEvents), turn: turn}
		indexes := make([]int, 0, len(f.branches))
		for i := range f.branches {
			indexes = append(indexes, i)
		}
		sort.Ints(indexes)
		c.events = session.CloneEvents(f.branches[indexes[0]].contextPrefix)
		for _, i := range indexes {
			c.events = append(c.events, session.CloneEvents(f.branches[i].turn)...)
		}
		c.version = f.base.Version + uint64(len(indexes))
	}
	c.parentCursor = req.ParentCursor
	m.bySession[req.SessionID] = c
	return true, false, nil
}

func (m *guardianConversationManager) commitValidated(req guardianConversationCommit) (bool, bool, error) {
	return m.commitTurn(req)
}
