package tuiapp

import (
	"strconv"
	"strings"
	"time"
)

type runningActivityPhase string

const (
	runningPhaseModelWait  runningActivityPhase = "waiting"
	runningPhaseThinking   runningActivityPhase = "thinking"
	runningPhaseResponding runningActivityPhase = "responding"
	runningPhaseSearch     runningActivityPhase = "search"
	runningPhaseWebSearch  runningActivityPhase = "web_search"
	runningPhaseFetch      runningActivityPhase = "fetch"
	runningPhaseToolWait   runningActivityPhase = "wait"
	runningPhaseCancel     runningActivityPhase = "cancel"
	runningPhaseReview     runningActivityPhase = "review"
	runningPhaseInterrupt  runningActivityPhase = "interrupt"
	runningPhaseRetrying   runningActivityPhase = "retrying"
	runningPhaseCompact    runningActivityPhase = "compact"
)

const runningCompactActivityKey = "context-compact"

type runningActivityTarget string

const (
	runningTargetShell    runningActivityTarget = "shell"
	runningTargetSubagent runningActivityTarget = "subagent"
	runningTargetTask     runningActivityTarget = "task"
)

type runningActivityState struct {
	Phase     runningActivityPhase
	Target    runningActivityTarget
	Key       string
	CallID    string
	StartedAt time.Time
}

type runningActivityOwner struct {
	Key     string
	CallID  string
	Handle  string
	BlockID string
	Target  runningActivityTarget
}

// runningHintTracker owns the TUI-only live hint projection derived from ACP
// updates. Tool invocations remain independently keyed activities, so an
// observer such as Task wait can finish without finishing the Spawn or
// RunCommand owner it observes. Completed keys are retained for the Session so
// late non-terminal projections cannot revive an already closed activity.
type runningHintTracker struct {
	focus           runningActivityState
	focusForeground bool
	foregroundKey   string
	active          map[string]runningActivityState
	order           []string
	completed       map[string]struct{}
	compact         runningActivityState
	overlay         runningActivityState
	ownersByHandle  map[string]runningActivityOwner
	ownersByCallID  map[string][]runningActivityOwner
	turnGeneration  uint64
	turnStartedAt   time.Time
}

func newRunningHintTracker() runningHintTracker {
	return runningHintTracker{
		active:         map[string]runningActivityState{},
		completed:      map[string]struct{}{},
		ownersByHandle: map[string]runningActivityOwner{},
		ownersByCallID: map[string][]runningActivityOwner{},
	}
}

func (t *runningHintTracker) ensure() {
	if t.active == nil {
		t.active = map[string]runningActivityState{}
	}
	if t.completed == nil {
		t.completed = map[string]struct{}{}
	}
	if t.ownersByHandle == nil {
		t.ownersByHandle = map[string]runningActivityOwner{}
	}
	if t.ownersByCallID == nil {
		t.ownersByCallID = map[string][]runningActivityOwner{}
	}
}

func (t *runningHintTracker) beginTurn(startedAt time.Time) {
	t.ensure()
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	clear(t.active)
	t.order = t.order[:0]
	t.compact = runningActivityState{}
	t.overlay = runningActivityState{}
	t.setModelWait(startedAt)
	t.turnGeneration++
	t.turnStartedAt = startedAt
}

func (t *runningHintTracker) endTurn() {
	t.ensure()
	clear(t.active)
	t.order = t.order[:0]
	t.compact = runningActivityState{}
	t.overlay = runningActivityState{}
	t.focus = runningActivityState{}
	t.focusForeground = false
	t.foregroundKey = ""
	t.turnStartedAt = time.Time{}
}

func (t *runningHintTracker) resetSession() {
	*t = newRunningHintTracker()
}

func (t *runningHintTracker) clearRetryFocus() {
	if t == nil || t.focus.Phase != runningPhaseRetrying {
		return
	}
	t.focus = runningActivityState{}
	t.focusForeground = false
	t.foregroundKey = ""
}

func (t *runningHintTracker) setFocus(
	phase runningActivityPhase,
	target runningActivityTarget,
	key string,
	now time.Time,
) {
	if phase == "" {
		return
	}
	key = strings.TrimSpace(key)
	startedAt := now
	if t.focus.Phase == phase && t.focus.Target == target && t.focus.Key == key && !t.focus.StartedAt.IsZero() {
		startedAt = t.focus.StartedAt
	}
	t.focus = runningActivityState{
		Phase:     phase,
		Target:    target,
		Key:       key,
		StartedAt: startedAt,
	}
	t.focusForeground = true
	t.foregroundKey = ""
}

