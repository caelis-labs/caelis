package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"unicode"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool/identity"
)

// generationLoopDetector is a near-zero-cost stream-tail probe.
//
// It detects high-confidence generation loops only:
//   - text_loop: reasoning/assistant tail is an exact cycle repeated textStreak times
//   - tool_loop: consecutive tool steps share the same args and the same content segment
//
// Same tool with different segment content is treated as progress (not a loop).
// Empty/unusable tool args fail open (never name-only identity).
// Task wait is long-running work observation, so it resets tool-loop evidence
// and is never counted as repeated execution.
//
// This is not a wall-clock task timeout.
const (
	defaultTextLoopStreak  = 20 // pure text/reasoning tail (15–30 band)
	defaultToolLoopStreak  = 6  // content+tool or pure-tool steps (5–8 band)
	defaultMinContentRunes = 32
	defaultMaxTailRunes    = 4096
	defaultMinCycleRunes   = 24
	defaultMaxCycleRunes   = 256
	defaultMaxToolCallIDs  = 256
	emptyContentDigest     = "empty-content"
)

type generationLoopDetector struct {
	textStreak int
	toolStreak int
	minRunes   int

	// Rolling tail for pure text/reasoning cycle detection (rune-capped).
	tail []rune
	// Content since the previous tool_call (or start); compared across tool steps.
	segment []rune

	lastStepContent  string
	lastStepTool     string
	countedToolIDs   map[string]struct{}
	countedToolOrder []string
	stepStreak       int
}

type loopHit struct {
	Reason  WatchdogReason
	Streak  int
	HasTool bool
	Content string
	Tools   string
	Detail  string
}

func newGenerationLoopDetector(textStreak, toolStreak, minRunes int) *generationLoopDetector {
	if textStreak <= 0 {
		textStreak = defaultTextLoopStreak
	}
	if toolStreak <= 0 {
		toolStreak = defaultToolLoopStreak
	}
	if minRunes <= 0 {
		minRunes = defaultMinContentRunes
	}
	return &generationLoopDetector{textStreak: textStreak, toolStreak: toolStreak, minRunes: minRunes}
}

func (d *generationLoopDetector) observe(event *session.Event) (loopHit, bool) {
	if d == nil || event == nil {
		return loopHit{}, false
	}
	switch session.EventTypeOf(event) {
	case session.EventTypeAssistant:
		text := watchdogEventText(event)
		// Skip whitespace-only chunks, but never trim non-empty deltas: stream
		// boundaries often split on spaces (" model"); trimming would destroy cycles.
		if strings.TrimSpace(text) == "" {
			return loopHit{}, false
		}
		d.appendText(text)
		if hit, ok := d.checkTextTailLoop(); ok {
			return hit, true
		}
	case session.EventTypeToolCall:
		// Pure-text cycles never cross a tool boundary. The tool step itself is
		// evaluated separately using the content segment plus canonical args.
		d.tail = d.tail[:0]
		callID := watchdogToolCallID(event)
		if callID != "" && d.toolCallCounted(callID) {
			// ACP may emit several pending updates for one remote tool call. They
			// are one generation step, not repeated tool execution evidence.
			return loopHit{}, false
		}
		if watchdogTaskWait(event) {
			d.rememberToolCall(callID)
			d.segment = d.segment[:0]
			d.resetToolEvidence()
			return loopHit{}, false
		}
		sig := watchdogToolSignature(event)
		if sig == "" {
			d.segment = d.segment[:0]
			d.resetToolEvidence()
			return loopHit{}, false
		}
		d.rememberToolCall(callID)
		if hit, ok := d.noteToolStep(d.segmentDigest(), sig); ok {
			d.segment = d.segment[:0]
			return hit, true
		}
		// Next tool step only compares content produced after this call.
		d.segment = d.segment[:0]
	case session.EventTypeUser:
		d.resetAll()
	}
	return loopHit{}, false
}

func (d *generationLoopDetector) appendText(text string) {
	// Preserve stream deltas as emitted (no inserted spaces). Chunks already
	// carry their own whitespace; forcing separators breaks real token streams.
	runes := []rune(text)
	if len(runes) == 0 {
		return
	}
	d.tail = appendRunesCapped(d.tail, runes, defaultMaxTailRunes)
	d.segment = append(d.segment, runes...)
	if len(d.segment) > defaultMaxTailRunes {
		d.segment = append([]rune(nil), d.segment[len(d.segment)-defaultMaxTailRunes:]...)
	}
}

func appendRunesCapped(dst, add []rune, max int) []rune {
	if max <= 0 {
		return add
	}
	dst = append(dst, add...)
	if len(dst) > max {
		dst = append([]rune(nil), dst[len(dst)-max:]...)
	}
	return dst
}

func (d *generationLoopDetector) noteToolStep(contentDig, toolSig string) (loopHit, bool) {
	if contentDig == d.lastStepContent && toolSig == d.lastStepTool {
		d.stepStreak++
	} else {
		d.stepStreak = 1
		d.lastStepContent = contentDig
		d.lastStepTool = toolSig
	}
	if d.stepStreak < d.toolStreak {
		return loopHit{}, false
	}
	return loopHit{
		Reason:  WatchdogReasonToolLoop,
		Streak:  d.stepStreak,
		HasTool: true,
		Content: contentDig,
		Tools:   toolSig,
		Detail:  "identical content+tool tail",
	}, true
}

