package tuiapp

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/OnslaughtSnail/caelis/tui/tuikit"

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
// UserNarrativeBlock — finalized user message rendered through glamour.
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
	return b.renderCache.renderNarrativeRows(b.id, b.Raw, "> ", tuikit.LineStyleUser, ctx, false)
}

type narrativeBlockRenderCache struct {
	width      int
	themeKey   string
	raw        string
	rolePrefix string
	streaming  bool
	rows       []RenderedRow
}

func (c *narrativeBlockRenderCache) renderNarrativeRows(blockID, raw, rolePrefix string, lineStyle tuikit.LineStyle, ctx BlockRenderContext, streaming bool) []RenderedRow {
	themeKey := ctx.renderThemeKey()
	if cached := c.cachedRows(raw, rolePrefix, ctx.Width, themeKey, streaming); cached != nil {
		return cached
	}
	ctx.observeGlamourRender()
	rows := renderNarrativeRows(blockID, raw, rolePrefix, lineStyle, ctx.Width, ctx.Theme, streaming)
	c.width = ctx.Width
	c.themeKey = themeKey
	c.raw = raw
	c.rolePrefix = rolePrefix
	c.streaming = streaming
	c.rows = rows
	return rows
}

func (c *narrativeBlockRenderCache) cachedRows(raw, rolePrefix string, width int, themeKey string, streaming bool) []RenderedRow {
	if c == nil || len(c.rows) == 0 {
		return nil
	}
	if c.width != width || c.themeKey != themeKey {
		return nil
	}
	if c.raw != raw || c.rolePrefix != rolePrefix || c.streaming != streaming {
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
	rolePrefix := "* " + assistantActorPrefix(b.Actor)
	if b.Streaming && b.activeBuffer != nil && !b.activeBuffer.Empty() {
		return b.activeBuffer.RenderRows(b.id, rolePrefix, tuikit.LineStyleAssistant, ctx)
	}
	return b.renderCache.renderNarrativeRows(
		b.id,
		b.Raw,
		rolePrefix,
		tuikit.LineStyleAssistant,
		ctx,
		b.Streaming,
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
	prefix := "· "
	if actor := strings.TrimSpace(b.Actor); actor != "" && !strings.EqualFold(actor, "assistant") {
		prefix += actor + ": "
	}
	if b.Streaming && b.activeBuffer != nil && !b.activeBuffer.Empty() {
		return b.activeBuffer.RenderRows(b.id, prefix, tuikit.LineStyleReasoning, ctx)
	}
	return b.renderCache.renderNarrativeRows(b.id, b.Raw, prefix, tuikit.LineStyleReasoning, ctx, b.Streaming)
}

// renderNarrativeFallbackRows preserves multi-line structure when glamour
// cannot produce usable output. This is intentionally minimal and should only
// be exercised for empty or degenerate markdown.
func renderNarrativeFallbackRows(blockID, raw, rolePrefix, continuationPrefix string, lineStyle tuikit.LineStyle, theme tuikit.Theme) []RenderedRow {
	normalized := strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n")
	if strings.TrimSpace(normalized) == "" {
		styled := tuikit.ColorizeLogLine(rolePrefix, lineStyle, theme)
		return []RenderedRow{StyledPlainRow(blockID, rolePrefix, styled)}
	}
	normalized = strings.TrimRight(normalized, "\n")
	lines := strings.Split(normalized, "\n")
	rows := make([]RenderedRow, 0, len(lines))
	for i, line := range lines {
		prefix := continuationPrefix
		if i == 0 {
			prefix = rolePrefix
		}
		plain := prefix + line
		styled := tuikit.ColorizeLogLine(plain, lineStyle, theme)
		rows = append(rows, StyledPlainRow(blockID, plain, styled))
	}
	return rows
}

func renderNarrativeRows(blockID, raw, rolePrefix string, lineStyle tuikit.LineStyle, width int, theme tuikit.Theme, streaming bool) []RenderedRow {
	if rows := renderNarrativeGlamourRows(blockID, raw, rolePrefix, lineStyle, width, theme, streaming); len(rows) > 0 {
		return rows
	}
	_, continuationPrefix := narrativeLinePrefixes(lineStyle)
	return renderNarrativeFallbackRows(blockID, raw, rolePrefix, continuationPrefix, lineStyle, theme)
}

func renderNarrativeGlamourRows(blockID, raw, rolePrefix string, lineStyle tuikit.LineStyle, width int, theme tuikit.Theme, streaming bool) []RenderedRow {
	if streaming {
		return glamourStreamingNarrativeRows(blockID, raw, rolePrefix, lineStyle, width, theme)
	}
	return glamourNarrativeRows(blockID, raw, rolePrefix, lineStyle, width, theme)
}

// ---------------------------------------------------------------------------
// MainACPTurnBlock — root ACP-controlled turn in the main transcript.
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
}

type ToolUpdateMeta struct {
	TaskID          string
	ToolKind        string
	DisableGrouping bool
}

func NewMainACPTurnBlock(sessionID string) *MainACPTurnBlock {
	return &MainACPTurnBlock{
		id:        nextBlockID(),
		SessionID: strings.TrimSpace(sessionID),
		Status:    "running",
		StartedAt: time.Now(),
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
		b.Events[idx].Text = collapseRepeatedNarrativeText(appendDeltaStreamChunk(b.Events[idx].Text, chunk))
		markNarrativeTiming(&b.Events[idx], at)
		return
	}
	ev := SubagentEvent{Kind: kind, Text: collapseRepeatedNarrativeText(chunk)}
	markNarrativeTiming(&ev, at)
	b.Events = append(b.Events, ev)
}

func (b *MainACPTurnBlock) ReplaceFinalStreamChunk(kind SubagentEventKind, chunk string, occurredAt ...time.Time) {
	if b == nil {
		return
	}
	if strings.TrimSpace(chunk) == "" {
		return
	}
	at := narrativeEventTime(occurredAt...)
	if idx := latestNarrativeFinalTargetIndex(b.Events, kind); idx >= 0 {
		b.Events[idx].Text = collapseRepeatedNarrativeText(chunk)
		markNarrativeTiming(&b.Events[idx], at)
		return
	}
	ev := SubagentEvent{Kind: kind, Text: collapseRepeatedNarrativeText(chunk)}
	markNarrativeTiming(&ev, at)
	b.Events = append(b.Events, ev)
}

func (b *MainACPTurnBlock) UpdateTool(callID, name, args, output string, final bool, err bool) {
	b.UpdateToolWithMeta(callID, name, args, output, final, err, ToolUpdateMeta{})
}

func (b *MainACPTurnBlock) UpdateToolWithMeta(callID, name, args, output string, final bool, err bool, meta ToolUpdateMeta) {
	if b == nil {
		return
	}
	callID = strings.TrimSpace(callID)
	name = strings.TrimSpace(name)
	args = strings.TrimSpace(args)
	toolKind := strings.TrimSpace(meta.ToolKind)
	if !isTerminalPanelToolKind(name, toolKind) || final {
		output = strings.TrimSpace(output)
	}
	taskID := strings.TrimSpace(meta.TaskID)
	disableGrouping := meta.DisableGrouping
	if updateLinkedTerminalEvent(b.Events, toolSemanticName(name, toolKind), taskID, output) {
		output = ""
	}
	if !final {
		for i := len(b.Events) - 1; i >= 0; i-- {
			ev := &b.Events[i]
			if ev.Kind != SEToolCall || strings.TrimSpace(ev.CallID) != callID || ev.Done {
				continue
			}
			if strings.TrimSpace(ev.Name) == "" {
				ev.Name = name
			}
			if strings.TrimSpace(ev.ToolKind) == "" {
				ev.ToolKind = toolKind
			}
			if strings.TrimSpace(ev.Args) == "" {
				ev.Args = args
			}
			if ev.TaskID == "" {
				ev.TaskID = taskID
			}
			if disableGrouping {
				ev.DisableGrouping = true
			}
			if text := output; text != "" {
				ev.Output = mergeSubagentStreamChunk(ev.Output, text)
			}
			return
		}
		b.Events = append(b.Events, SubagentEvent{
			Kind:            SEToolCall,
			CallID:          callID,
			Name:            name,
			ToolKind:        toolKind,
			Args:            args,
			Output:          output,
			TaskID:          taskID,
			DisableGrouping: disableGrouping,
		})
		return
	}
	finalEvent := SubagentEvent{
		Kind:            SEToolCall,
		CallID:          callID,
		Name:            name,
		ToolKind:        toolKind,
		Args:            args,
		Output:          output,
		Done:            true,
		Err:             err,
		TaskID:          taskID,
		DisableGrouping: disableGrouping,
	}
	for i := len(b.Events) - 1; i >= 0; i-- {
		ev := &b.Events[i]
		if ev.Kind != SEToolCall || strings.TrimSpace(ev.CallID) != callID {
			continue
		}
		if !ev.Done {
			if strings.TrimSpace(finalEvent.Name) == "" {
				finalEvent.Name = strings.TrimSpace(ev.Name)
			}
			if strings.TrimSpace(finalEvent.Args) == "" {
				finalEvent.Args = strings.TrimSpace(ev.Args)
			}
			if strings.TrimSpace(finalEvent.ToolKind) == "" {
				finalEvent.ToolKind = strings.TrimSpace(ev.ToolKind)
			}
			ev.Name = finalEvent.Name
			ev.ToolKind = finalEvent.ToolKind
			ev.Args = finalEvent.Args
			ev.Output = finalEvent.Output
			ev.Done = true
			ev.Err = finalEvent.Err
			if ev.TaskID == "" {
				ev.TaskID = finalEvent.TaskID
			}
			if ev.DisableGrouping {
				finalEvent.DisableGrouping = true
			}
			ev.DisableGrouping = finalEvent.DisableGrouping
			if shouldDefaultCollapseToolEvent(finalEvent) {
				b.setToolPanelExpanded(callID, false)
			}
			return
		}
		if strings.TrimSpace(finalEvent.Name) == "" {
			finalEvent.Name = strings.TrimSpace(ev.Name)
		}
		if strings.TrimSpace(finalEvent.Args) == "" {
			finalEvent.Args = strings.TrimSpace(ev.Args)
		}
		if strings.TrimSpace(finalEvent.ToolKind) == "" {
			finalEvent.ToolKind = strings.TrimSpace(ev.ToolKind)
		}
		break
	}
	b.Events = append(b.Events, finalEvent)
	if shouldDefaultCollapseToolEvent(finalEvent) {
		b.setToolPanelExpanded(callID, false)
	}
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

func (b *MainACPTurnBlock) Render(ctx BlockRenderContext) []RenderedRow {
	if b == nil {
		return nil
	}
	return renderACPTranscriptRows(b.id, b.Events, b.Status, maxInt(8, ctx.Width), ctx, acpTranscriptRenderOptions{
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
}

func NewParticipantTurnBlock(sessionID, actor string) *ParticipantTurnBlock {
	return &ParticipantTurnBlock{
		id:        nextBlockID(),
		SessionID: strings.TrimSpace(sessionID),
		Actor:     strings.TrimSpace(actor),
		Status:    "running",
		Expanded:  true,
		StartedAt: time.Now(),
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
		b.Events[idx].Text = collapseRepeatedNarrativeText(appendDeltaStreamChunk(b.Events[idx].Text, chunk))
		markNarrativeTiming(&b.Events[idx], at)
		return
	}
	ev := SubagentEvent{Kind: kind, Text: collapseRepeatedNarrativeText(chunk)}
	markNarrativeTiming(&ev, at)
	b.Events = append(b.Events, ev)
}

func (b *ParticipantTurnBlock) ReplaceFinalStreamChunk(kind SubagentEventKind, chunk string, occurredAt ...time.Time) {
	if b == nil {
		return
	}
	if strings.TrimSpace(chunk) == "" {
		return
	}
	at := narrativeEventTime(occurredAt...)
	if idx := latestNarrativeFinalTargetIndex(b.Events, kind); idx >= 0 {
		b.Events[idx].Text = collapseRepeatedNarrativeText(chunk)
		markNarrativeTiming(&b.Events[idx], at)
		return
	}
	ev := SubagentEvent{Kind: kind, Text: collapseRepeatedNarrativeText(chunk)}
	markNarrativeTiming(&ev, at)
	b.Events = append(b.Events, ev)
}

func (b *ParticipantTurnBlock) UpdateTool(callID, name, args, output string, final bool, err bool) {
	b.UpdateToolWithMeta(callID, name, args, output, final, err, ToolUpdateMeta{})
}

func (b *ParticipantTurnBlock) UpdateToolWithMeta(callID, name, args, output string, final bool, err bool, meta ToolUpdateMeta) {
	if b == nil {
		return
	}
	callID = strings.TrimSpace(callID)
	name = strings.TrimSpace(name)
	args = strings.TrimSpace(args)
	toolKind := strings.TrimSpace(meta.ToolKind)
	if !isTerminalPanelToolKind(name, toolKind) || final {
		output = strings.TrimSpace(output)
	}
	taskID := strings.TrimSpace(meta.TaskID)
	disableGrouping := meta.DisableGrouping
	if updateLinkedTerminalEvent(b.Events, toolSemanticName(name, toolKind), taskID, output) {
		output = ""
	}
	if !final {
		for i := len(b.Events) - 1; i >= 0; i-- {
			ev := &b.Events[i]
			if ev.Kind != SEToolCall || strings.TrimSpace(ev.CallID) != callID || ev.Done {
				continue
			}
			if strings.TrimSpace(ev.Name) == "" {
				ev.Name = name
			}
			if strings.TrimSpace(ev.ToolKind) == "" {
				ev.ToolKind = toolKind
			}
			if strings.TrimSpace(ev.Args) == "" {
				ev.Args = args
			}
			if ev.TaskID == "" {
				ev.TaskID = taskID
			}
			if disableGrouping {
				ev.DisableGrouping = true
			}
			if text := output; text != "" {
				ev.Output = mergeSubagentStreamChunk(ev.Output, text)
			}
			return
		}
		b.Events = append(b.Events, SubagentEvent{
			Kind:            SEToolCall,
			CallID:          callID,
			Name:            name,
			ToolKind:        toolKind,
			Args:            args,
			Output:          output,
			TaskID:          taskID,
			DisableGrouping: disableGrouping,
		})
		return
	}
	finalEvent := SubagentEvent{
		Kind:            SEToolCall,
		CallID:          callID,
		Name:            name,
		ToolKind:        toolKind,
		Args:            args,
		Output:          output,
		Done:            true,
		Err:             err,
		TaskID:          taskID,
		DisableGrouping: disableGrouping,
	}
	for i := len(b.Events) - 1; i >= 0; i-- {
		ev := &b.Events[i]
		if ev.Kind != SEToolCall || strings.TrimSpace(ev.CallID) != callID {
			continue
		}
		if !ev.Done {
			if strings.TrimSpace(finalEvent.Name) == "" {
				finalEvent.Name = strings.TrimSpace(ev.Name)
			}
			if strings.TrimSpace(finalEvent.Args) == "" {
				finalEvent.Args = strings.TrimSpace(ev.Args)
			}
			if strings.TrimSpace(finalEvent.ToolKind) == "" {
				finalEvent.ToolKind = strings.TrimSpace(ev.ToolKind)
			}
			ev.Name = finalEvent.Name
			ev.ToolKind = finalEvent.ToolKind
			ev.Args = finalEvent.Args
			ev.Output = finalEvent.Output
			ev.Done = true
			ev.Err = finalEvent.Err
			if ev.TaskID == "" {
				ev.TaskID = finalEvent.TaskID
			}
			if ev.DisableGrouping {
				finalEvent.DisableGrouping = true
			}
			ev.DisableGrouping = finalEvent.DisableGrouping
			if shouldDefaultCollapseToolEvent(finalEvent) {
				b.setToolPanelExpanded(callID, false)
			}
			return
		}
		if strings.TrimSpace(finalEvent.Name) == "" {
			finalEvent.Name = strings.TrimSpace(ev.Name)
		}
		if strings.TrimSpace(finalEvent.Args) == "" {
			finalEvent.Args = strings.TrimSpace(ev.Args)
		}
		if strings.TrimSpace(finalEvent.ToolKind) == "" {
			finalEvent.ToolKind = strings.TrimSpace(ev.ToolKind)
		}
		break
	}
	b.Events = append(b.Events, finalEvent)
	if shouldDefaultCollapseToolEvent(finalEvent) {
		b.setToolPanelExpanded(callID, false)
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

func (b *ParticipantTurnBlock) Render(ctx BlockRenderContext) []RenderedRow {
	if b == nil {
		return nil
	}
	rows := []RenderedRow{StyledRow(b.id, renderParticipantTurnHeader(b, ctx))}
	if !b.Expanded {
		return rows
	}
	rows = append(rows, renderACPTranscriptRows(b.id, b.Events, b.Status, maxInt(8, ctx.Width), ctx, acpTranscriptRenderOptions{
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
	})...)
	if b.Expanded && participantTurnIsTerminal(b.Status) {
		rows = append(rows, StyledRow(b.id, renderParticipantTurnFooter(b, ctx)))
	}
	return rows
}

func renderParticipantTurnHeader(b *ParticipantTurnBlock, ctx BlockRenderContext) string {
	if b == nil {
		return ""
	}
	icon := "▾"
	if !b.Expanded {
		icon = "▸"
	}
	iconText := ctx.Theme.PromptStyle().Bold(true).Render(icon)
	actor := renderParticipantActorLabel(ctx.Theme, b.Actor)
	left := iconText + " " + actor
	switch strings.ToLower(strings.TrimSpace(b.Status)) {
	case "waiting_approval":
		left = ctx.Theme.WarnStyle().Bold(true).Render(icon) + " " + actor
	case "failed":
		left = ctx.Theme.ErrorStyle().Bold(true).Render(icon) + " " + actor
	case "interrupted":
		left = ctx.Theme.WarnStyle().Bold(true).Render(icon) + " " + actor
	}
	metaParts := make([]string, 0, 1)
	if label := participantTurnStatusLabel(b.Status); label != "" {
		metaParts = append(metaParts, label)
	}
	if len(metaParts) == 0 {
		return left
	}
	return left + " " + ctx.Theme.TranscriptMetaStyle().Render("· "+strings.Join(metaParts, " · "))
}

func toolPanelExpanded(state map[string]bool, callID string) bool {
	callID = strings.TrimSpace(callID)
	if callID == "" || state == nil {
		return true
	}
	expanded, ok := state[callID]
	if !ok {
		return true
	}
	return expanded
}

func toolPanelFullOutput(state map[string]bool, callID string) bool {
	callID = strings.TrimSpace(callID)
	if callID == "" || state == nil {
		return false
	}
	return state[callID]
}

func toggleToolPanelExpanded(state *map[string]bool, callID string) bool {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return false
	}
	if *state == nil {
		*state = map[string]bool{}
	}
	next := !toolPanelExpanded(*state, callID)
	(*state)[callID] = next
	return next
}

func toggleToolPanelClick(expandedState *map[string]bool, fullOutputState *map[string]bool, events []SubagentEvent, callID string) bool {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return false
	}
	if !toolPanelExpanded(mapValue(expandedState), callID) {
		setToolPanelExpandedState(expandedState, callID, true)
		return true
	}
	if toolPanelHasHiddenSummary(events, callID) {
		if fullOutputState == nil {
			return false
		}
		if *fullOutputState == nil {
			*fullOutputState = map[string]bool{}
		}
		(*fullOutputState)[callID] = !(*fullOutputState)[callID]
		return true
	}
	return toggleToolPanelExpanded(expandedState, callID)
}

func mapValue(ptr *map[string]bool) map[string]bool {
	if ptr == nil {
		return nil
	}
	return *ptr
}

func setToolPanelExpandedState(state *map[string]bool, callID string, expanded bool) {
	if state == nil || strings.TrimSpace(callID) == "" {
		return
	}
	if *state == nil {
		*state = map[string]bool{}
	}
	(*state)[strings.TrimSpace(callID)] = expanded
}

type toolPanelScrollState struct {
	Offset                int
	FollowTail            bool
	ScrollbarVisibleUntil time.Time
}

func defaultToolPanelScrollState() toolPanelScrollState {
	return toolPanelScrollState{FollowTail: true}
}

func toolPanelScrollStateFromMap(state map[string]toolPanelScrollState, callID string) toolPanelScrollState {
	callID = strings.TrimSpace(callID)
	if callID == "" || state == nil {
		return defaultToolPanelScrollState()
	}
	value, ok := state[callID]
	if !ok {
		return defaultToolPanelScrollState()
	}
	return value
}

func scrollToolPanelState(state *map[string]toolPanelScrollState, callID string, total int, delta int) bool {
	callID = strings.TrimSpace(callID)
	if state == nil || callID == "" {
		return false
	}
	value := defaultToolPanelScrollState()
	if *state != nil {
		value = toolPanelScrollStateFromMap(*state, callID)
	}
	if !scrollPanelState(&value.Offset, &value.FollowTail, total, acpTerminalPanelMaxLines, delta) {
		return false
	}
	value.ScrollbarVisibleUntil = time.Now().Add(scrollbarVisibleDuration)
	if *state == nil {
		*state = map[string]toolPanelScrollState{}
	}
	(*state)[callID] = value
	return true
}

func (b *MainACPTurnBlock) toolPanelExpanded(callID string) bool {
	if b == nil {
		return true
	}
	return toolPanelExpanded(b.ExpandedTools, callID)
}

func (b *MainACPTurnBlock) toolPanelFullOutput(callID string) bool {
	if b == nil {
		return false
	}
	return toolPanelFullOutput(b.ExpandedToolOutput, callID)
}

func (b *MainACPTurnBlock) renderToolPanelRows(request toolPanelRenderRequest) []RenderedRow {
	if b == nil {
		return request.renderUncached()
	}
	return renderCachedToolPanelRows(&b.toolPanelRenderCache, request, b.toolPanelScrollState(request.CallID))
}

func (b *MainACPTurnBlock) toggleToolPanelExpanded(callID string) bool {
	if b == nil {
		return false
	}
	return toggleToolPanelExpanded(&b.ExpandedTools, callID)
}

func (b *MainACPTurnBlock) toggleToolPanelClick(callID string) bool {
	if b == nil {
		return false
	}
	return toggleToolPanelClick(&b.ExpandedTools, &b.ExpandedToolOutput, b.Events, callID)
}

func (b *MainACPTurnBlock) setToolPanelExpanded(callID string, expanded bool) {
	if b == nil || strings.TrimSpace(callID) == "" {
		return
	}
	if b.ExpandedTools == nil {
		b.ExpandedTools = map[string]bool{}
	}
	b.ExpandedTools[strings.TrimSpace(callID)] = expanded
	if !expanded && b.ExpandedToolOutput != nil {
		delete(b.ExpandedToolOutput, strings.TrimSpace(callID))
	}
}

func (b *MainACPTurnBlock) reasoningExpanded(key string) bool {
	if b == nil || strings.TrimSpace(key) == "" || b.ExpandedThought == nil {
		return false
	}
	return b.ExpandedThought[strings.TrimSpace(key)]
}

func (b *MainACPTurnBlock) toggleReasoningExpanded(key string) bool {
	key = strings.TrimSpace(key)
	if b == nil || key == "" {
		return false
	}
	if b.ExpandedThought == nil {
		b.ExpandedThought = map[string]bool{}
	}
	next := !b.ExpandedThought[key]
	b.ExpandedThought[key] = next
	return true
}

func (b *MainACPTurnBlock) explorationExpanded(key string) bool {
	if b == nil || strings.TrimSpace(key) == "" || b.ExpandedExplore == nil {
		return false
	}
	return b.ExpandedExplore[strings.TrimSpace(key)]
}

func (b *MainACPTurnBlock) toggleExplorationExpanded(key string) bool {
	key = strings.TrimSpace(key)
	if b == nil || key == "" {
		return false
	}
	if b.ExpandedExplore == nil {
		b.ExpandedExplore = map[string]bool{}
	}
	b.ExpandedExplore[key] = !b.ExpandedExplore[key]
	return true
}

func (b *MainACPTurnBlock) toolPanelScrollState(callID string) toolPanelScrollState {
	if b == nil {
		return defaultToolPanelScrollState()
	}
	return toolPanelScrollStateFromMap(b.ToolPanelScroll, callID)
}

func (b *MainACPTurnBlock) ScrollToolPanel(callID string, delta int, ctx BlockRenderContext) bool {
	return false
}

func (b *MainACPTurnBlock) CanScrollToolPanel(callID string, delta int, ctx BlockRenderContext) bool {
	return false
}

func (b *MainACPTurnBlock) collapseAllToolPanels() {
	if b == nil {
		return
	}
	b.ExpandedTools = collapseToolPanelsForEvents(b.ExpandedTools, b.Events)
}

func (b *ParticipantTurnBlock) toolPanelExpanded(callID string) bool {
	if b == nil {
		return true
	}
	return toolPanelExpanded(b.ExpandedTools, callID)
}

func (b *ParticipantTurnBlock) toolPanelFullOutput(callID string) bool {
	if b == nil {
		return false
	}
	return toolPanelFullOutput(b.ExpandedToolOutput, callID)
}

func (b *ParticipantTurnBlock) renderToolPanelRows(request toolPanelRenderRequest) []RenderedRow {
	if b == nil {
		return request.renderUncached()
	}
	return renderCachedToolPanelRows(&b.toolPanelRenderCache, request, b.toolPanelScrollState(request.CallID))
}

func (b *ParticipantTurnBlock) toggleToolPanelExpanded(callID string) bool {
	if b == nil {
		return false
	}
	return toggleToolPanelExpanded(&b.ExpandedTools, callID)
}

func (b *ParticipantTurnBlock) toggleToolPanelClick(callID string) bool {
	if b == nil {
		return false
	}
	return toggleToolPanelClick(&b.ExpandedTools, &b.ExpandedToolOutput, b.Events, callID)
}

func (b *ParticipantTurnBlock) setToolPanelExpanded(callID string, expanded bool) {
	if b == nil || strings.TrimSpace(callID) == "" {
		return
	}
	if b.ExpandedTools == nil {
		b.ExpandedTools = map[string]bool{}
	}
	b.ExpandedTools[strings.TrimSpace(callID)] = expanded
	if !expanded && b.ExpandedToolOutput != nil {
		delete(b.ExpandedToolOutput, strings.TrimSpace(callID))
	}
}

func (b *ParticipantTurnBlock) reasoningExpanded(key string) bool {
	if b == nil || strings.TrimSpace(key) == "" || b.ExpandedThought == nil {
		return false
	}
	return b.ExpandedThought[strings.TrimSpace(key)]
}

func (b *ParticipantTurnBlock) toggleReasoningExpanded(key string) bool {
	key = strings.TrimSpace(key)
	if b == nil || key == "" {
		return false
	}
	if b.ExpandedThought == nil {
		b.ExpandedThought = map[string]bool{}
	}
	next := !b.ExpandedThought[key]
	b.ExpandedThought[key] = next
	return true
}

func (b *ParticipantTurnBlock) explorationExpanded(key string) bool {
	if b == nil || strings.TrimSpace(key) == "" || b.ExpandedExplore == nil {
		return false
	}
	return b.ExpandedExplore[strings.TrimSpace(key)]
}

func (b *ParticipantTurnBlock) toggleExplorationExpanded(key string) bool {
	key = strings.TrimSpace(key)
	if b == nil || key == "" {
		return false
	}
	if b.ExpandedExplore == nil {
		b.ExpandedExplore = map[string]bool{}
	}
	b.ExpandedExplore[key] = !b.ExpandedExplore[key]
	return true
}

func (b *ParticipantTurnBlock) toolPanelScrollState(callID string) toolPanelScrollState {
	if b == nil {
		return defaultToolPanelScrollState()
	}
	return toolPanelScrollStateFromMap(b.ToolPanelScroll, callID)
}

func (b *ParticipantTurnBlock) ScrollToolPanel(callID string, delta int, ctx BlockRenderContext) bool {
	return false
}

func (b *ParticipantTurnBlock) CanScrollToolPanel(callID string, delta int, ctx BlockRenderContext) bool {
	return false
}

func (b *ParticipantTurnBlock) collapseAllToolPanels() {
	if b == nil {
		return
	}
	b.ExpandedTools = collapseToolPanelsForEvents(b.ExpandedTools, b.Events)
}

func collapseToolPanelsForEvents(state map[string]bool, events []SubagentEvent) map[string]bool {
	callIDs := collectToolPanelCallIDs(events)
	if len(callIDs) == 0 {
		return state
	}
	if state == nil {
		state = map[string]bool{}
	}
	for _, callID := range callIDs {
		if !shouldDefaultCollapseCallID(events, callID) {
			continue
		}
		state[callID] = false
	}
	return state
}

func shouldDefaultCollapseCallID(events []SubagentEvent, callID string) bool {
	var name string
	for _, ev := range events {
		if ev.Kind != SEToolCall || strings.TrimSpace(ev.CallID) != strings.TrimSpace(callID) {
			continue
		}
		if ev.DisableGrouping {
			return false
		}
		if name == "" {
			name = strings.TrimSpace(ev.Name)
		}
	}
	return shouldDefaultCollapseToolPanel(name)
}

func toolPanelHasHiddenSummary(events []SubagentEvent, callID string) bool {
	final, ok := finalToolEventForCallID(events, callID)
	if !ok {
		return false
	}
	if !final.DisableGrouping && !isTerminalPanelToolEvent(final) {
		return false
	}
	return len(nonEmptyToolOutputLines(final.Output)) > acpTerminalPanelMaxLines
}

func finalToolEventForCallID(events []SubagentEvent, callID string) (SubagentEvent, bool) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return SubagentEvent{}, false
	}
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Kind != SEToolCall || strings.TrimSpace(ev.CallID) != callID {
			continue
		}
		if ev.Done {
			return ev, true
		}
		return SubagentEvent{}, false
	}
	return SubagentEvent{}, false
}

func collectToolPanelCallIDs(events []SubagentEvent) []string {
	if len(events) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	callIDs := make([]string, 0, len(events))
	for _, ev := range events {
		if ev.Kind != SEToolCall {
			continue
		}
		callID := strings.TrimSpace(ev.CallID)
		if callID == "" {
			continue
		}
		if _, ok := seen[callID]; ok {
			continue
		}
		seen[callID] = struct{}{}
		callIDs = append(callIDs, callID)
	}
	return callIDs
}

func shouldDefaultCollapseToolPanel(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "READ", "RG", "LIST", "GLOB", "SEARCH", "FIND":
		return true
	default:
		return false
	}
}

