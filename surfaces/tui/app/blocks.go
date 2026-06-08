package tuiapp

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"

	"github.com/charmbracelet/x/ansi"
)

// ---------------------------------------------------------------------------
// TranscriptBlock — a single committed log/system/user line.
// ---------------------------------------------------------------------------

type TranscriptBlock struct {
	id        string
	Raw       string
	Style     tuikit.LineStyle
	PreStyled bool // if true, Raw already contains ANSI styling
}

func NewTranscriptBlock(raw string, style tuikit.LineStyle) *TranscriptBlock {
	return &TranscriptBlock{id: nextBlockID(), Raw: raw, Style: style}
}

func (b *TranscriptBlock) BlockID() string { return b.id }
func (b *TranscriptBlock) Kind() BlockKind { return BlockTranscript }
func (b *TranscriptBlock) Render(ctx BlockRenderContext) []RenderedRow {
	if b.PreStyled {
		return []RenderedRow{StyledRow(b.id, b.Raw)}
	}
	colored := tuikit.ColorizeLogLine(b.Raw, b.Style, ctx.Theme)
	gutter := tuikit.LineExtraGutter(b.Style)
	styled := gutter + colored
	return []RenderedRow{StyledRow(b.id, styled)}
}

// ---------------------------------------------------------------------------
// SpacerBlock — an empty line for visual separation. Reuses BlockTranscript.
// ---------------------------------------------------------------------------

func NewSpacerBlock() *TranscriptBlock {
	return &TranscriptBlock{id: nextBlockID(), Raw: "", Style: tuikit.LineStyleDefault}
}

// ---------------------------------------------------------------------------
// UserNarrativeBlock — finalized user message rendered as plain text.
// ---------------------------------------------------------------------------

type UserNarrativeBlock struct {
	id          string
	Raw         string // user's display text (without the "> " prefix)
	renderCache narrativeBlockRenderCache
}

func NewUserNarrativeBlock(text string) *UserNarrativeBlock {
	return &UserNarrativeBlock{id: nextBlockID(), Raw: strings.TrimSpace(text)}
}

func (b *UserNarrativeBlock) BlockID() string { return b.id }
func (b *UserNarrativeBlock) Kind() BlockKind { return BlockTranscript }
func (b *UserNarrativeBlock) Render(ctx BlockRenderContext) []RenderedRow {
	return RenderTextWithContext(ctx, TextRenderRequest{
		Kind:           TextUser,
		Mode:           RenderPlain,
		MarkdownPolicy: MarkdownNone,
		Raw:            b.Raw,
		Prefix:         "▌ ",
		BlockID:        b.id,
		LineStyle:      tuikit.LineStyleUser,
	}).Rows
}

type narrativeBlockRenderCache struct {
	width      int
	themeKey   string
	raw        string
	rolePrefix string
	rows       []RenderedRow
}

func (c *narrativeBlockRenderCache) renderTextRows(blockID, raw, rolePrefix string, lineStyle tuikit.LineStyle, ctx BlockRenderContext) []RenderedRow {
	themeKey := ctx.renderThemeKey()
	if cached := c.cachedRows(raw, rolePrefix, ctx.Width, themeKey); cached != nil {
		return cached
	}
	mode := RenderFinal
	policy := MarkdownFull
	if lineStyle == tuikit.LineStyleReasoning {
		policy = MarkdownNone
	}
	rows := RenderTextWithContext(ctx, TextRenderRequest{
		Kind:           textKindForLineStyle(lineStyle),
		Mode:           mode,
		MarkdownPolicy: policy,
		Raw:            raw,
		Prefix:         rolePrefix,
		BlockID:        blockID,
		LineStyle:      lineStyle,
	}).Rows
	c.width = ctx.Width
	c.themeKey = themeKey
	c.raw = raw
	c.rolePrefix = rolePrefix
	c.rows = rows
	return rows
}

func (c *narrativeBlockRenderCache) cachedRows(raw, rolePrefix string, width int, themeKey string) []RenderedRow {
	if c == nil || len(c.rows) == 0 {
		return nil
	}
	if c.width != width || c.themeKey != themeKey {
		return nil
	}
	if c.raw != raw || c.rolePrefix != rolePrefix {
		return nil
	}
	return c.rows
}

// ---------------------------------------------------------------------------
// AssistantBlock — streaming or finalized assistant answer.
// ---------------------------------------------------------------------------

type AssistantBlock struct {
	id           string
	Actor        string
	Raw          string
	Streaming    bool
	LastFinal    string // dedup for duplicate final events
	renderCache  narrativeBlockRenderCache
	activeBuffer *activeNarrativeBuffer
}

func NewAssistantBlock(actor ...string) *AssistantBlock {
	label := ""
	if len(actor) > 0 {
		label = strings.TrimSpace(actor[0])
	}
	return &AssistantBlock{id: nextBlockID(), Actor: label, Streaming: true}
}

