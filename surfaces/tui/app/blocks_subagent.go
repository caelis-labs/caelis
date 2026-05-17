package tuiapp

import (
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/x/ansi"
)

// SubagentEventKind identifies the type of a child session event.
type SubagentEventKind int

const (
	SEAssistant SubagentEventKind = iota
	SEReasoning
	SEToolCall
	SEPlan
	SEApproval
)

// SubagentEvent is a single event in a subagent's chronological event stream.
type SubagentEvent struct {
	Kind SubagentEventKind

	// Assistant/Reasoning: accumulated text.
	Text      string
	StartedAt time.Time
	EndedAt   time.Time

	// ToolCall fields.
	CallID          string
	Name            string
	ToolKind        string
	Args            string
	FullArgs        string
	Output          string
	OutputSynthetic bool
	TaskID          string
	TaskAction      string
	TaskInput       string
	TaskTargetKind  string
	Done            bool
	Err             bool
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

type SubagentSessionState struct {
	SpawnID   string
	AttachID  string
	Agent     string
	Status    string // "running", "completed", "failed", "interrupted", "timed_out", "waiting_approval"
	StartedAt time.Time
	Events    []SubagentEvent

	// eventsGen is bumped on every Events mutation. Panels use it to
	// detect staleness without reflect.DeepEqual.
	eventsGen      uint64
	toolEventIndex map[string]int
}

func NewSubagentSessionState(spawnID, attachID, agent string) *SubagentSessionState {
	return &SubagentSessionState{
		SpawnID:        strings.TrimSpace(spawnID),
		AttachID:       strings.TrimSpace(attachID),
		Agent:          strings.TrimSpace(agent),
		Status:         "running",
		StartedAt:      time.Now(),
		toolEventIndex: map[string]int{},
	}
}

func (s *SubagentSessionState) AppendStreamChunk(kind SubagentEventKind, chunk string, occurredAt ...time.Time) {
	if s == nil {
		return
	}
	chunk = tuikit.SanitizeLogText(chunk)
	if chunk == "" {
		return
	}
	at := narrativeEventTime(occurredAt...)
	if idx := latestNarrativeAppendTargetIndex(s.Events, kind); idx >= 0 {
		s.Events[idx].Text = collapseRepeatedNarrativeText(mergeSubagentStreamChunk(s.Events[idx].Text, chunk))
		markNarrativeTiming(&s.Events[idx], at)
		s.eventsGen++
		return
	}
	ev := SubagentEvent{Kind: kind, Text: collapseRepeatedNarrativeText(chunk)}
	markNarrativeTiming(&ev, at)
	s.Events = append(s.Events, ev)
	s.eventsGen++
}

func (s *SubagentSessionState) ReplaceFinalStreamChunk(kind SubagentEventKind, chunk string, occurredAt ...time.Time) {
	if s == nil {
		return
	}
	chunk = tuikit.SanitizeLogText(chunk)
	if strings.TrimSpace(chunk) == "" {
		return
	}
	at := narrativeEventTime(occurredAt...)
	if idx := latestNarrativeFinalTargetIndex(s.Events, kind); idx >= 0 {
		s.Events[idx].Text = collapseRepeatedNarrativeText(chunk)
		markNarrativeTiming(&s.Events[idx], at)
		s.Events = pruneNarrativeEventsCoveredByFinal(s.Events, idx, kind)
		s.eventsGen++
		return
	}
	ev := SubagentEvent{Kind: kind, Text: collapseRepeatedNarrativeText(chunk)}
	markNarrativeTiming(&ev, at)
	s.Events = append(s.Events, ev)
	s.Events = pruneNarrativeEventsCoveredByFinal(s.Events, len(s.Events)-1, kind)
	s.eventsGen++
}

func (s *SubagentSessionState) UpdateToolCall(callID, toolName, args, stream, chunk string, final bool) {
	s.UpdateToolCallWithMeta(callID, toolName, args, stream, chunk, final, ToolUpdateMeta{})
}

func (s *SubagentSessionState) UpdateToolCallWithMeta(callID, toolName, args, stream, chunk string, final bool, meta ToolUpdateMeta) {
	if s == nil {
		return
	}
	stream = strings.ToLower(strings.TrimSpace(stream))
	chunk = normalizeSubagentChunkBoundary("", chunk)
	events, changed, _ := applyToolEventUpdate(s.Events, toolEventUpdate{
		CallID:          callID,
		Name:            toolName,
		Args:            args,
		Output:          chunk,
		Final:           final,
		Err:             stream == "stderr",
		Meta:            meta,
		SkipErroredOpen: true,
	}, s.toolEventIndex)
	s.Events = events
	if changed {
		s.eventsGen++
	}
}

func (s *SubagentSessionState) UpdatePlan(entries []planEntryState) {
	if s == nil {
		return
	}
	if n := len(s.Events); n > 0 && s.Events[n-1].Kind == SEPlan {
		s.Events[n-1].PlanEntries = entries
		s.eventsGen++
		return
	}
	s.Events = append(s.Events, SubagentEvent{
		Kind:        SEPlan,
		PlanEntries: entries,
	})
	s.eventsGen++
}

func (s *SubagentSessionState) AddApprovalEvent(tool, command string) {
	if s == nil {
		return
	}
	if tool == "" {
		for i := len(s.Events) - 1; i >= 0; i-- {
			e := &s.Events[i]
			if e.Kind == SEToolCall && !e.Done {
				tool = e.Name
				command = e.Args
				break
			}
		}
	}
	if n := len(s.Events); n > 0 && s.Events[n-1].Kind == SEApproval {
		s.Events[n-1].ApprovalTool = tool
		s.Events[n-1].ApprovalCommand = command
		s.eventsGen++
		return
	}
	s.Events = append(s.Events, SubagentEvent{
		Kind:            SEApproval,
		ApprovalTool:    tool,
		ApprovalCommand: command,
	})
	s.eventsGen++
}

func (s *SubagentSessionState) AddApprovalReviewEvent(callID, tool, command, status, risk, authorization, text string) {
	if s == nil {
		return
	}
	updated, changed := addApprovalReviewSubagentEvent(s.Events, callID, tool, command, status, risk, authorization, text)
	if changed {
		s.Events = updated
		s.eventsGen++
	}
}

func (s *SubagentSessionState) ReviveFromTerminal() {
	if s == nil || !isTerminalSubagentState(s.Status) {
		return
	}
	s.Status = "running"
	filtered := s.Events[:0]
	changed := false
	for _, ev := range s.Events {
		if ev.Kind == SEToolCall &&
			ev.Done &&
			ev.Err &&
			strings.Contains(strings.ToLower(strings.TrimSpace(ev.Output)), "interrupted before completion") &&
			(strings.EqualFold(strings.TrimSpace(ev.Name), "SPAWN") || strings.EqualFold(strings.TrimSpace(ev.Name), "TASK")) {
			changed = true
			continue
		}
		filtered = append(filtered, ev)
	}
	s.Events = filtered
	if changed {
		s.eventsGen++
	}
}

type SubagentPanelBlock struct {
	id                    string
	session               *SubagentSessionState
	localEvtGen           uint64 // tracks which session eventsGen was last copied
	SpawnID               string
	AttachID              string
	Agent                 string
	CallID                string
	Status                string // "running", "completed", "failed", "interrupted", "timed_out", "waiting_approval"
	StartedAt             time.Time
	Expanded              bool
	CollapseAt            time.Time
	CollapseFrom          time.Time
	CollapseFor           time.Duration
	VisibleLines          int
	ScrollOffset          int
	FollowTail            bool
	Terminal              bool
	ScrollbarVisibleUntil time.Time

	// PinnedOpenByUser is set when a terminal inline panel is manually
	// reopened from its anchor. That suppresses future auto-collapse until
	// the session resumes active work.
	PinnedOpenByUser bool

	// Events is the chronological stream of child session events.
	Events []SubagentEvent

	toolPanelRenderCache map[string]toolOutputRenderCache
}

func NewSubagentPanelBlock(spawnID, attachID, agent, callID string) *SubagentPanelBlock {
	return &SubagentPanelBlock{
		id:          nextBlockID(),
		SpawnID:     spawnID,
		AttachID:    attachID,
		Agent:       agent,
		CallID:      callID,
		Status:      "running",
		StartedAt:   time.Now(),
		Expanded:    true,
		CollapseFor: inlinePanelCollapseDuration,
		FollowTail:  true,
	}
}

func (b *SubagentPanelBlock) sessionState() *SubagentSessionState {
	if b == nil {
		return nil
	}
	if b.session == nil {
		state := NewSubagentSessionState(b.SpawnID, b.AttachID, b.Agent)
		state.Status = strings.TrimSpace(b.Status)
		if state.Status == "" {
			state.Status = "running"
		}
		if !b.StartedAt.IsZero() {
			state.StartedAt = b.StartedAt
		}
		state.Events = append(state.Events, b.Events...)
		state.eventsGen++
		b.session = state
		b.localEvtGen = state.eventsGen
		return state
	}
	b.syncMirrorIntoSession()
	return b.session
}

func (b *SubagentPanelBlock) bindSession(state *SubagentSessionState) {
	if b == nil || state == nil {
		return
	}
	b.session = state
	b.syncSessionMirror()
}

func (b *SubagentPanelBlock) syncMirrorIntoSession() {
	if b == nil || b.session == nil {
		return
	}
	if strings.TrimSpace(b.SpawnID) != "" && b.SpawnID != b.session.SpawnID {
		b.session.SpawnID = b.SpawnID
	}
	if strings.TrimSpace(b.AttachID) != "" && b.AttachID != b.session.AttachID {
		b.session.AttachID = b.AttachID
	}
	if strings.TrimSpace(b.Agent) != "" && b.Agent != b.session.Agent {
		b.session.Agent = b.Agent
	}
	if strings.TrimSpace(b.Status) != "" && b.Status != b.session.Status {
		b.session.Status = b.Status
	}
	if !b.StartedAt.IsZero() && !b.StartedAt.Equal(b.session.StartedAt) {
		b.session.StartedAt = b.StartedAt
	}
	if b.localEvtGen != b.session.eventsGen || len(b.Events) != len(b.session.Events) {
		b.session.Events = append(b.session.Events[:0], b.Events...)
		b.session.eventsGen++
		b.localEvtGen = b.session.eventsGen
	}
}

func (b *SubagentPanelBlock) syncSessionMirror() {
	if b == nil || b.session == nil {
		return
	}
	state := b.session
	b.SpawnID = state.SpawnID
	b.AttachID = state.AttachID
	b.Agent = state.Agent
	b.Status = state.Status
	b.StartedAt = state.StartedAt
	if b.localEvtGen != state.eventsGen {
		b.Events = append(b.Events[:0], state.Events...)
		b.localEvtGen = state.eventsGen
	}
}

// AppendStreamChunk appends a streaming text chunk (assistant or reasoning).
// If the most recent event is the same kind, the chunk is concatenated;
// otherwise a new event is created, preserving chronological ordering.
func (b *SubagentPanelBlock) AppendStreamChunk(kind SubagentEventKind, chunk string, occurredAt ...time.Time) {
	state := b.sessionState()
	state.AppendStreamChunk(kind, chunk, occurredAt...)
	b.syncSessionMirror()
}

func (b *SubagentPanelBlock) ReplaceFinalStreamChunk(kind SubagentEventKind, chunk string, occurredAt ...time.Time) {
	state := b.sessionState()
	state.ReplaceFinalStreamChunk(kind, chunk, occurredAt...)
	b.syncSessionMirror()
}

func mergeSubagentStreamChunk(existing string, incoming string) string {
	incoming = normalizeSubagentChunkBoundary(existing, incoming)
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
	return existing + incoming
}

func appendDeltaStreamChunk(existing string, incoming string) string {
	incoming = normalizeSubagentChunkBoundary(existing, incoming)
	if incoming == "" {
		return existing
	}
	if existing == "" {
		return incoming
	}
	return existing + incoming
}

func normalizeSubagentChunkBoundary(existing string, incoming string) string {
	if incoming == "" {
		return ""
	}
	if existing == "" {
		return strings.TrimLeft(incoming, "\uFEFF")
	}
	// Some upstream streaming paths occasionally surface a replacement-rune
	// prefix at chunk boundaries when a multibyte rune was split mid-update.
	// Keep the fix narrow: only trim leading U+FFFD/FEFF on continuation chunks.
	incoming = strings.TrimLeft(incoming, "\uFFFD\uFEFF")
	return incoming
}

// UpdateToolCall creates or updates a tool call event identified by callID.
func (b *SubagentPanelBlock) UpdateToolCall(callID, toolName, args, stream, chunk string, final bool) {
	b.UpdateToolCallWithMeta(callID, toolName, args, stream, chunk, final, ToolUpdateMeta{})
}

func (b *SubagentPanelBlock) UpdateToolCallWithMeta(callID, toolName, args, stream, chunk string, final bool, meta ToolUpdateMeta) {
	state := b.sessionState()
	state.UpdateToolCallWithMeta(callID, toolName, args, stream, chunk, final, meta)
	b.syncSessionMirror()
}

// UpdatePlan appends a new plan event or coalesces with the last event if it
// is also a plan (rapid consecutive plan updates). This preserves the
// chronological interleaving: tool→plan→tool→plan shows two plan snapshots.
func (b *SubagentPanelBlock) UpdatePlan(entries []planEntryState) {
	state := b.sessionState()
	state.UpdatePlan(entries)
	b.syncSessionMirror()
}

// AddApprovalEvent appends an approval event or coalesces with the last event
// if it is also an approval (rapid consecutive status updates). This preserves
// the chronological interleaving for multiple approval cycles.
func (b *SubagentPanelBlock) AddApprovalEvent(tool, command string) {
	state := b.sessionState()
	state.AddApprovalEvent(tool, command)
	b.syncSessionMirror()
}

func (b *SubagentPanelBlock) AddApprovalReviewEvent(callID, tool, command, status, risk, authorization, text string) {
	state := b.sessionState()
	state.AddApprovalReviewEvent(callID, tool, command, status, risk, authorization, text)
	b.syncSessionMirror()
}

func (b *SubagentPanelBlock) BlockID() string { return b.id }
func (b *SubagentPanelBlock) Kind() BlockKind { return BlockSubagent }
func (b *SubagentPanelBlock) Render(ctx BlockRenderContext) []RenderedRow {
	if b == nil || !b.Expanded {
		return nil
	}
	_ = b.sessionState()
	b.syncSessionMirror()
	lines := renderSubagentPanelLines(b, ctx)
	rows := make([]RenderedRow, len(lines))
	for i, l := range lines {
		rows[i] = StyledRow(b.id, l)
	}
	return rows
}

func renderSubagentPanelLines(panel *SubagentPanelBlock, ctx BlockRenderContext) []string {
	if panel == nil {
		return nil
	}
	baseWidth := ctx.Width
	if baseWidth <= 0 {
		baseWidth = 80
	}
	boxWidth := maxInt(20, baseWidth-4)
	_, lines, _ := subagentPanelRenderLines(panel, ctx, boxWidth)
	lines = tailNonEmptyStyledLines(lines, panel.previewLines())
	vm := PanelViewModel{
		Variant: tuikit.PanelShellVariantDrawer,
		Width:   boxWidth,
		Header:  tuikit.PanelHeaderModel{},
		Body:    lines,
	}
	return renderPanelViewModel(ctx.Theme, vm)
}

func (b *SubagentPanelBlock) previewLines() int {
	if b == nil {
		return subagentOutputPreviewLines
	}
	if b.VisibleLines > 0 {
		return b.VisibleLines
	}
	return subagentOutputPreviewLines
}

func subagentPanelRenderLines(panel *SubagentPanelBlock, ctx BlockRenderContext, boxWidth int) (contentWidth int, lines []string, overflow bool) {
	baseWidth := maxInt(1, boxWidth-4)
	return baseWidth, renderSubagentInnerLines(panel, ctx, baseWidth), false
}

func renderSubagentInnerLines(panel *SubagentPanelBlock, ctx BlockRenderContext, contentWidth int) []string {
	events, status := subagentPanelDisplayEvents(panel)
	return renderACPTranscriptLines(panel.id, events, status, contentWidth, ctx, acpTranscriptRenderOptions{
		EmptyPlaceholder: "waiting for subagent output",
		HideCompletedRow: true,
		ToolPanelRows:    panel.renderToolPanelRows,
	})
}

func (b *SubagentPanelBlock) renderToolPanelRows(request toolPanelRenderRequest) []RenderedRow {
	if b == nil {
		return request.renderUncached()
	}
	return renderCachedToolPanelRows(&b.toolPanelRenderCache, request, defaultToolPanelScrollState())
}

func subagentPanelDisplayEvents(panel *SubagentPanelBlock) ([]SubagentEvent, string) {
	if panel == nil {
		return nil, ""
	}
	status := strings.ToLower(strings.TrimSpace(panel.Status))
	if status != "completed" {
		return panel.Events, panel.Status
	}
	if ev, ok := latestSubagentNarrativeEvent(panel.Events, SEAssistant); ok {
		return []SubagentEvent{ev}, panel.Status
	}
	if ev, ok := latestSubagentNarrativeEvent(panel.Events, SEReasoning); ok {
		return []SubagentEvent{ev}, panel.Status
	}
	if len(panel.Events) > 0 {
		return panel.Events, panel.Status
	}
	return []SubagentEvent{{Kind: SEAssistant, Text: "completed"}}, panel.Status
}

func latestSubagentNarrativeEvent(events []SubagentEvent, kind SubagentEventKind) (SubagentEvent, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Kind == kind && strings.TrimSpace(ev.Text) != "" {
			return ev, true
		}
	}
	return SubagentEvent{}, false
}