func shouldDefaultCollapseToolEvent(ev SubagentEvent) bool {
	if ev.DisableGrouping {
		return false
	}
	return shouldDefaultCollapseToolPanel(ev.Name)
}

func participantTurnStatusLabel(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "", "running", "initializing", "prompting", "completed":
		return ""
	case "waiting_approval":
		return "waiting approval"
	case "failed":
		return "failed"
	case "interrupted":
		return "interrupted"
	default:
		return strings.TrimSpace(state)
	}
}

func participantNarrativeEventActive(events []SubagentEvent, idx int, status string) bool {
	return narrativeEventActive(events, idx, participantTurnIsTerminal(status))
}

func renderParticipantTurnNarrativeRows(blockID string, raw string, lineStyle tuikit.LineStyle, width int, ctx BlockRenderContext, active bool) []RenderedRow {
	rolePrefix, _ := narrativeLinePrefixes(lineStyle)
	return renderNarrativeRows(blockID, raw, rolePrefix, lineStyle, width, ctx.Theme, active)
}

func renderParticipantTurnToolRows(blockID string, ev SubagentEvent, width int, ctx BlockRenderContext) []RenderedRow {
	return renderToolEventViewModelLines(blockID, buildToolEventViewModel(ev), width, ctx.Theme)
}