func (b *AssistantBlock) BlockID() string { return b.id }
func (b *AssistantBlock) Kind() BlockKind { return BlockAssistant }
func (b *AssistantBlock) Render(ctx BlockRenderContext) []RenderedRow {
	rolePrefix := "· " + assistantActorPrefix(b.Actor)
	if b.Streaming {
		if b.activeBuffer != nil && !b.activeBuffer.Empty() {
			return b.activeBuffer.RenderRows(b.id, rolePrefix, tuikit.LineStyleAssistant, ctx)
		}
		if strings.TrimSpace(b.Raw) != "" {
			return renderActiveNarrativeTextRows(b.id, b.Raw, rolePrefix, tuikit.LineStyleAssistant, ctx)
		}
		return nil
	}
	return b.renderCache.renderTextRows(
		b.id,
		b.Raw,
		rolePrefix,
		tuikit.LineStyleAssistant,
		ctx,
	)
}

func assistantActorPrefix(actor string) string {
	if actor = strings.TrimSpace(actor); actor != "" && !strings.EqualFold(actor, "assistant") {
		return actor + ": "
	}
	return ""
}

// ---------------------------------------------------------------------------
// ReasoningBlock — streaming or finalized reasoning/thinking.
// ---------------------------------------------------------------------------

type ReasoningBlock struct {
	id           string
	Actor        string
	Raw          string
	Streaming    bool
	renderCache  narrativeBlockRenderCache
	activeBuffer *activeNarrativeBuffer
}

func NewReasoningBlock(actor ...string) *ReasoningBlock {
	label := ""
	if len(actor) > 0 {
		label = strings.TrimSpace(actor[0])
	}
	return &ReasoningBlock{id: nextBlockID(), Actor: label, Streaming: true}
}

func (b *ReasoningBlock) BlockID() string { return b.id }
func (b *ReasoningBlock) Kind() BlockKind { return BlockReasoning }
func (b *ReasoningBlock) Render(ctx BlockRenderContext) []RenderedRow {
	prefix := "› "
	if actor := strings.TrimSpace(b.Actor); actor != "" && !strings.EqualFold(actor, "assistant") {
		prefix += actor + ": "
	}
	if b.Streaming {
		if b.activeBuffer != nil && !b.activeBuffer.Empty() {
			return b.activeBuffer.RenderRows(b.id, prefix, tuikit.LineStyleReasoning, ctx)
		}
		if strings.TrimSpace(b.Raw) != "" {
			return renderActiveNarrativeTextRows(b.id, b.Raw, prefix, tuikit.LineStyleReasoning, ctx)
		}
		return nil
	}
	return b.renderCache.renderTextRows(b.id, b.Raw, prefix, tuikit.LineStyleReasoning, ctx)
}

// ---------------------------------------------------------------------------
// MainACPTurnBlock – root ACP-controlled turn in the main transcript.
// ---------------------------------------------------------------------------

type MainACPTurnBlock struct {
	id                   string
	SessionID            string
	Status               string
	StartedAt            time.Time
	EndedAt              time.Time
	Events               []SubagentEvent
	ExpandedTools        map[string]bool
	ExpandedToolOutput   map[string]bool
	ToolPanelScroll      map[string]toolPanelScrollState
	ExpandedThought      map[string]bool
	ExpandedExplore      map[string]bool
	toolPanelRenderCache map[string]toolOutputRenderCache
	toolEventIndex       map[string]int
	compactHeightBudget  compactHeightBudgetState
}

func NewMainACPTurnBlock(sessionID string) *MainACPTurnBlock {
	return &MainACPTurnBlock{
		id:             nextBlockID(),
		SessionID:      strings.TrimSpace(sessionID),
		Status:         "running",
		StartedAt:      time.Now(),
		toolEventIndex: map[string]int{},
	}
}

func (b *MainACPTurnBlock) BlockID() string { return b.id }
func (b *MainACPTurnBlock) Kind() BlockKind { return BlockMainACPTurn }

func (b *MainACPTurnBlock) AppendStreamChunk(kind SubagentEventKind, chunk string, occurredAt ...time.Time) {
	if b == nil {
		return
	}
	at := narrativeEventTime(occurredAt...)
	if idx := latestNarrativeAppendTargetIndex(b.Events, kind); idx >= 0 {
		appendNarrativeEventChunk(&b.Events[idx], kind, chunk, at, appendDeltaStreamChunk)
		return
	}
	b.Events = append(b.Events, newNarrativeEventChunk(kind, chunk, at))
}

func (b *MainACPTurnBlock) ClearActiveBuffers() {
	if b == nil {
		return
	}
	out := b.Events[:0]
	for _, ev := range b.Events {
		if ev.ActiveBuffer != nil && activeNarrativeEventKind(ev.Kind) {
			continue
		}
		ev.ActiveBuffer = nil
		out = append(out, ev)
	}
	clear(b.Events[len(out):])
	b.Events = out
}