func (d *generationLoopDetector) checkTextTailLoop() (loopHit, bool) {
	n := len(d.tail)
	if n < d.minRunes*d.textStreak {
		return loopHit{}, false
	}
	maxP := defaultMaxCycleRunes
	if maxP > n/d.textStreak {
		maxP = n / d.textStreak
	}
	for p := defaultMinCycleRunes; p <= maxP; p++ {
		need := p * d.textStreak
		if need > n {
			break
		}
		block := d.tail[n-need:]
		cycle := block[len(block)-p:]
		matched := true
		for i := 0; i < d.textStreak; i++ {
			seg := block[i*p : (i+1)*p]
			if !runesEqual(seg, cycle) {
				matched = false
				break
			}
		}
		if matched {
			return loopHit{
				Reason:  WatchdogReasonTextLoop,
				Streak:  d.textStreak,
				HasTool: false,
				Content: hashLoopString(string(cycle)),
				Detail:  "reasoning/assistant stream tail cycle",
			}, true
		}
	}
	return loopHit{}, false
}

func runesEqual(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (d *generationLoopDetector) segmentDigest() string {
	if len(d.segment) == 0 {
		return emptyContentDigest
	}
	// Collapse only internal whitespace runs for stable comparison across
	// harmless formatting differences; do not invent separators between chunks.
	return hashLoopString(collapseSpace(string(d.segment)))
}

func (d *generationLoopDetector) resetAll() {
	d.tail = d.tail[:0]
	d.segment = d.segment[:0]
	clear(d.countedToolIDs)
	d.countedToolOrder = d.countedToolOrder[:0]
	d.resetToolEvidence()
}

func (d *generationLoopDetector) toolCallCounted(callID string) bool {
	if d == nil || strings.TrimSpace(callID) == "" || d.countedToolIDs == nil {
		return false
	}
	_, ok := d.countedToolIDs[strings.TrimSpace(callID)]
	return ok
}

func (d *generationLoopDetector) rememberToolCall(callID string) {
	if d == nil {
		return
	}
	callID = strings.TrimSpace(callID)
	if callID == "" || d.toolCallCounted(callID) {
		return
	}
	if d.countedToolIDs == nil {
		d.countedToolIDs = make(map[string]struct{}, defaultMaxToolCallIDs)
	}
	d.countedToolIDs[callID] = struct{}{}
	d.countedToolOrder = append(d.countedToolOrder, callID)
	if len(d.countedToolOrder) <= defaultMaxToolCallIDs {
		return
	}
	oldest := d.countedToolOrder[0]
	d.countedToolOrder = d.countedToolOrder[1:]
	delete(d.countedToolIDs, oldest)
}

func (d *generationLoopDetector) resetToolEvidence() {
	d.lastStepContent, d.lastStepTool = "", ""
	d.stepStreak = 0
}

func collapseSpace(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(text))
	prevSpace := false
	for _, r := range text {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

func hashLoopString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}

func watchdogEventText(event *session.Event) string {
	if event == nil {
		return ""
	}
	if event.Message != nil {
		if t := event.Message.TextContent(); t != "" {
			return t
		}
	}
	if event.Protocol != nil && event.Protocol.Update != nil {
		update := strings.TrimSpace(event.Protocol.Update.SessionUpdate)
		if update == string(session.ProtocolUpdateTypeAgentThought) || update == string(session.ProtocolUpdateTypeAgentMessage) {
			if t := protocolUpdateText(event.Protocol.Update.Content); t != "" {
				return t
			}
		}
	}
	return event.Text
}

func protocolUpdateText(content any) string {
	switch typed := content.(type) {
	case string:
		return typed
	case map[string]any:
		if t, ok := typed["text"].(string); ok {
			return t
		}
	}
	return ""
}

func watchdogToolSignature(event *session.Event) string {
	if event == nil {
		return ""
	}
	update := session.ProtocolUpdateOf(event)
	name := strings.ToUpper(strings.TrimSpace(session.CanonicalToolName(event, update)))
	if name == "" {
		return ""
	}
	input := watchdogToolInput(event, update)
	args, ok := canonicalToolArgs(input)
	if !ok {
		return ""
	}
	payload, err := json.Marshal(args)
	if err != nil || len(payload) == 0 || string(payload) == "null" || string(payload) == "{}" {
		return ""
	}
	sum := sha256.Sum256(append([]byte(name+"\x00"), payload...))
	return hex.EncodeToString(sum[:])
}

func watchdogTaskWait(event *session.Event) bool {
	if event == nil {
		return false
	}
	update := session.ProtocolUpdateOf(event)
	info, ok := identity.Lookup(session.CanonicalToolName(event, update))
	if !ok || info.Name != identity.Task {
		return false
	}
	action, _ := watchdogToolInput(event, update)["action"].(string)
	return strings.EqualFold(strings.TrimSpace(action), "wait")
}

func watchdogToolInput(event *session.Event, update *session.ProtocolUpdate) map[string]any {
	if event == nil {
		return nil
	}
	if event.Tool != nil {
		return event.Tool.Input
	}
	if update != nil {
		return update.RawInput
	}
	return nil
}

func watchdogToolCallID(event *session.Event) string {
	if event == nil {
		return ""
	}
	if event.Tool != nil {
		if id := strings.TrimSpace(event.Tool.ID); id != "" {
			return id
		}
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
		return strings.TrimSpace(update.ToolCallID)
	}
	return ""
}

func canonicalToolArgs(input map[string]any) (map[string]any, bool) {
	if len(input) == 0 {
		return nil, false
	}
	keys := make([]string, 0, len(input))
	for k := range input {
		if strings.TrimSpace(k) == "" {
			continue
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil, false
	}
	sort.Strings(keys)
	out := make(map[string]any, len(keys))
	for _, k := range keys {
		out[k] = input[k]
	}
	return out, true
}