func collapseRepeatedNarrativeText(text string) string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	if strings.TrimSpace(text) == "" {
		return text
	}
	parts := strings.Split(text, "\n\n")
	filteredParts := make([]string, 0, len(parts))
	lastPart := ""
	for _, part := range parts {
		part = collapseAdjacentDuplicateLines(part)
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		if trimmed == lastPart && len([]rune(trimmed)) >= 16 {
			continue
		}
		filteredParts = append(filteredParts, part)
		lastPart = trimmed
	}
	if len(filteredParts) == 0 {
		return ""
	}
	return strings.Join(filteredParts, "\n\n")
}

func collapseAdjacentDuplicateLines(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	last := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && trimmed == last && len([]rune(trimmed)) >= 16 {
			continue
		}
		out = append(out, line)
		if trimmed != "" {
			last = trimmed
		}
	}
	return strings.Join(out, "\n")
}

func latestNarrativeAppendTargetIndex(events []SubagentEvent, kind SubagentEventKind) int {
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Kind == kind {
			return i
		}
		if narrativeStreamBarrier(ev) {
			return -1
		}
	}
	return -1
}

func latestNarrativeFinalTargetIndex(events []SubagentEvent, kind SubagentEventKind) int {
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Kind == kind {
			return i
		}
		if narrativeFinalBarrier(ev) {
			return -1
		}
	}
	return -1
}