func activeNarrativeEventKind(kind SubagentEventKind) bool {
	return kind == SEAssistant || kind == SEReasoning
}

func (b *MainACPTurnBlock) ReplaceFinalStreamChunk(kind SubagentEventKind, chunk string, occurredAt ...time.Time) {
	if b == nil {
		return
	}
	if strings.TrimSpace(chunk) == "" {
		return
	}
	at := narrativeEventTime(occurredAt...)
	chunk = collapseRepeatedNarrativeText(chunk)
	if cumulativeFinalNarrativeAlreadyRendered(b.Events, kind, chunk) {
		return
	}
	if idx := latestNarrativeFinalTargetIndex(b.Events, kind); idx >= 0 {
		chunk = cumulativeFinalNarrativeTimelineText(b.Events, kind, chunk, idx)
		if strings.TrimSpace(chunk) == "" {
			return
		}
		replaceNarrativeEventFinal(&b.Events[idx], chunk, at)
		b.Events = pruneNarrativeEventsCoveredByFinal(b.Events, idx, kind)
		return
	}
	chunk = cumulativeFinalNarrativeTimelineText(b.Events, kind, chunk, len(b.Events))
	if strings.TrimSpace(chunk) == "" {
		return
	}
	ev := SubagentEvent{Kind: kind, Text: chunk}
	markNarrativeTiming(&ev, at)
	b.Events = append(b.Events, ev)
	b.Events = pruneNarrativeEventsCoveredByFinal(b.Events, len(b.Events)-1, kind)
}

func (b *MainACPTurnBlock) UpdateTool(callID, name, args, output string, final bool, err bool) {
	b.UpdateToolWithMeta(callID, name, args, output, final, err, ToolUpdateMeta{})
}

func (b *MainACPTurnBlock) UpdateToolWithMeta(callID, name, args, output string, final bool, err bool, meta ToolUpdateMeta) {
	if b == nil {
		return
	}
	if !isTerminalPanelToolKind(name, meta.ToolKind) {
		output = strings.TrimSpace(output)
	}
	events, _, collapse := applyToolEventUpdate(b.Events, toolEventUpdate{
		CallID: callID,
		Name:   name,
		Args:   args,
		Output: output,
		Final:  final,
		Err:    err,
		Meta:   meta,
	}, b.toolEventIndex)
	b.Events = events
	if collapse {
		b.setToolPanelExpanded(strings.TrimSpace(callID), false)
	}
}

func shouldReplaceSpawnDisplayArgs(existing string, incoming string) bool {
	existing = sanitizeSpawnHeaderArgs(strings.TrimSpace(existing))
	incoming = sanitizeSpawnHeaderArgs(strings.TrimSpace(incoming))
	if incoming == "" || incoming == existing {
		return false
	}
	if existing == "" {
		return true
	}
	if !strings.Contains(existing, ":") && strings.Contains(incoming, ":") {
		return true
	}
	return len([]rune(incoming)) > len([]rune(existing)) &&
		(strings.HasPrefix(incoming, existing+":") || strings.Contains(incoming, ":"))
}

func shouldReplaceCompletedTerminalToolEvent(existing SubagentEvent, incoming SubagentEvent) bool {
	if !existing.Done || !incoming.Done {
		return false
	}
	if !isTerminalPanelToolEvent(existing) && !isTerminalPanelToolEvent(incoming) {
		return false
	}
	if strings.TrimSpace(existing.CallID) == "" || strings.TrimSpace(existing.CallID) != strings.TrimSpace(incoming.CallID) {
		return false
	}
	return true
}

func (b *MainACPTurnBlock) UpdatePlan(entries []planEntryState) {
	if b == nil {
		return
	}
	if n := len(b.Events); n > 0 && b.Events[n-1].Kind == SEPlan {
		b.Events[n-1].PlanEntries = entries
		return
	}
	b.Events = append(b.Events, SubagentEvent{
		Kind:        SEPlan,
		PlanEntries: entries,
	})
}

func (b *MainACPTurnBlock) SetStatus(state string, approvalTool string, approvalCommand string, occurredAt time.Time) {
	if b == nil {
		return
	}
	b.Status = strings.ToLower(strings.TrimSpace(state))
	collapseTools := false
	switch b.Status {
	case "completed", "failed", "interrupted", "cancelled", "canceled", "terminated":
		if b.EndedAt.IsZero() {
			collapseTools = true
			if !occurredAt.IsZero() {
				b.EndedAt = occurredAt
			} else {
				b.EndedAt = time.Now()
			}
		}
	default:
		b.EndedAt = time.Time{}
	}
	if collapseTools {
		b.collapseAllToolPanels()
	}
	if !strings.EqualFold(b.Status, "waiting_approval") {
		return
	}
	if n := len(b.Events); n > 0 && b.Events[n-1].Kind == SEApproval {
		b.Events[n-1].ApprovalTool = strings.TrimSpace(approvalTool)
		b.Events[n-1].ApprovalCommand = strings.TrimSpace(approvalCommand)
		return
	}
	b.Events = append(b.Events, SubagentEvent{
		Kind:            SEApproval,
		ApprovalTool:    strings.TrimSpace(approvalTool),
		ApprovalCommand: strings.TrimSpace(approvalCommand),
	})
}

