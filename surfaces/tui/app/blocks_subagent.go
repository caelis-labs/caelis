package tuiapp

import (
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/caelis-labs/caelis/surfaces/transcript"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

// SubagentEventKind identifies the type of a child session event.
type SubagentEventKind int

const (
	SEAssistant SubagentEventKind = iota
	SEReasoning
	SEToolCall
	SEPlan
	SEApproval
	SENotice
)

// SubagentEvent is a single event in a subagent's chronological event stream.
type SubagentEvent struct {
	Kind SubagentEventKind

	// Assistant/Reasoning: accumulated text.
	Text       string
	NoticeKind transcript.NoticeKind
	StartedAt  time.Time
	EndedAt    time.Time

	// ActiveBuffer is transient UI state derived from Text for streaming
	// narrative rendering. It is not canonical session data.
	ActiveBuffer *activeNarrativeBuffer `json:"-"`

	// ToolCall fields.
	CallID   string
	Name     string
	ToolKind string
	Args     string
	// StartArgs keeps live exploration rows stable when final summaries arrive.
	StartArgs       string
	FullArgs        string
	Output          string
	OutputMessageID string
	OutputMessage   string
	// OutputNarrative marks text supplied by child ACP narrative chunks so a
	// repeated parent result cannot truncate already rendered child output.
	OutputNarrative bool
	// OutputNarrativeBoundary separates the next child assistant segment after
	// a child tool/plan barrier even when the optional ACP MessageID is absent.
	OutputNarrativeBoundary bool
	Terminal                bool
	OutputSynthetic         bool
	OutputTerminal          bool
	OutputGapBefore         bool
	// TaskHandle is presentation identity only. Runtime TaskID never enters a
	// transcript panel.
	TaskHandle     string
	TaskAction     string
	TaskInput      string
	TaskTargetKind string
	Done           bool
	Err            bool
	// Plan fields.
	PlanEntries []planEntryState

	// Approval fields (derived from context when status becomes waiting_approval).
	ApprovalTool    string
	ApprovalCommand string
	ApprovalStatus  string
	ApprovalRisk    string
	ApprovalAuth    string
	ApprovalText    string
}

func narrativeEventTime(values ...time.Time) time.Time {
	if len(values) > 0 {
		for _, value := range values {
			if !value.IsZero() {
				return value
			}
		}
		return time.Time{}
	}
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Now()
}

func markNarrativeTiming(ev *SubagentEvent, occurredAt time.Time) {
	if ev == nil || ev.Kind != SEReasoning || occurredAt.IsZero() {
		return
	}
	if ev.StartedAt.IsZero() || occurredAt.Before(ev.StartedAt) {
		ev.StartedAt = occurredAt
	}
	if ev.EndedAt.IsZero() || occurredAt.After(ev.EndedAt) {
		ev.EndedAt = occurredAt
	}
}

func closeLatestReasoningTiming(events []SubagentEvent, occurredAt time.Time) {
	if occurredAt.IsZero() {
		return
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind != SEReasoning {
			continue
		}
		markNarrativeTiming(&events[i], occurredAt)
		return
	}
}

func appendNarrativeEventChunk(ev *SubagentEvent, kind SubagentEventKind, chunk string, at time.Time, merge func(string, string) string) {
	if ev == nil {
		return
	}
	if merge == nil {
		merge = appendDeltaStreamChunk
	}
	text := normalizeNarrativeLineEndings(merge(ev.Text, chunk))
	ev.Kind = kind
	ev.Text = text
	if ev.ActiveBuffer == nil {
		ev.ActiveBuffer = &activeNarrativeBuffer{}
	}
	ev.ActiveBuffer.SetText(text)
	markNarrativeTiming(ev, at)
}

func newNarrativeEventChunk(kind SubagentEventKind, chunk string, at time.Time) SubagentEvent {
	ev := SubagentEvent{Kind: kind}
	appendNarrativeEventChunk(&ev, kind, chunk, at, appendDeltaStreamChunk)
	return ev
}

func replaceNarrativeEventFinal(ev *SubagentEvent, text string, at time.Time) {
	if ev == nil {
		return
	}
	ev.Text = normalizeNarrativeLineEndings(text)
	ev.ActiveBuffer = nil
	markNarrativeTiming(ev, at)
}

// ACP ingress owns transport framing and byte reassembly. Surface chunk merges
// preserve all received runes, including U+FEFF and U+FFFD.
func appendSubagentStreamDelta(existing string, incoming string) string {
	if incoming == "" {
		return existing
	}
	return existing + incoming
}

func mergeSubagentNarrativeChunk(existing string, existingMessageID string, existingMessage string, incoming string, incomingMessageID string) (string, string) {
	if incoming == "" {
		return existing, existingMessage
	}
	existingMessageID = strings.TrimSpace(existingMessageID)
	incomingMessageID = strings.TrimSpace(incomingMessageID)
	if existingMessageID != "" && incomingMessageID != "" && existingMessageID != incomingMessageID {
		return joinSubagentNarrativeMessages(existing, incoming), incoming
	}
	// ACP content chunks are deltas after ingress normalization. Preserve every
	// chunk, including identical adjacent deltas. Message identity only scopes
	// the current-message accumulator used when reconciling the parent result;
	// it must not turn the Surface into a second cumulative-stream normalizer.
	return existing + incoming, existingMessage + incoming
}

// joinSubagentNarrativeMessages preserves the semantic boundary between two
// distinct ACP assistant messages. Two line breaks keep Markdown blocks from
// collapsing together while retaining any boundary already supplied upstream.
func joinSubagentNarrativeMessages(existing string, incoming string) string {
	if existing == "" {
		return incoming
	}
	if incoming == "" {
		return existing
	}
	suffixNewline := strings.HasSuffix(existing, "\n")
	prefixNewline := strings.HasPrefix(incoming, "\n")
	switch {
	case suffixNewline && prefixNewline:
		return existing + incoming
	case suffixNewline || prefixNewline:
		return existing + "\n" + incoming
	default:
		return existing + "\n\n" + incoming
	}
}

func mergeCommandStreamChunk(existing string, incoming string) string {
	if incoming == "" {
		return existing
	}
	if existing == "" {
		return incoming
	}
	if overlap := commandLineOverlap(existing, incoming); overlap > 0 {
		return existing + incoming[overlap:]
	}
	if overlap := commandCumulativePrefixOverlap(existing, incoming); overlap > 0 {
		return existing + incoming[overlap:]
	}
	return existing + incoming
}

func mergeTerminalStreamChunk(existing string, incoming string) string {
	if incoming == "" {
		return existing
	}
	if existing == "" {
		return incoming
	}
	if incoming == existing {
		return existing
	}
	if strings.HasPrefix(incoming, existing) {
		return incoming
	}
	if strings.HasPrefix(existing, incoming) {
		return existing
	}
	if overlap := commandLineOverlap(existing, incoming); overlap > 0 {
		return existing + incoming[overlap:]
	}
	if overlap := commandCumulativePrefixOverlap(existing, incoming); overlap > 0 {
		return existing + incoming[overlap:]
	}
	return existing + incoming
}

func commandCumulativePrefixOverlap(existing string, incoming string) int {
	common := commonPrefixBytes(existing, incoming)
	if common == 0 || common >= len(incoming) {
		return 0
	}
	prefix := incoming[:common]
	if !strings.Contains(prefix, "\n") {
		return 0
	}
	if !strings.HasSuffix(prefix, "\n") {
		if idx := strings.LastIndex(prefix, "\n"); idx >= 0 {
			return idx + 1
		}
		return 0
	}
	return common
}

func commonPrefixBytes(left string, right string) int {
	max := min(len(left), len(right))
	idx := 0
	for idx < max && left[idx] == right[idx] {
		idx++
	}
	return idx
}

func appendDeltaStreamChunk(existing string, incoming string) string {
	if incoming == "" {
		return existing
	}
	if existing == "" {
		return incoming
	}
	return existing + incoming
}

func commandLineOverlap(existing string, incoming string) int {
	maxOverlap := minInt(len(existing), len(incoming))
	const maxSearch = 64 * 1024
	if maxOverlap > maxSearch {
		maxOverlap = maxSearch
	}
	start := len(existing) - maxOverlap
	for i := start; i < len(existing); i++ {
		if i > 0 && existing[i-1] != '\n' && existing[i-1] != '\r' {
			continue
		}
		suffix := existing[i:]
		if suffix == "" || (!strings.HasSuffix(suffix, "\n") && !strings.HasSuffix(suffix, "\r")) {
			continue
		}
		if strings.HasPrefix(incoming, suffix) {
			return len(suffix)
		}
	}
	return 0
}

func narrativeEventActive(events []SubagentEvent, idx int, terminal bool) bool {
	if terminal || idx < 0 || idx >= len(events) {
		return false
	}
	ev := events[idx]
	if ev.Kind != SEAssistant && ev.Kind != SEReasoning {
		return false
	}
	for j := idx + 1; j < len(events); j++ {
		if events[j].Kind == SEAssistant || events[j].Kind == SEReasoning {
			return false
		}
	}
	return true
}

func panelScrollWindow(total, visible, offset int, followTail bool) (start int, end int, maxOffset int) {
	if visible <= 0 {
		visible = 1
	}
	if total <= visible {
		return 0, total, 0
	}
	maxOffset = total - visible
	if followTail {
		offset = maxOffset
	} else {
		if offset < 0 {
			offset = 0
		}
		if offset > maxOffset {
			offset = maxOffset
		}
	}
	return offset, minInt(total, offset+visible), maxOffset
}

func canScrollPanelState(offset int, followTail bool, total, visible, delta int) bool {
	if delta == 0 {
		return false
	}
	_, _, maxOffset := panelScrollWindow(total, visible, offset, followTail)
	if maxOffset == 0 {
		return false
	}
	current := offset
	if followTail {
		current = maxOffset
	}
	next := current + delta
	next = max(next, 0)
	next = min(next, maxOffset)
	return next != current
}

func addScrollbar(lines []string, contentWidth, visible, offset, total int, theme tuikit.Theme, visibleNow bool) []string {
	if len(lines) == 0 || total <= visible || !visibleNow {
		return lines
	}
	thumbHeight := maxInt(1, visible*visible/maxInt(visible, total))
	maxStart := maxInt(0, visible-thumbHeight)
	thumbStart := 0
	if total > visible && maxStart > 0 {
		thumbStart = (offset * maxStart) / maxInt(1, total-visible)
	}
	withScrollbar := make([]string, len(lines))
	for i, line := range lines {
		glyph := theme.ScrollbarTrackStyle().Render("▏")
		if i >= thumbStart && i < thumbStart+thumbHeight {
			glyph = theme.ScrollbarThumbStyle().Render("▎")
		}
		if pad := contentWidth - lipgloss.Width(line); pad > 0 {
			line += strings.Repeat(" ", pad)
		}
		withScrollbar[i] = line + glyph
	}
	return withScrollbar
}

func scrollPanelState(offset *int, followTail *bool, total, visible, delta int) bool {
	if offset == nil || followTail == nil || delta == 0 {
		return false
	}
	_, _, maxOffset := panelScrollWindow(total, visible, *offset, *followTail)
	if maxOffset == 0 {
		return false
	}
	current := *offset
	if *followTail {
		current = maxOffset
	}
	next := current + delta
	next = max(next, 0)
	next = min(next, maxOffset)
	changed := next != current || *followTail != (next == maxOffset)
	*offset = next
	*followTail = next == maxOffset
	return changed
}