func narrativeStreamBarrier(ev SubagentEvent) bool {
	switch ev.Kind {
	case SEApproval, SEAssistant, SEReasoning:
		return false
	default:
		return true
	}
}

func narrativeFinalBarrier(ev SubagentEvent) bool {
	switch ev.Kind {
	case SEApproval, SEAssistant, SEReasoning:
		return false
	default:
		return true
	}
}

func participantTurnIsTerminal(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "completed", "failed", "interrupted", "cancelled", "canceled", "terminated":
		return true
	default:
		return false
	}
}

func renderParticipantTurnFooter(b *ParticipantTurnBlock, ctx BlockRenderContext) string {
	label := ""
	if b != nil && !b.StartedAt.IsZero() && !b.EndedAt.IsZero() && !b.EndedAt.Before(b.StartedAt) {
		label = formatTurnDuration(b.EndedAt.Sub(b.StartedAt))
	}
	width := maxInt(12, ctx.Width)
	return ctx.Theme.HelpHintTextStyle().Render(centeredDivider(width, label))
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
		return "* ", "  "
	case tuikit.LineStyleReasoning:
		return "· ", "  "
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
	output := strings.TrimSpace(ev.Output)
	if output == "" || strings.EqualFold(output, "completed") {
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

func updateLinkedTerminalEvent(events []SubagentEvent, toolName string, taskID string, output string) bool {
	if !strings.EqualFold(strings.TrimSpace(toolName), "TASK") {
		return false
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || output == "" {
		return false
	}
	for i := len(events) - 1; i >= 0; i-- {
		ev := &events[i]
		if ev.Kind != SEToolCall || strings.TrimSpace(ev.TaskID) != taskID || !isTerminalPanelToolEvent(*ev) {
			continue
		}
		ev.Output = output
		return true
	}
	return false
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

// ---------------------------------------------------------------------------
// SubagentPanelBlock — SPAWN child ACP session viewer.
// ---------------------------------------------------------------------------

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
	Output          string
	TaskID          string
	Done            bool
	Err             bool
	DisableGrouping bool

	// Plan fields.
	PlanEntries []planEntryState

	// Approval fields (derived from context when status becomes waiting_approval).
	ApprovalTool    string
	ApprovalCommand string
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
	eventsGen uint64
}

func NewSubagentSessionState(spawnID, attachID, agent string) *SubagentSessionState {
	return &SubagentSessionState{
		SpawnID:   strings.TrimSpace(spawnID),
		AttachID:  strings.TrimSpace(attachID),
		Agent:     strings.TrimSpace(agent),
		Status:    "running",
		StartedAt: time.Now(),
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
		s.eventsGen++
		return
	}
	ev := SubagentEvent{Kind: kind, Text: collapseRepeatedNarrativeText(chunk)}
	markNarrativeTiming(&ev, at)
	s.Events = append(s.Events, ev)
	s.eventsGen++
}

func (s *SubagentSessionState) UpdateToolCall(callID, toolName, args, stream, chunk string, final bool) {
	s.UpdateToolCallWithMeta(callID, toolName, args, stream, chunk, final, ToolUpdateMeta{})
}

func (s *SubagentSessionState) UpdateToolCallWithMeta(callID, toolName, args, stream, chunk string, final bool, meta ToolUpdateMeta) {
	if s == nil {
		return
	}
	callID = strings.TrimSpace(callID)
	toolName = strings.TrimSpace(toolName)
	args = strings.TrimSpace(args)
	stream = strings.ToLower(strings.TrimSpace(stream))
	chunk = normalizeSubagentChunkBoundary("", chunk)
	toolKind := strings.TrimSpace(meta.ToolKind)
	taskID := strings.TrimSpace(meta.TaskID)
	disableGrouping := meta.DisableGrouping
	if updateLinkedTerminalEvent(s.Events, toolSemanticName(toolName, toolKind), taskID, chunk) {
		chunk = ""
	}
	if !final {
		for i := len(s.Events) - 1; i >= 0; i-- {
			e := &s.Events[i]
			if e.Kind != SEToolCall || e.CallID != callID || e.Done || e.Err {
				continue
			}
			if strings.TrimSpace(e.Name) == "" {
				e.Name = toolName
			}
			if strings.TrimSpace(e.ToolKind) == "" {
				e.ToolKind = toolKind
			}
			if strings.TrimSpace(e.Args) == "" {
				e.Args = args
			}
			if e.TaskID == "" {
				e.TaskID = taskID
			}
			if disableGrouping {
				e.DisableGrouping = true
			}
			if chunk != "" {
				e.Output = mergeSubagentStreamChunk(e.Output, chunk)
			}
			s.eventsGen++
			return
		}
		s.Events = append(s.Events, SubagentEvent{
			Kind:            SEToolCall,
			Name:            toolName,
			ToolKind:        toolKind,
			CallID:          callID,
			Args:            args,
			Output:          chunk,
			TaskID:          taskID,
			DisableGrouping: disableGrouping,
		})
		s.eventsGen++
		return
	}

	finalEvent := SubagentEvent{
		Kind:            SEToolCall,
		Name:            toolName,
		ToolKind:        toolKind,
		CallID:          callID,
		Args:            args,
		Output:          chunk,
		Done:            true,
		Err:             stream == "stderr",
		TaskID:          taskID,
		DisableGrouping: disableGrouping,
	}
	for i := len(s.Events) - 1; i >= 0; i-- {
		e := &s.Events[i]
		if e.Kind != SEToolCall || e.CallID != callID {
			continue
		}
		if !e.Done {
			if strings.TrimSpace(finalEvent.Name) == "" {
				finalEvent.Name = e.Name
			}
			if strings.TrimSpace(finalEvent.Args) == "" {
				finalEvent.Args = e.Args
			}
			if strings.TrimSpace(finalEvent.ToolKind) == "" {
				finalEvent.ToolKind = strings.TrimSpace(e.ToolKind)
			}
			e.Name = finalEvent.Name
			e.ToolKind = finalEvent.ToolKind
			e.Args = finalEvent.Args
			e.Output = finalEvent.Output
			e.Done = true
			e.Err = finalEvent.Err
			if e.TaskID == "" {
				e.TaskID = finalEvent.TaskID
			}
			if e.DisableGrouping {
				finalEvent.DisableGrouping = true
			}
			e.DisableGrouping = finalEvent.DisableGrouping
			s.eventsGen++
			return
		}
		if strings.TrimSpace(finalEvent.Name) == "" {
			finalEvent.Name = e.Name
		}
		if strings.TrimSpace(finalEvent.Args) == "" {
			finalEvent.Args = e.Args
		}
		if strings.TrimSpace(finalEvent.ToolKind) == "" {
			finalEvent.ToolKind = strings.TrimSpace(e.ToolKind)
		}
		break
	}
	s.Events = append(s.Events, finalEvent)
	s.eventsGen++
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

	const stableReplayThreshold = 12
	if runeCount(existing) >= stableReplayThreshold && strings.HasPrefix(incoming, existing) {
		return incoming
	}
	if runeCount(incoming) >= stableReplayThreshold && strings.HasPrefix(existing, incoming) {
		return existing
	}
	if suffix := overlappingSubagentSuffix(existing, incoming, 6); suffix != incoming {
		return existing + suffix
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

func overlappingSubagentSuffix(existing string, incoming string, minOverlap int) string {
	existingRunes := []rune(existing)
	incomingRunes := []rune(incoming)
	limit := minInt(len(existingRunes), len(incomingRunes))
	for overlap := limit; overlap >= minOverlap; overlap-- {
		if string(existingRunes[len(existingRunes)-overlap:]) == string(incomingRunes[:overlap]) {
			return string(incomingRunes[overlap:])
		}
	}
	return incoming
}

func runeCount(text string) int {
	return len([]rune(text))
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