func (b *MainACPTurnBlock) AddApprovalReviewEvent(callID, tool, command, status, risk, authorization, text string) {
	if b == nil {
		return
	}
	b.Events, _ = addApprovalReviewSubagentEvent(b.Events, callID, tool, command, status, risk, authorization, text)
}

func (b *MainACPTurnBlock) Render(ctx BlockRenderContext) []RenderedRow {
	if b == nil {
		return nil
	}
	rows := renderACPTranscriptRows(b.id, b.Events, b.Status, maxInt(8, ctx.Width), ctx, acpTranscriptRenderOptions{
		UseStatusPlaceholder:   true,
		PlaceholderAsMeta:      true,
		HideWaitingApprovalRow: true,
		HideCompletedRow:       true,
		ToolOutputPanels:       true,
		ToolPanelExpanded:      b.toolPanelExpanded,
		ToolPanelFullOutput:    b.toolPanelFullOutput,
		ToolPanelRows:          b.renderToolPanelRows,
		ExplorationExpanded:    b.explorationExpanded,
		ToolPanelScrollState:   b.toolPanelScrollState,
		ReasoningExpanded:      b.reasoningExpanded,
	})
	return b.compactHeightBudget.apply(b.id, rows, b.Events, b.Status, ctx)
}

type compactHeightBudgetState struct {
	contextKey          string
	lastRows            int
	lastHadDeferredTail bool
	floorRows           int
}

func (s *compactHeightBudgetState) apply(blockID string, rows []RenderedRow, events []SubagentEvent, status string, ctx BlockRenderContext) []RenderedRow {
	if s == nil {
		return rows
	}
	contextKey := compactHeightBudgetContextKey(ctx)
	if s.contextKey != contextKey {
		*s = compactHeightBudgetState{contextKey: contextKey}
	}
	rowCount := len(rows)
	terminal := isTerminalACPTranscriptStatus(status)
	hadDeferredTail := hasDeferredLiveTailCompactStage(events, status)
	switch {
	case terminal:
		s.floorRows = 0
	case s.lastHadDeferredTail && !hadDeferredTail && s.lastRows > rowCount:
		s.floorRows = minInt(s.lastRows, rowCount+compactHeightBudgetMaxRows(ctx))
	case hadDeferredTail:
		s.floorRows = 0
	case s.floorRows <= rowCount:
		s.floorRows = 0
	}
	if s.floorRows > rowCount {
		s.floorRows = minInt(s.floorRows, rowCount+compactHeightBudgetMaxRows(ctx))
	}
	if !terminal && s.floorRows > rowCount {
		rows = appendCompactHeightBudgetSpacerRows(rows, blockID, s.floorRows-rowCount)
	}
	s.lastRows = rowCount
	s.lastHadDeferredTail = hadDeferredTail
	return rows
}

func compactHeightBudgetContextKey(ctx BlockRenderContext) string {
	return strings.Join([]string{
		strconv.Itoa(ctx.Width),
		strconv.Itoa(ctx.TermWidth),
		ctx.renderThemeKey(),
	}, "|")
}

func compactHeightBudgetMaxRows(ctx BlockRenderContext) int {
	if ctx.Height > 0 {
		return maxInt(1, ctx.Height/2)
	}
	return 4
}

func appendCompactHeightBudgetSpacerRows(rows []RenderedRow, blockID string, count int) []RenderedRow {
	for i := 0; i < count; i++ {
		rows = append(rows, PlainRow(blockID, ""))
	}
	return rows
}

func (s compactHeightBudgetState) heightSensitive() bool {
	return s.floorRows > 0
}

