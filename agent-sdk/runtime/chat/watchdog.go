package chat

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
)

const (
	defaultTextLoopStreak  = 20
	defaultToolLoopStreak  = 6
	defaultMinContentRunes = 32
	defaultMaxTailRunes    = 4096
	defaultMinCycleRunes   = 24
	defaultMaxCycleRunes   = 256
	emptyContentDigest     = "empty-content"

	agentWatchdogCheckpointStatus = "agent_loop_watchdog_checkpoint"
)

// GenerationLoopReason identifies one high-confidence loop in the raw,
// provider-neutral output of a local chat Agent.
type GenerationLoopReason string

const (
	GenerationTextLoop GenerationLoopReason = "text_loop"
	GenerationToolLoop GenerationLoopReason = "tool_loop"
)

// GenerationLoopError reports that a local chat Agent stopped its own model
// loop before another repeated generation step could execute.
type GenerationLoopError struct {
	Reason        GenerationLoopReason
	Streak        int
	HasTool       bool
	ContentDigest string
	ToolDigest    string
	Detail        string
}

func (e *GenerationLoopError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("generation loop interrupted: %s (streak %d)", e.Reason, e.Streak)
}

func (*GenerationLoopError) ErrorCode() errorcode.Code { return errorcode.Interrupted }

// generationWatchdog belongs to one local Agent.Run invocation. It consumes
// provider-neutral model stream events before they become Runtime or ACP
// events. Control and external ACP Agent output must never pass through it.
type generationWatchdog struct {
	textStreak int
	toolStreak int
	minRunes   int

	tail    []rune
	segment []rune

	lastStepContent string
	lastStepTool    string
	stepStreak      int
	sawStepDelta    bool
}

func newGenerationWatchdog(textStreak, toolStreak, minRunes int) *generationWatchdog {
	if textStreak <= 0 {
		textStreak = defaultTextLoopStreak
	}
	if toolStreak <= 0 {
		toolStreak = defaultToolLoopStreak
	}
	if minRunes <= 0 {
		minRunes = defaultMinContentRunes
	}
	return &generationWatchdog{
		textStreak: textStreak,
		toolStreak: toolStreak,
		minRunes:   minRunes,
	}
}

func newDefaultGenerationWatchdog() *generationWatchdog {
	return newGenerationWatchdog(defaultTextLoopStreak, defaultToolLoopStreak, defaultMinContentRunes)
}

func (d *generationWatchdog) beginModelStep() {
	if d == nil {
		return
	}
	d.sawStepDelta = false
}

func (d *generationWatchdog) observeStreamEvent(event *model.StreamEvent) error {
	if d == nil || event == nil {
		return nil
	}
	if event.Type == model.StreamEventAttemptReset {
		d.resetAll()
		return nil
	}
	if event.PartDelta == nil {
		return nil
	}
	switch event.PartDelta.Kind {
	case model.PartKindReasoning, model.PartKindText:
	default:
		return nil
	}
	if strings.TrimSpace(event.PartDelta.TextDelta) == "" {
		return nil
	}
	d.sawStepDelta = true
	return d.observeText(event.PartDelta.TextDelta)
}

func (d *generationWatchdog) finishModelStep(resp *model.Response) error {
	if d == nil || resp == nil {
		return nil
	}
	if !d.sawStepDelta {
		for _, part := range resp.Message.Parts {
			var text string
			switch {
			case part.Reasoning != nil && part.Reasoning.VisibleText != nil:
				text = *part.Reasoning.VisibleText
			case part.Text != nil:
				text = part.Text.Text
			}
			if strings.TrimSpace(text) == "" {
				continue
			}
			if err := d.observeText(text); err != nil {
				return err
			}
		}
	}

	calls := resp.Message.ToolCalls()
	if len(calls) == 0 {
		d.segment = d.segment[:0]
		d.resetToolEvidence()
		return nil
	}
	d.tail = d.tail[:0]
	if modelCallsAreOnlyTaskWaits(calls) {
		d.segment = d.segment[:0]
		d.resetToolEvidence()
		return nil
	}
	toolDigest, ok := modelToolStepDigest(calls)
	if !ok {
		d.segment = d.segment[:0]
		d.resetToolEvidence()
		return nil
	}
	contentDigest := modelMessageContentDigest(resp.Message)
	if contentDigest == emptyContentDigest {
		contentDigest = d.segmentDigest()
	}
	d.segment = d.segment[:0]
	if contentDigest == d.lastStepContent && toolDigest == d.lastStepTool {
		d.stepStreak++
	} else {
		d.stepStreak = 1
		d.lastStepContent = contentDigest
		d.lastStepTool = toolDigest
	}
	if d.stepStreak < d.toolStreak {
		return nil
	}
	return &GenerationLoopError{
		Reason:        GenerationToolLoop,
		Streak:        d.stepStreak,
		HasTool:       true,
		ContentDigest: contentDigest,
		ToolDigest:    toolDigest,
		Detail:        "identical raw model content+tool step",
	}
}

func (d *generationWatchdog) observeText(text string) error {
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}
	d.tail = appendRunesCapped(d.tail, runes, defaultMaxTailRunes)
	d.segment = appendRunesCapped(d.segment, runes, defaultMaxTailRunes)
	return d.textLoopError()
}

