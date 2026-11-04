package tuiapp

import (
	"strconv"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
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

// runningFocusOrderKey marks foreground model progress in the existing recency
// list. Older tools remain indexed for real progress/settlement, but cannot
// reappear solely because a newer foreground tool completed.
const runningFocusOrderKey = "model-focus"

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
	Key        string
	CallID     string
	Handle     string
	TerminalID string
	BlockID    string
	Target     runningActivityTarget
}

// runningHintTracker owns the TUI-only live hint projection derived from ACP
// updates. Tool invocations remain independently keyed activities, so an
// observer such as Task wait can finish without finishing the Spawn or
// RunCommand owner it observes. Completed keys are retained for the Session so
// late non-terminal projections cannot revive an already closed activity.
type runningHintTracker struct {
	focus                       runningActivityState
	focusForeground             bool
	foregroundKey               string
	active                      map[string]runningActivityState
	order                       []string
	completed                   map[string]struct{}
	compact                     runningActivityState
	overlay                     runningActivityState
	ownersByHandle              map[string]runningActivityOwner
	ownersByCallID              map[string][]runningActivityOwner
	turnGeneration              uint64
	turnStartedAt               time.Time
	turnID                      string
	retiredTurns                map[string]struct{}
	activityAt                  time.Time
	activityDurable             *eventstream.DurableFeedPosition
	activityTransientGeneration string
	activityTransientSequence   uint64
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
	if t.retiredTurns == nil {
		t.retiredTurns = map[string]struct{}{}
	}
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
	if t.turnID != "" {
		t.retiredTurns[t.turnID] = struct{}{}
	}
	t.turnID = ""
	t.activityAt = time.Time{}
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
	if t.turnID != "" {
		t.retiredTurns[t.turnID] = struct{}{}
	}
	t.turnID = ""
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
	t.compact = runningActivityState{}
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
	t.removeOrderKey(runningFocusOrderKey)
	t.order = append(t.order, runningFocusOrderKey)
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
	t.compact = runningActivityState{}
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
	if t.latestCurrentActivity().Phase == "" && !t.focusForeground {
		t.setModelWait(now)
	}
}

// completeTool closes only the original Turn/call identity. Cross-Turn Task
// observations must first resolve their typed parent relation and handle.
func (t *runningHintTracker) completeTool(key string, callID string, now time.Time) {
	t.complete(key, now)
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return
	}
	// A canonical result also settles the presentation of this exact review,
	// even when its transient review-ended delivery was lost.
	if strings.HasPrefix(key, "tool:") {
		reviewKey := "approval:" + strings.TrimPrefix(key, "tool:")
		if entry, active := t.active[reviewKey]; active && entry.Phase == runningPhaseReview {
			t.complete(reviewKey, now)
		}
	}
}

func (t *runningHintTracker) observedToolOwner(callID, handle, terminalID string) (runningActivityOwner, bool) {
	handle = normalizeRunningActivityHandle(handle)
	// Prefer the Task handle. A command may expose only its standard ACP
	// terminal identity before its first yielded result; that original identity
	// is also usable, but never overrides conflicting known Task handles.
	for _, byHandle := range []bool{true, false} {
		var found runningActivityOwner
		for _, owner := range t.ownersByCallID[callID] {
			match := sameTaskHandle(owner.Handle, handle)
			if !byHandle {
				match = terminalID != "" && owner.TerminalID == terminalID && (handle == "" || owner.Handle == "")
			}
			if !match || (terminalID != "" && owner.TerminalID != "" && owner.TerminalID != terminalID) {
				continue
			}
			if found.Key != "" && found.Key != owner.Key {
				return runningActivityOwner{}, false
			}
			found = owner
		}
		if found.Key != "" {
			return found, true
		}
	}
	return runningActivityOwner{}, false
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
	if !alreadyCompacting && t.latestCurrentActivity().Phase == "" {
		// Replace the pre-compaction model phase with the expected retry wait.
		// Later foreground events still advance beneath the compact boundary.
		t.setModelWait(now)
	}
}

// completeCompact closes a still-current compact hint. Later foreground actions
// already superseded it, so a delayed notice cannot change their phase clock.
func (t *runningHintTracker) completeCompact(now time.Time) {
	if t.compact.Phase != runningPhaseCompact {
		return
	}
	t.compact = runningActivityState{}
	if t.focusForeground && t.focus.Phase != "" && t.focus.Phase != runningPhaseModelWait {
		return
	}
	if t.latestCurrentActivity().Phase != "" && !t.focusForeground {
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
	t.removeOrderKey(runningFocusOrderKey)
	t.order = append(t.order, runningFocusOrderKey)
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
	if !turnRunning {
		return runningActivityState{}
	}
	if t.overlay.Phase != "" {
		return t.overlay
	}
	for index := len(t.order) - 1; index >= 0; index-- {
		if entry, ok := t.active[t.order[index]]; ok && entry.Phase == runningPhaseReview {
			return entry
		}
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
	if current := t.latestCurrentActivity(); current.Phase != "" {
		return current
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

func (t *runningHintTracker) latestCurrentActivity() runningActivityState {
	for index := len(t.order) - 1; index >= 0; index-- {
		key := t.order[index]
		if key == runningFocusOrderKey {
			break
		}
		if entry, ok := t.active[key]; ok {
			return entry
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
	if current.TerminalID == "" {
		current.TerminalID = previous.TerminalID
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