func hasDeferredLiveTailCompactStage(events []SubagentEvent, status string) bool {
	if isTerminalACPTranscriptStatus(status) {
		return false
	}
	for i := range events {
		if liveTailHasPotentialDeferredCompactStage(events, i, status) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// ParticipantTurnBlock — inline external-agent turn inside the main transcript.
// ---------------------------------------------------------------------------

type ParticipantTurnBlock struct {
	id                   string
	SessionID            string
	Actor                string
	Status               string
	Expanded             bool
	StartedAt            time.Time
	EndedAt              time.Time
	Events               []SubagentEvent
	ExpandedTools        map[string]bool
	ExpandedToolOutput   map[string]bool
	ToolPanelScroll      map[string]toolPanelScrollState
	ExpandedThought      map[string]bool
	ExpandedExplore      map[string]bool
	toolPanelRenderCache map[string]toolOutputRenderCache
	toolEventIndex       map[string]int
	compactHeightBudget  compactHeightBudgetState
}

func NewParticipantTurnBlock(sessionID, actor string) *ParticipantTurnBlock {
	return &ParticipantTurnBlock{
		id:             nextBlockID(),
		SessionID:      strings.TrimSpace(sessionID),
		Actor:          strings.TrimSpace(actor),
		Status:         "running",
		Expanded:       true,
		StartedAt:      time.Now(),
		toolEventIndex: map[string]int{},
	}
}

func (b *ParticipantTurnBlock) BlockID() string { return b.id }
func (b *ParticipantTurnBlock) Kind() BlockKind { return BlockParticipantTurn }

func (b *ParticipantTurnBlock) AppendStreamChunk(kind SubagentEventKind, chunk string, occurredAt ...time.Time) {
	if b == nil {
		return
	}
	at := narrativeEventTime(occurredAt...)
	if idx := latestNarrativeAppendTargetIndex(b.Events, kind); idx >= 0 {
		appendNarrativeEventChunk(&b.Events[idx], kind, chunk, at, appendDeltaStreamChunk)
		return
	}
	b.Events = append(b.Events, newNarrativeEventChunk(kind, chunk, at))
}

func (b *ParticipantTurnBlock) ReplaceFinalStreamChunk(kind SubagentEventKind, chunk string, occurredAt ...time.Time) {
	if b == nil {
		return
	}
	if strings.TrimSpace(chunk) == "" {
		return
	}
	at := narrativeEventTime(occurredAt...)
	chunk = collapseRepeatedNarrativeText(chunk)
	if cumulativeFinalNarrativeAlreadyRendered(b.Events, kind, chunk) {
		return
	}
	if idx := latestNarrativeFinalTargetIndex(b.Events, kind); idx >= 0 {
		chunk = cumulativeFinalNarrativeTimelineText(b.Events, kind, chunk, idx)
		if strings.TrimSpace(chunk) == "" {
			return
		}
		replaceNarrativeEventFinal(&b.Events[idx], chunk, at)
		b.Events = pruneNarrativeEventsCoveredByFinal(b.Events, idx, kind)
		return
	}
	chunk = cumulativeFinalNarrativeTimelineText(b.Events, kind, chunk, len(b.Events))
	if strings.TrimSpace(chunk) == "" {
		return
	}
	ev := SubagentEvent{Kind: kind, Text: chunk}
	markNarrativeTiming(&ev, at)
	b.Events = append(b.Events, ev)
	b.Events = pruneNarrativeEventsCoveredByFinal(b.Events, len(b.Events)-1, kind)
}

func (b *ParticipantTurnBlock) UpdateTool(callID, name, args, output string, final bool, err bool) {
	b.UpdateToolWithMeta(callID, name, args, output, final, err, ToolUpdateMeta{})
}

func (b *ParticipantTurnBlock) UpdateToolWithMeta(callID, name, args, output string, final bool, err bool, meta ToolUpdateMeta) {
	if b == nil {
		return
	}
	if !isTerminalPanelToolKind(name, meta.ToolKind) {
		output = strings.TrimSpace(output)
	}
	events, _, collapse := applyToolEventUpdate(b.Events, toolEventUpdate{
		CallID: callID,
		Name:   name,
		Args:   args,
		Output: output,
		Final:  final,
		Err:    err,
		Meta:   meta,
	}, b.toolEventIndex)
	b.Events = events
	if collapse {
		b.setToolPanelExpanded(strings.TrimSpace(callID), false)
	}
}

func (b *ParticipantTurnBlock) UpdatePlan(entries []planEntryState) {
	if b == nil {
		return
	}
	if n := len(b.Events); n > 0 && b.Events[n-1].Kind == SEPlan {
		b.Events[n-1].PlanEntries = entries
		return
	}
	b.Events = append(b.Events, SubagentEvent{
		Kind:        SEPlan,
		PlanEntries: entries,
	})
}

func (b *ParticipantTurnBlock) SetStatus(state string, approvalTool string, approvalCommand string, occurredAt time.Time) {
	if b == nil {
		return
	}
	b.Status = strings.ToLower(strings.TrimSpace(state))
	collapseTools := false
	switch b.Status {
	case "completed", "failed", "interrupted", "cancelled", "canceled", "terminated":
		if b.EndedAt.IsZero() {
			collapseTools = true
			if !occurredAt.IsZero() {
				b.EndedAt = occurredAt
			} else {
				b.EndedAt = time.Now()
			}
		}
	default:
		b.EndedAt = time.Time{}
	}
	if collapseTools {
		b.collapseAllToolPanels()
	}
	if !strings.EqualFold(b.Status, "waiting_approval") {
		return
	}
	if n := len(b.Events); n > 0 && b.Events[n-1].Kind == SEApproval {
		b.Events[n-1].ApprovalTool = strings.TrimSpace(approvalTool)
		b.Events[n-1].ApprovalCommand = strings.TrimSpace(approvalCommand)
		return
	}
	b.Events = append(b.Events, SubagentEvent{
		Kind:            SEApproval,
		ApprovalTool:    strings.TrimSpace(approvalTool),
		ApprovalCommand: strings.TrimSpace(approvalCommand),
	})
}

func (b *ParticipantTurnBlock) AddApprovalReviewEvent(callID, tool, command, status, risk, authorization, text string) {
	if b == nil {
		return
	}
	b.Events, _ = addApprovalReviewSubagentEvent(b.Events, callID, tool, command, status, risk, authorization, text)
}

func (b *ParticipantTurnBlock) Render(ctx BlockRenderContext) []RenderedRow {
	if b == nil {
		return nil
	}
	bodyRows := []RenderedRow(nil)
	if b.Expanded {
		bodyRows = renderACPTranscriptRows(b.id, b.Events, b.Status, maxInt(8, ctx.Width), ctx, acpTranscriptRenderOptions{
			UseStatusPlaceholder:   true,
			PlaceholderAsMeta:      true,
			HideWaitingApprovalRow: true,
			HideCompletedRow:       true,
			ToolOutputPanels:       true,
			ToolPanelExpanded:      b.toolPanelExpanded,
			ToolPanelFullOutput:    b.toolPanelFullOutput,
			ToolPanelRows:          b.renderToolPanelRows,
			ExplorationExpanded:    b.explorationExpanded,
			ToolPanelScrollState:   b.toolPanelScrollState,
			ReasoningExpanded:      b.reasoningExpanded,
		})
	}
	if len(bodyRows) == 0 && participantTurnIsTerminal(b.Status) && strings.TrimSpace(b.Actor) == "" {
		return nil
	}
	rows := []RenderedRow{StyledRow(b.id, renderParticipantTurnHeader(b, ctx))}
	if !b.Expanded {
		return rows
	}
	rows = append(rows, bodyRows...)
	rows = b.compactHeightBudget.apply(b.id, rows, b.Events, b.Status, ctx)
	if b.Expanded && participantTurnIsTerminal(b.Status) {
		if footer := renderParticipantTurnFooter(b, ctx); strings.TrimSpace(ansi.Strip(footer)) != "" {
			rows = append(rows, StyledRow(b.id, footer))
		}
	}
	return rows
}

// ---------------------------------------------------------------------------
// DividerBlock — turn separator.
// ---------------------------------------------------------------------------

type DividerBlock struct {
	id    string
	Label string
	Text  string // legacy pre-rendered divider text
}

func NewDividerBlock(label string) *DividerBlock {
	return &DividerBlock{id: nextBlockID(), Label: strings.TrimSpace(label)}
}

func (b *DividerBlock) BlockID() string { return b.id }
func (b *DividerBlock) Kind() BlockKind { return BlockDivider }
func (b *DividerBlock) Render(ctx BlockRenderContext) []RenderedRow {
	label := strings.TrimSpace(b.Label)
	if label == "" && strings.TrimSpace(b.Text) != "" {
		label = strings.TrimSpace(ansi.Strip(b.Text))
	}
	plain := centeredDivider(maxInt(12, ctx.Width), label)
	styled := ctx.Theme.HelpHintTextStyle().Render(plain)
	return []RenderedRow{{
		Styled:     styled,
		Plain:      plain,
		BlockID:    b.id,
		PreWrapped: true,
	}}
}

func renderParticipantActorLabel(theme tuikit.Theme, actor string) string {
	name, provider := splitParticipantActor(actor)
	nameStyle := theme.TextStyle().Bold(true)
	if provider == "" {
		return nameStyle.Render(name)
	}
	return nameStyle.Render(name) +
		" " + theme.TranscriptMetaStyle().Render(fmt.Sprintf("[%s]", provider))
}

func narrativeLinePrefixes(lineStyle tuikit.LineStyle) (string, string) {
	switch lineStyle {
	case tuikit.LineStyleAssistant:
		return "· ", "  "
	case tuikit.LineStyleReasoning:
		return "› ", "  "
	default:
		return "", ""
	}
}

func shouldRenderToolEvent(ev SubagentEvent) bool {
	if ev.Kind != SEToolCall {
		return true
	}
	if !ev.Done || ev.Err {
		return true
	}
	if !renderableTextHasContent(ev.Output) || strings.EqualFold(strings.TrimSpace(ev.Output), "completed") {
		return false
	}
	return true
}

func visibleNarrativeEvents(events []SubagentEvent, status string) []SubagentEvent {
	if len(events) == 0 {
		return nil
	}
	hidePlan := strings.EqualFold(strings.TrimSpace(status), "waiting_approval") && hasApprovalEvent(events)
	out := make([]SubagentEvent, 0, len(events))
	for i, ev := range events {
		if ev.Kind == SEReasoning && !shouldRenderReasoningEvent(events, i, status) {
			continue
		}
		if hidePlan && ev.Kind == SEPlan {
			continue
		}
		out = append(out, ev)
	}
	return out
}

func updateLinkedTerminalEvent(events []SubagentEvent, callID string, toolName string, taskID string, output string, final bool, err bool, meta ToolUpdateMeta) bool {
	toolName = strings.TrimSpace(toolName)
	taskID = strings.TrimSpace(taskID)
	if strings.EqualFold(toolName, "SPAWN") {
		if updateLinkedTaskWriteEvent(events, taskID, output, final, err) {
			return true
		}
		return updateLinkedSpawnEvent(events, strings.TrimSpace(callID), taskID, output, final, err)
	}
	return false
}

func updateLinkedSpawnEvent(events []SubagentEvent, callID string, taskID string, output string, final bool, err bool) bool {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || (!renderableTextHasContent(output) && !final) {
		return false
	}
	for i := len(events) - 1; i >= 0; i-- {
		ev := &events[i]
		if ev.Kind != SEToolCall || strings.TrimSpace(ev.TaskID) != taskID {
			continue
		}
		if !strings.EqualFold(toolSemanticName(ev.Name, ev.ToolKind), "SPAWN") {
			continue
		}
		if strings.TrimSpace(ev.CallID) == callID {
			return false
		}
		if renderableTextHasContent(output) {
			if final || ev.Done {
				ev.Output = output
			} else {
				ev.Output = mergeSubagentStreamChunk(ev.Output, output)
			}
			ev.OutputSynthetic = false
		}
		if final {
			ev.Done = true
			ev.Err = err
		} else if renderableTextHasContent(output) {
			ev.Done = false
			ev.Err = false
		}
		return true
	}
	return false
}

func taskCancelArgsWithLinkedCommand(args string, events []SubagentEvent, taskID string) string {
	verb, _ := splitTaskAction(args)
	if !strings.EqualFold(verb, "Cancel") {
		return args
	}
	command := linkedTerminalCommandForTask(events, taskID)
	if command == "" {
		return args
	}
	return "Cancel " + command
}

func linkedTerminalCommandForTask(events []SubagentEvent, taskID string) string {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return ""
	}
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Kind != SEToolCall || strings.TrimSpace(ev.TaskID) != taskID || !isTerminalPanelToolEvent(ev) {
			continue
		}
		if strings.EqualFold(toolSemanticName(ev.Name, ev.ToolKind), "SPAWN") {
			continue
		}
		if command := strings.TrimSpace(ev.FullArgs); command != "" {
			return command
		}
		if command := strings.TrimSpace(ev.Args); command != "" {
			return command
		}
	}
	return ""
}

