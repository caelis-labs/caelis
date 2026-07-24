package tuiapp

import (
	"strconv"
	"strings"
	"time"
)

type runningActivityPhase string

const (
	runningPhaseThinking   runningActivityPhase = "thinking"
	runningPhaseResponding runningActivityPhase = "responding"
	runningPhaseWait       runningActivityPhase = "wait"
	runningPhaseRead       runningActivityPhase = "read"
	runningPhaseCancel     runningActivityPhase = "cancel"
	runningPhaseReview     runningActivityPhase = "review"
	runningPhaseInterrupt  runningActivityPhase = "interrupt"
)

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

// runningActivityTracker owns the live hint projection. Tool invocations are
// independently keyed activities: an observer such as Task wait can finish
// without finishing the Spawn or RunCommand owner it observes. Completed keys
// are retained for the Session so late non-terminal projections cannot revive
// an already closed activity.
type runningActivityTracker struct {
	focus          runningActivityState
	active         map[string]runningActivityState
	order          []string
	completed      map[string]struct{}
	overlay        runningActivityState
	ownersByHandle map[string]runningActivityOwner
	ownersByCallID map[string][]runningActivityOwner
	turnGeneration uint64
	turnStartedAt  time.Time
}

func newRunningActivityTracker() runningActivityTracker {
	return runningActivityTracker{
		active:         map[string]runningActivityState{},
		completed:      map[string]struct{}{},
		ownersByHandle: map[string]runningActivityOwner{},
		ownersByCallID: map[string][]runningActivityOwner{},
	}
}

func (t *runningActivityTracker) ensure() {
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

func (t *runningActivityTracker) beginTurn(startedAt time.Time) {
	t.ensure()
	clear(t.active)
	t.order = t.order[:0]
	t.overlay = runningActivityState{}
	t.focus = runningActivityState{Phase: runningPhaseThinking}
	t.turnGeneration++
	t.turnStartedAt = startedAt
}

func (t *runningActivityTracker) endTurn() {
	t.ensure()
	clear(t.active)
	t.order = t.order[:0]
	t.overlay = runningActivityState{}
	t.focus = runningActivityState{}
	t.turnStartedAt = time.Time{}
}

func (t *runningActivityTracker) resetSession() {
	*t = newRunningActivityTracker()
}

func (t *runningActivityTracker) setFocus(phase runningActivityPhase, target runningActivityTarget, key string) {
	if phase == "" {
		return
	}
	t.focus = runningActivityState{
		Phase:  phase,
		Target: target,
		Key:    strings.TrimSpace(key),
	}
}

func (t *runningActivityTracker) start(
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
		Phase:  phase,
		Target: target,
		Key:    key,
		CallID: strings.TrimSpace(callID),
	}
	if phase.showsElapsed() {
		entry.StartedAt = now
	}
	if previous, exists := t.active[key]; exists {
		if previous.Phase == phase && !previous.StartedAt.IsZero() {
			entry.StartedAt = previous.StartedAt
		}
		t.removeOrderKey(key)
	}
	t.active[key] = entry
	t.order = append(t.order, key)
}

func (t *runningActivityTracker) complete(key string) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	t.ensure()
	t.completed[key] = struct{}{}
	delete(t.active, key)
	t.removeOrderKey(key)
}

func (t *runningActivityTracker) setOverlay(phase runningActivityPhase, key string, now time.Time) {
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

func (t *runningActivityTracker) clearOverlay(key string) {
	key = strings.TrimSpace(key)
	if key == "" || t.overlay.Key == key {
		t.overlay = runningActivityState{}
	}
}

func (t *runningActivityTracker) visible(turnRunning bool) runningActivityState {
	if t.overlay.Phase != "" {
		return t.overlay
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
		return runningActivityState{Phase: runningPhaseThinking}
	}
	return runningActivityState{}
}

func (t *runningActivityTracker) observeOwner(handle string, owner runningActivityOwner) {
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
func (t *runningActivityTracker) toolKey(turnID string, callID string, occurredAt time.Time) string {
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

func (t *runningActivityTracker) observedOwnerCandidates(handle string, parentCallID string) []runningActivityOwner {
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
// normalized handle/call index used by running activity. When both identities
// are present they must agree; handle-free compatibility input is accepted only
// when the parent call has one compatible rendered owner.
func (t *runningActivityTracker) presentationOwner(
	handle string,
	parentCallID string,
	target runningActivityTarget,
) (runningActivityOwner, bool) {
	handle = normalizeRunningActivityHandle(handle)
	parentCallID = strings.TrimSpace(parentCallID)
	t.ensure()
	if handle != "" {
		owner := t.ownersByHandle[handle]
		if owner.Key == "" || owner.BlockID == "" || owner.Target != target {
			return runningActivityOwner{}, false
		}
		if parentCallID != "" && owner.CallID != parentCallID {
			return runningActivityOwner{}, false
		}
		return owner, true
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

func (t *runningActivityTracker) targetForHandles(handles []string) runningActivityTarget {
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

func (t *runningActivityTracker) removeOrderKey(key string) {
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