func (t *runningHintTracker) start(
	key string,
	phase runningActivityPhase,
	target runningActivityTarget,
	now time.Time,
	callID string,
) {
	key = strings.TrimSpace(key)
	if key == "" || phase == "" {
		return
	}
	t.ensure()
	if _, closed := t.completed[key]; closed {
		return
	}
	entry := runningActivityState{
		Phase:     phase,
		Target:    target,
		Key:       key,
		CallID:    strings.TrimSpace(callID),
		StartedAt: now,
	}
	_, existed := t.active[key]
	if previous, exists := t.active[key]; exists {
		if previous.Phase == phase && !previous.StartedAt.IsZero() {
			entry.StartedAt = previous.StartedAt
		}
		// Keep progress recency for the fallback used only when no explicit
		// narrative or tool owner holds foreground.
		t.removeOrderKey(key)
	}
	t.active[key] = entry
	t.order = append(t.order, key)
	if !existed {
		t.focusForeground = false
		t.foregroundKey = key
	}
}

func (t *runningHintTracker) complete(key string, now time.Time) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	t.ensure()
	t.completed[key] = struct{}{}
	delete(t.active, key)
	t.removeOrderKey(key)
	if t.foregroundKey == key {
		t.foregroundKey = ""
	}
	if len(t.active) == 0 && !t.focusForeground {
		t.setModelWait(now)
	}
}

// completeTool closes the exact invocation key and any indexed presentation
// owner for the same ACP tool call. Task-stream frames carry the runtime Turn
// identity, while the parent RunCommand/Spawn activity was opened by the main
// Turn; the typed tool-call relation is the stable join between those streams.
func (t *runningHintTracker) completeTool(key string, callID string, now time.Time) {
	t.complete(key, now)
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return
	}
	for _, owner := range t.ownersByCallID[callID] {
		if owner.Key == key {
			continue
		}
		if _, active := t.active[owner.Key]; active {
			t.complete(owner.Key, now)
		}
	}
}

func (t *runningHintTracker) setCompact(now time.Time) {
	startedAt := now
	alreadyCompacting := t.compact.Phase == runningPhaseCompact
	if alreadyCompacting && !t.compact.StartedAt.IsZero() {
		startedAt = t.compact.StartedAt
	}
	t.compact = runningActivityState{
		Phase:     runningPhaseCompact,
		Key:       runningCompactActivityKey,
		StartedAt: startedAt,
	}
	if !alreadyCompacting && len(t.active) == 0 {
		// Replace the pre-compaction model phase with the expected retry wait.
		// Later foreground events still advance beneath the compact boundary.
		t.setModelWait(now)
	}
}

// completeCompact releases the strong compaction boundary only after its typed
// success/failure signal. Foreground activity may advance underneath the
// boundary and becomes visible after release; otherwise the Agent is waiting on
// its retried model request.
func (t *runningHintTracker) completeCompact(now time.Time) {
	if t.compact.Phase != runningPhaseCompact {
		return
	}
	t.compact = runningActivityState{}
	if t.focusForeground && t.focus.Phase != "" && t.focus.Phase != runningPhaseModelWait {
		return
	}
	if len(t.active) > 0 && !t.focusForeground {
		return
	}
	t.setModelWait(now)
}

func (t *runningHintTracker) setModelWait(now time.Time) {
	t.focus = runningActivityState{
		Phase:     runningPhaseModelWait,
		Key:       "model",
		StartedAt: now,
	}
	t.focusForeground = true
	t.foregroundKey = ""
}

func (t *runningHintTracker) setOverlay(phase runningActivityPhase, key string, now time.Time) {
	key = strings.TrimSpace(key)
	if phase == "" || key == "" {
		return
	}
	t.overlay = runningActivityState{
		Phase:     phase,
		Key:       key,
		StartedAt: now,
	}
}

func (t *runningHintTracker) clearOverlay(key string) {
	key = strings.TrimSpace(key)
	if key == "" || t.overlay.Key == key {
		t.overlay = runningActivityState{}
	}
}

func (t *runningHintTracker) visible(turnRunning bool) runningActivityState {
	if t.overlay.Phase != "" {
		return t.overlay
	}
	if t.compact.Phase != "" {
		return t.compact
	}
	if t.focusForeground && t.focus.Phase != "" {
		return t.focus
	}
	if t.foregroundKey != "" {
		if entry, ok := t.active[t.foregroundKey]; ok {
			return entry
		}
	}
	for index := len(t.order) - 1; index >= 0; index-- {
		if entry, ok := t.active[t.order[index]]; ok {
			return entry
		}
	}
	if t.focus.Phase != "" {
		return t.focus
	}
	if turnRunning {
		return runningActivityState{
			Phase:     runningPhaseModelWait,
			Key:       "model",
			StartedAt: t.turnStartedAt,
		}
	}
	return runningActivityState{}
}