func updateLinkedTaskWriteEvent(events []SubagentEvent, taskID string, output string, final bool, err bool) bool {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || (!renderableTextHasContent(output) && !final) {
		return false
	}
	for i := len(events) - 1; i >= 0; i-- {
		ev := &events[i]
		if ev.Kind != SEToolCall {
			continue
		}
		if strings.TrimSpace(ev.TaskID) != taskID {
			continue
		}
		if strings.EqualFold(toolSemanticName(ev.Name, ev.ToolKind), "SPAWN") {
			return false
		}
		if !strings.EqualFold(strings.TrimSpace(ev.Name), "TASK") || taskEventAction(*ev) != "write" {
			continue
		}
		if renderableTextHasContent(output) {
			if final || ev.Done {
				ev.Output = output
			} else {
				ev.Output = mergeSubagentStreamChunk(ev.Output, output)
			}
			ev.OutputSynthetic = false
		}
		if final {
			ev.Done = true
			ev.Err = err
		} else if renderableTextHasContent(output) {
			ev.Done = false
			ev.Err = false
		}
		return true
	}
	return false
}

func spawnContinuationDisplayArgs(existing string, prompt string) string {
	prompt = strings.Join(strings.Fields(strings.TrimSpace(prompt)), " ")
	if prompt == "" {
		return strings.TrimSpace(existing)
	}
	existing = sanitizeSpawnHeaderArgs(existing)
	if before, _, ok := strings.Cut(existing, ":"); ok && strings.TrimSpace(before) != "" {
		return strings.TrimSpace(before) + ": " + prompt
	}
	return prompt
}

