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
	events        []*session.Event
	parentCompact guardianParentCompactIdentity
	parentCursor  guardianParentCanonicalCursor
	version       uint64
	fork          *guardianConversationFork
}

// guardianConversationFork pins one model step to a common reusable prefix.
// Validated branches join back into the conversation in original ToolCall
// order regardless of review completion order.
type guardianConversationFork struct {
	ref           guardianConversationForkRef
	base          guardianConversationSnapshot
	parentEvents  []*session.Event
	branches      map[int]guardianConversationBranch
	parentCompact guardianParentCompactIdentity
	parentCursor  guardianParentCanonicalCursor
	hasParent     bool
}

type guardianConversationBranch struct {
	user          *session.Event
	assistant     *session.Event
	contextPrefix []*session.Event
	hasContext    bool
}

type guardianConversationForkRef struct {
	Key       string
	Index     int
	CallCount int
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
	ParentEvents  []*session.Event
	ParentCompact guardianParentCompactIdentity
	ParentCursor  guardianParentCanonicalCursor
	Version       uint64
}

type guardianConversationCommit struct {
	SessionID       string
	ExpectedVersion uint64
	Fork            guardianConversationForkRef
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

// fork returns the pinned base for one concurrent model-step branch. The first
// branch captures the current joined conversation; later branches in the same
// step receive an exact clone of that base even if siblings finish first.
func (m *guardianConversationManager) fork(
	sessionID string,
	ref guardianConversationForkRef,
	parentEvents []*session.Event,
) (guardianConversationSnapshot, error) {
	ref, err := normalizeGuardianConversationForkRef(ref)
	if err != nil {
		return guardianConversationSnapshot{}, err
	}
	if ref == (guardianConversationForkRef{}) {
		snapshot, snapshotErr := m.snapshot(sessionID)
		snapshot.ParentEvents = session.CloneEvents(parentEvents)
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
				Events:        session.CloneEvents(conversation.events),
				ParentCompact: conversation.parentCompact,
				ParentCursor:  conversation.parentCursor,
				Version:       conversation.version,
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

// commitValidated records exactly one already-validated Guardian user/assistant
// exchange. Concurrent branches from one model step share a pinned fork and
// join in original ToolCall order; linear callers retain optimistic versioned
// commits. When ContextEvents carries the staging runtime's model-visible
// context, its prefix retains transparent runtime compaction. A newer parent
// compact identity atomically replaces the old exchange history so prompts
// derived from different compact baselines are not mixed.
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
	req.ParentCompact = parentCompact
	req.ParentCursor = parentCursor
	user, assistant, err := validatedGuardianConversationPair(req.User, req.Assistant)
	if err != nil {
		return false, false, err
	}
	contextEvents, err := validatedGuardianConversationContext(req.ContextEvents, user, assistant)
	if err != nil {
		return false, false, err
	}
	forkRef, err := normalizeGuardianConversationForkRef(req.Fork)
	if err != nil {
		return false, false, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	conversation := m.bySession[req.SessionID]
	if forkRef != (guardianConversationForkRef{}) {
		return m.commitForkValidatedLocked(conversation, req, forkRef, user, assistant, contextEvents)
	}
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

func (m *guardianConversationManager) commitForkValidatedLocked(
	conversation guardianConversation,
	req guardianConversationCommit,
	ref guardianConversationForkRef,
	user *session.Event,
	assistant *session.Event,
	contextEvents []*session.Event,
) (bool, bool, error) {
	fork := conversation.fork
	if fork == nil || fork.ref.Key != ref.Key || fork.ref.CallCount != ref.CallCount || fork.base.Version != req.ExpectedVersion {
		return false, false, nil
	}
	if _, exists := fork.branches[ref.Index]; exists {
		return false, false, fmt.Errorf("guardian conversation fork already contains ToolCall index %d", ref.Index)
	}
	compactChanged, err := guardianParentCompactAdvanced(fork.base.ParentCompact, req.ParentCompact)
	if err != nil {
		return false, false, err
	}
	if err := validateGuardianParentCursorAdvance(fork.base.ParentCursor, req.ParentCursor); err != nil {
		return false, false, err
	}
	if fork.hasParent && (fork.parentCompact != req.ParentCompact || fork.parentCursor != req.ParentCursor) {
		return false, false, fmt.Errorf("guardian conversation fork branches observed different parent transcript positions")
	}
	if !fork.hasParent {
		fork.parentCompact = req.ParentCompact
		fork.parentCursor = req.ParentCursor
		fork.hasParent = true
	}
	branch := guardianConversationBranch{user: user, assistant: assistant}
	if len(contextEvents) > 0 {
		branch.contextPrefix = session.CloneEvents(contextEvents[:len(contextEvents)-2])
		branch.hasContext = true
	}
	fork.branches[ref.Index] = branch

	indexes := make([]int, 0, len(fork.branches))
	for index := range fork.branches {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	nextEvents := session.CloneEvents(fork.base.Events)
	if compactChanged {
		nextEvents = nil
	}
	for _, index := range indexes {
		candidate := fork.branches[index]
		if candidate.hasContext {
			nextEvents = session.CloneEvents(candidate.contextPrefix)
			break
		}
	}
	for _, index := range indexes {
		candidate := fork.branches[index]
		nextEvents = append(nextEvents, session.CloneEvent(candidate.user), session.CloneEvent(candidate.assistant))
	}
	conversation.events = nextEvents
	conversation.parentCompact = fork.parentCompact
	conversation.parentCursor = fork.parentCursor
	conversation.version = fork.base.Version + uint64(len(fork.branches))
	conversation.fork = fork
	m.bySession[req.SessionID] = conversation
	return true, compactChanged && len(fork.base.Events) > 0, nil
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