func tailNonEmptyStyledLines(lines []string, limit int) []string {
	if limit <= 0 || len(lines) == 0 {
		return nil
	}
	out := make([]string, 0, minInt(len(lines), limit))
	for _, line := range lines {
		if strings.TrimSpace(ansi.Strip(line)) == "" {
			continue
		}
		out = append(out, line)
		if len(out) > limit {
			copy(out, out[len(out)-limit:])
			out = out[:limit]
		}
	}
	return out
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

func (b *SubagentPanelBlock) scrollableLineCount(ctx BlockRenderContext) int {
	if b == nil || !b.Expanded {
		return 0
	}
	_ = b.sessionState()
	b.syncSessionMirror()
	baseWidth := ctx.Width
	if baseWidth <= 0 {
		baseWidth = 80
	}
	boxWidth := maxInt(20, baseWidth-4)
	_, lines, _ := subagentPanelRenderLines(b, ctx, boxWidth)
	return len(lines)
}

func (b *SubagentPanelBlock) Scroll(delta int, ctx BlockRenderContext) bool {
	return false
}

func (b *SubagentPanelBlock) CanScroll(delta int, ctx BlockRenderContext) bool {
	return false
}

func (b *SubagentPanelBlock) scrollState() (*int, *bool) {
	if b == nil {
		return nil, nil
	}
	return &b.ScrollOffset, &b.FollowTail
}

func (b *SubagentPanelBlock) scrollbarVisibleUntilPtr() *time.Time {
	if b == nil {
		return nil
	}
	return &b.ScrollbarVisibleUntil
}