func (d *generationWatchdog) textLoopError() error {
	n := len(d.tail)
	if n < d.minRunes*d.textStreak {
		return nil
	}
	maxPeriod := min(defaultMaxCycleRunes, n/d.textStreak)
	for period := defaultMinCycleRunes; period <= maxPeriod; period++ {
		need := period * d.textStreak
		block := d.tail[n-need:]
		cycle := block[len(block)-period:]
		matched := true
		for i := range d.textStreak {
			if !runesEqual(block[i*period:(i+1)*period], cycle) {
				matched = false
				break
			}
		}
		if matched {
			return &GenerationLoopError{
				Reason:        GenerationTextLoop,
				Streak:        d.textStreak,
				ContentDigest: hashLoopString(string(cycle)),
				Detail:        "raw reasoning/assistant stream tail cycle",
			}
		}
	}
	return nil
}

func (d *generationWatchdog) segmentDigest() string {
	if len(d.segment) == 0 {
		return emptyContentDigest
	}
	return hashLoopString(collapseSpace(string(d.segment)))
}

func (d *generationWatchdog) resetAll() {
	d.tail = d.tail[:0]
	d.segment = d.segment[:0]
	d.sawStepDelta = false
	d.resetToolEvidence()
}

func (d *generationWatchdog) resetToolEvidence() {
	d.lastStepContent = ""
	d.lastStepTool = ""
	d.stepStreak = 0
}

func modelCallsAreOnlyTaskWaits(calls []model.ToolCall) bool {
	if len(calls) == 0 {
		return false
	}
	for _, call := range calls {
		if call.Name != tasktool.ToolName {
			return false
		}
		args, ok := decodeComparableToolArgs(call.Args)
		if !ok {
			return false
		}
		action, _ := args["action"].(string)
		if !strings.EqualFold(strings.TrimSpace(action), "wait") {
			return false
		}
	}
	return true
}

func modelMessageContentDigest(message model.Message) string {
	var content strings.Builder
	for _, part := range message.Parts {
		var (
			kind model.PartKind
			text string
		)
		switch {
		case part.Reasoning != nil && part.Reasoning.VisibleText != nil:
			kind = model.PartKindReasoning
			text = *part.Reasoning.VisibleText
		case part.Text != nil:
			kind = model.PartKindText
			text = part.Text.Text
		default:
			continue
		}
		text = collapseSpace(text)
		if text == "" {
			continue
		}
		fmt.Fprintf(&content, "%s:%d:%s\n", kind, len([]rune(text)), text)
	}
	if content.Len() == 0 {
		return emptyContentDigest
	}
	return hashLoopString(content.String())
}

func modelToolStepDigest(calls []model.ToolCall) (string, bool) {
	if len(calls) == 0 {
		return "", false
	}
	steps := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		name := strings.TrimSpace(call.Name)
		if name == "" {
			return "", false
		}
		args, ok := decodeComparableToolArgs(call.Args)
		if !ok {
			return "", false
		}
		steps = append(steps, map[string]any{"name": name, "args": args})
	}
	payload, err := json.Marshal(steps)
	if err != nil || len(payload) == 0 {
		return "", false
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), true
}

func decodeComparableToolArgs(raw string) (map[string]any, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || !json.Valid([]byte(raw)) {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.UseNumber()
	var decoded map[string]any
	if err := decoder.Decode(&decoded); err != nil || len(decoded) == 0 {
		return nil, false
	}
	keys := make([]string, 0, len(decoded))
	for key := range decoded {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil, false
	}
	sort.Strings(keys)
	canonical := make(map[string]any, len(keys))
	for _, key := range keys {
		canonical[key] = decoded[key]
	}
	return canonical, true
}

func generationLoopEvent(loopErr *GenerationLoopError) *session.Event {
	if loopErr == nil {
		return nil
	}
	return &session.Event{
		Type:       session.EventTypeLifecycle,
		Visibility: session.VisibilityJournal,
		Actor:      session.ActorRef{Kind: session.ActorKindSystem, Name: "agent-watchdog"},
		Lifecycle: &session.EventLifecycle{
			Status: agentWatchdogCheckpointStatus,
			Reason: loopErr.Error(),
			Meta: map[string]any{
				"reason":         string(loopErr.Reason),
				"loop_streak":    loopErr.Streak,
				"loop_has_tool":  loopErr.HasTool,
				"content_digest": loopErr.ContentDigest,
				"tool_digest":    loopErr.ToolDigest,
				"loop_detail":    loopErr.Detail,
			},
		},
	}
}

func appendRunesCapped(dst, add []rune, maxRunes int) []rune {
	dst = append(dst, add...)
	if maxRunes > 0 && len(dst) > maxRunes {
		return append([]rune(nil), dst[len(dst)-maxRunes:]...)
	}
	return dst
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

func collapseSpace(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(text))
	previousSpace := false
	for _, r := range text {
		if unicode.IsSpace(r) {
			if !previousSpace {
				b.WriteByte(' ')
				previousSpace = true
			}
			continue
		}
		previousSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

func hashLoopString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}