func (t *runningHintTracker) observeOwner(handle string, owner runningActivityOwner) {
	handle = normalizeRunningActivityHandle(handle)
	owner.Key = strings.TrimSpace(owner.Key)
	owner.CallID = strings.TrimSpace(owner.CallID)
	owner.BlockID = strings.TrimSpace(owner.BlockID)
	if handle != "" {
		owner.Handle = handle
	} else {
		owner.Handle = normalizeRunningActivityHandle(owner.Handle)
	}
	if owner.Key == "" || owner.Target == "" {
		return
	}
	t.ensure()
	if owner.CallID != "" {
		owners := t.ownersByCallID[owner.CallID]
		replaced := false
		for index := range owners {
			if owners[index].Key != owner.Key {
				continue
			}
			owner = mergeRunningActivityOwner(owners[index], owner)
			owners[index] = owner
			replaced = true
			break
		}
		if !replaced {
			owners = append(owners, owner)
		}
		t.ownersByCallID[owner.CallID] = owners
	}
	if owner.Handle != "" {
		t.ownersByHandle[owner.Handle] = owner
	}
}

func mergeRunningActivityOwner(previous runningActivityOwner, current runningActivityOwner) runningActivityOwner {
	if current.Key == "" {
		current.Key = previous.Key
	}
	if current.CallID == "" {
		current.CallID = previous.CallID
	}
	if current.Handle == "" {
		current.Handle = previous.Handle
	}
	if current.BlockID == "" {
		current.BlockID = previous.BlockID
	}
	if current.Target == "" {
		current.Target = previous.Target
	}
	return current
}

// toolKey prefers the typed Turn identity. Some compatibility/live Envelopes
// omit TurnID, so the current Turn generation supplies a bounded fallback and
// OccurredAt rejects updates that predate that generation.
func (t *runningHintTracker) toolKey(turnID string, callID string, occurredAt time.Time) string {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return ""
	}
	if turnID = strings.TrimSpace(turnID); turnID != "" {
		return "tool:" + turnID + ":" + callID
	}
	if !occurredAt.IsZero() && !t.turnStartedAt.IsZero() && occurredAt.Before(t.turnStartedAt) {
		return ""
	}
	return "tool:g" + strconv.FormatUint(t.turnGeneration, 10) + ":" + callID
}

func (t *runningHintTracker) observedOwnerCandidates(handle string, parentCallID string) []runningActivityOwner {
	handle = normalizeRunningActivityHandle(handle)
	parentCallID = strings.TrimSpace(parentCallID)
	t.ensure()
	callOwners := t.ownersByCallID[parentCallID]
	if len(callOwners) == 0 {
		return nil
	}
	if handle == "" {
		return append([]runningActivityOwner(nil), callOwners...)
	}
	handleOwner := t.ownersByHandle[handle]
	if handleOwner.Key != "" && handleOwner.CallID != parentCallID {
		return nil
	}
	// Return every owner for the call ID. The Model filters owners whose block
	// is still open before applying exact-handle/unique-compatible rules; doing
	// that here would let a closed owner make a later fallback ambiguous.
	return append([]runningActivityOwner(nil), callOwners...)
}

// presentationOwner resolves one rendered Task owner through the same
// normalized handle/call index used by running activity. Known handle and typed
// parent identities must agree. An unknown handle may fall back to the typed
// parent only when it has one compatible rendered owner.
func (t *runningHintTracker) presentationOwner(
	handle string,
	parentCallID string,
	target runningActivityTarget,
) (runningActivityOwner, bool) {
	handle = normalizeRunningActivityHandle(handle)
	parentCallID = strings.TrimSpace(parentCallID)
	t.ensure()
	if handle != "" {
		owner := t.ownersByHandle[handle]
		if owner.Key != "" {
			if owner.BlockID == "" || owner.Target != target {
				return runningActivityOwner{}, false
			}
			if parentCallID != "" && owner.CallID != parentCallID {
				return runningActivityOwner{}, false
			}
			return owner, true
		}
	}
	if parentCallID == "" {
		return runningActivityOwner{}, false
	}
	var match runningActivityOwner
	for _, owner := range t.ownersByCallID[parentCallID] {
		if owner.BlockID == "" || owner.Target != target {
			continue
		}
		if match.Key != "" && match.Key != owner.Key {
			return runningActivityOwner{}, false
		}
		match = owner
	}
	return match, match.Key != ""
}

func (t *runningHintTracker) targetForHandles(handles []string) runningActivityTarget {
	if len(handles) == 0 {
		return ""
	}
	t.ensure()
	target := t.ownersByHandle[normalizeRunningActivityHandle(handles[0])].Target
	if target == "" {
		return ""
	}
	for _, handle := range handles[1:] {
		if t.ownersByHandle[normalizeRunningActivityHandle(handle)].Target != target {
			return ""
		}
	}
	return target
}

func (t *runningHintTracker) removeOrderKey(key string) {
	for index := len(t.order) - 1; index >= 0; index-- {
		if t.order[index] != key {
			continue
		}
		t.order = append(t.order[:index], t.order[index+1:]...)
	}
}

func normalizeRunningActivityHandle(value string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "@"))
}

func sameTaskHandle(left string, right string) bool {
	normalized := normalizeRunningActivityHandle(left)
	return normalized != "" && normalized == normalizeRunningActivityHandle(right)
}