func shouldIgnoreStaleTerminalUpdate(events []SubagentEvent, callID string, name string, toolKind string, final bool) bool {
	if final || strings.TrimSpace(callID) == "" || !isTerminalPanelToolKind(name, toolKind) {
		return false
	}
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Kind != SEToolCall || strings.TrimSpace(ev.CallID) != strings.TrimSpace(callID) || !isTerminalPanelToolEvent(ev) {
			continue
		}
		return ev.Done
	}
	return false
}

func addApprovalReviewSubagentEvent(events []SubagentEvent, callID, tool, command, status, risk, authorization, text string) ([]SubagentEvent, bool) {
	review := SubagentEvent{
		Kind:            SEApproval,
		CallID:          strings.TrimSpace(callID),
		ApprovalTool:    strings.TrimSpace(tool),
		ApprovalCommand: strings.TrimSpace(command),
		ApprovalStatus:  strings.TrimSpace(status),
		ApprovalRisk:    strings.TrimSpace(risk),
		ApprovalAuth:    strings.TrimSpace(authorization),
		ApprovalText:    strings.TrimSpace(text),
	}
	if review.CallID != "" {
		for i := range events {
			if events[i].Kind != SEApproval || strings.TrimSpace(events[i].CallID) != review.CallID {
				continue
			}
			mergeApprovalReviewEvent(&events[i], review)
			events, _ = relocateApprovalReviewEventsAfterTool(events, review.CallID)
			return events, true
		}
		if toolIdx := latestToolEventIndexForCallID(events, review.CallID); toolIdx >= 0 {
			return insertSubagentEvent(events, approvalReviewInsertIndex(events, toolIdx, review.CallID), review), true
		}
	}
	return append(events, review), true
}

func mergeApprovalReviewEvent(target *SubagentEvent, review SubagentEvent) {
	if target == nil {
		return
	}
	target.Kind = SEApproval
	if review.CallID != "" {
		target.CallID = review.CallID
	}
	if review.ApprovalTool != "" {
		target.ApprovalTool = review.ApprovalTool
	}
	if review.ApprovalCommand != "" {
		target.ApprovalCommand = review.ApprovalCommand
	}
	if review.ApprovalStatus != "" {
		target.ApprovalStatus = review.ApprovalStatus
	}
	if review.ApprovalRisk != "" {
		target.ApprovalRisk = review.ApprovalRisk
	}
	if review.ApprovalAuth != "" {
		target.ApprovalAuth = review.ApprovalAuth
	}
	if review.ApprovalText != "" {
		target.ApprovalText = review.ApprovalText
	}
}

func relocateApprovalReviewEventsAfterTool(events []SubagentEvent, callID string) ([]SubagentEvent, bool) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return events, false
	}
	toolIdx := latestToolEventIndexForCallID(events, callID)
	if toolIdx < 0 {
		return events, false
	}
	changed := false
	for {
		insertIdx := approvalReviewInsertIndex(events, toolIdx, callID)
		moveIdx := -1
		for i, ev := range events {
			if ev.Kind != SEApproval || strings.TrimSpace(ev.CallID) != callID {
				continue
			}
			if i > toolIdx && i < insertIdx {
				continue
			}
			moveIdx = i
			break
		}
		if moveIdx < 0 {
			return events, changed
		}
		review := events[moveIdx]
		events = append(events[:moveIdx], events[moveIdx+1:]...)
		if moveIdx < toolIdx {
			toolIdx--
		}
		insertIdx = approvalReviewInsertIndex(events, toolIdx, callID)
		events = insertSubagentEvent(events, insertIdx, review)
		changed = true
	}
}

func approvalReviewInsertIndex(events []SubagentEvent, toolIdx int, callID string) int {
	insertIdx := toolIdx + 1
	for insertIdx < len(events) {
		ev := events[insertIdx]
		if ev.Kind != SEApproval || strings.TrimSpace(ev.CallID) != callID {
			break
		}
		insertIdx++
	}
	return insertIdx
}

func insertSubagentEvent(events []SubagentEvent, idx int, ev SubagentEvent) []SubagentEvent {
	if idx < 0 || idx > len(events) {
		idx = len(events)
	}
	events = append(events, SubagentEvent{})
	copy(events[idx+1:], events[idx:])
	events[idx] = ev
	return events
}

func latestToolEventIndexForCallID(events []SubagentEvent, callID string) int {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return -1
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == SEToolCall && strings.TrimSpace(events[i].CallID) == callID {
			return i
		}
	}
	return -1
}

func hasApprovalEvent(events []SubagentEvent) bool {
	for _, ev := range events {
		if ev.Kind == SEApproval {
			return true
		}
	}
	return false
}

func shouldRenderReasoningEvent(events []SubagentEvent, idx int, _ string) bool {
	if idx < 0 || idx >= len(events) {
		return false
	}
	ev := events[idx]
	return ev.Kind == SEReasoning && strings.TrimSpace(ev.Text) != ""
}

func splitParticipantActor(actor string) (name string, provider string) {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return "", ""
	}
	open := strings.LastIndex(actor, "(")
	closeIdx := strings.LastIndex(actor, ")")
	if open <= 0 || closeIdx != len(actor)-1 || closeIdx <= open+1 {
		return actor, ""
	}
	name = strings.TrimSpace(actor[:open])
	provider = strings.TrimSpace(actor[open+1 : closeIdx])
	if name == "" || provider == "" {
		return actor, ""
	}
	return name, provider
}
