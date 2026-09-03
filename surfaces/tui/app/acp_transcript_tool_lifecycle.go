package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/surfaces/internal/transcript"
)

func renderACPToolLifecycleRows(blockID string, events []SubagentEvent, idx int, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) ([]RenderedRow, int) {
	if idx < 0 || idx >= len(events) {
		return nil, idx
	}
	ev := events[idx]
	if ev.Kind != SEToolCall {
		return nil, idx
	}
	callID := strings.TrimSpace(ev.CallID)
	if callID == "" {
		if !shouldRenderToolEvent(ev) {
			return nil, idx
		}
		return renderACPStandardToolCollapsedRows(blockID, ev, "", width, ctx, ev.Err, ev.Done, ""), idx
	}

	end := idx
	for end+1 < len(events) {
		next := events[end+1]
		if next.Kind != SEToolCall || strings.TrimSpace(next.CallID) != callID {
			break
		}
		end++
	}

	group := events[idx : end+1]
	start := group[0]
	singleCompletedLifecycle := len(group) == 1 && start.Done && toolEventHasPresentation(start)
	if start.Done && len(group) > 1 {
		start = SubagentEvent{}
		for _, item := range group {
			if !item.Done {
				start = item
				break
			}
		}
		if start.Kind == 0 && start.CallID == "" && start.Name == "" {
			start = group[0]
		}
	}

	var final SubagentEvent
	var preview string
	settled := false
	hasStart := (!start.Done || singleCompletedLifecycle) && toolEventHasPresentation(start)
	hasFinal := false
	for _, item := range group {
		if !item.Done {
			if renderableTextHasContent(item.Output) {
				preview = item.Output
			}
			continue
		}
		settled = true
		if !toolEventHasPresentation(item) || !shouldRenderToolEvent(item) {
			continue
		}
		final = item
		hasFinal = true
	}
	if singleCompletedLifecycle {
		final = start
		hasFinal = shouldRenderToolEvent(final)
		start.Done = false
		start.Output = ""
	}

	if !hasStart {
		if hasFinal {
			return renderACPStandaloneFinalToolRows(blockID, final, width, ctx, opts), end
		}
		if toolEventHasPresentation(ev) && shouldRenderToolEvent(ev) {
			return renderACPStandardToolCollapsedRows(blockID, ev, callID, width, ctx, ev.Err, ev.Done, ""), end
		}
		return nil, end
	}

	spawnHeader := toolLifecycleHeaderEvent(start, final, hasFinal, settled)
	if opts.SubagentOutputLinks && isSpawnToolEvent(spawnHeader) {
		return renderACPSpawnToolRows(blockID, spawnHeader, callID, width, ctx), end
	}

	if isTerminalPanelToolEvent(start) {
		start.Args = normalizeACPToolInline(start.Args)
	} else {
		start.Args = compactACPToolInline(start.Args, width)
	}
	headerEvent := toolLifecycleHeaderEvent(start, final, hasFinal, settled)
	panelExpanded := true
	if opts.ToolPanelExpanded != nil {
		panelExpanded = opts.ToolPanelExpanded(start.CallID)
	}
	fullOutput := false
	if opts.ToolPanelFullOutput != nil {
		fullOutput = opts.ToolPanelFullOutput(start.CallID)
	}
	if opts.ToolOutputPanels {
		panelText, panelErr := acpToolPanelText(preview, final, hasFinal)
		if isTaskWriteInteractionEvent(headerEvent) {
			return renderACPStandardToolLifecycleRows(blockID, headerEvent, callID, panelText, width, ctx, panelErr, settled, fullOutput), end
		}
		if headerEvent.Name == surfaceToolSendMessage {
			return renderACPTerminalLifecycleRows(blockID, headerEvent, callID, panelText, width, ctx, panelErr, panelExpanded, settled, fullOutput, opts), end
		}
		if isTerminalPanelToolEvent(start) {
			return renderACPTerminalLifecycleRows(blockID, headerEvent, callID, panelText, width, ctx, panelErr, panelExpanded, settled, fullOutput, opts), end
		}
		if isMutationPanelToolEvent(start) {
			return renderACPMutationLifecycleRows(blockID, headerEvent, callID, panelText, width, ctx, panelErr, panelExpanded, settled, opts), end
		}
		if hasFinal && shouldDefaultCollapseToolEvent(final) && !panelExpanded {
			token := acpStandardCollapsedClickToken(callID, final, panelText, panelErr)
			return renderACPStandardToolCollapsedRows(blockID, headerEvent, callID, width, ctx, final.Err, true, token), end
		}
		if !hasFinal && shouldDefaultCollapseToolEvent(start) && !shouldRenderACPToolPanel(panelText, panelErr) {
			return renderACPStandardToolCollapsedRows(blockID, headerEvent, callID, width, ctx, panelErr, settled, ""), end
		}
		if !shouldRenderACPToolPanel(panelText, panelErr) {
			return renderACPStandardToolCollapsedRows(blockID, headerEvent, callID, width, ctx, panelErr, settled, ""), end
		}
		if !panelExpanded {
			token := acpStandardCollapsedClickToken(callID, headerEvent, panelText, panelErr)
			return renderACPStandardToolCollapsedRows(blockID, headerEvent, callID, width, ctx, panelErr, settled, token), end
		}
		return renderACPStandardToolLifecycleRows(blockID, headerEvent, callID, panelText, width, ctx, panelErr, settled, fullOutput), end
	}
	rows := renderACPStandardToolCollapsedRows(blockID, headerEvent, callID, width, ctx, false, settled, "")
	if text := sanitizeRenderableText(preview); text != "" {
		rows = append(rows, renderACPToolDetailRows(blockID, "· ", text, width, ctx, ctx.Theme.HelpHintTextStyle())...)
	}
	if hasFinal {
		prefix := "✓ "
		style := ctx.Theme.HelpHintTextStyle()
		if final.Err {
			prefix = "✗ "
			style = ctx.Theme.ToolErrorStyle()
		}
		text := sanitizeRenderableText(final.Output)
		if !renderableTextHasContent(text) && !final.Err {
			text = "completed"
		}
		if renderableTextHasContent(text) {
			rows = append(rows, renderACPToolDetailRows(blockID, prefix, text, width, ctx, style)...)
		}
	}
	return rows, end
}

func renderACPStandaloneFinalToolRows(blockID string, ev SubagentEvent, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) []RenderedRow {
	if opts.SubagentOutputLinks && isSpawnToolEvent(ev) {
		return renderACPSpawnToolRows(blockID, ev, ev.CallID, width, ctx)
	}
	output := sanitizeRenderableText(ev.Output)
	if opts.ToolOutputPanels && isTaskWriteInteractionEvent(ev) {
		fullOutput := false
		if opts.ToolPanelFullOutput != nil {
			fullOutput = opts.ToolPanelFullOutput(ev.CallID)
		}
		return renderACPStandardToolLifecycleRows(blockID, ev, ev.CallID, output, width, ctx, ev.Err, true, fullOutput)
	}
	if opts.ToolOutputPanels && ev.Name == surfaceToolSendMessage {
		panelExpanded := true
		if opts.ToolPanelExpanded != nil {
			panelExpanded = opts.ToolPanelExpanded(ev.CallID)
		}
		fullOutput := false
		if opts.ToolPanelFullOutput != nil {
			fullOutput = opts.ToolPanelFullOutput(ev.CallID)
		}
		return renderACPTerminalLifecycleRows(blockID, ev, ev.CallID, output, width, ctx, ev.Err, panelExpanded, true, fullOutput, opts)
	}
	if opts.ToolOutputPanels && shouldRenderACPToolPanel(output, ev.Err) {
		panelExpanded := true
		if opts.ToolPanelExpanded != nil {
			panelExpanded = opts.ToolPanelExpanded(ev.CallID)
		}
		fullOutput := false
		if opts.ToolPanelFullOutput != nil {
			fullOutput = opts.ToolPanelFullOutput(ev.CallID)
		}
		if isTerminalPanelToolEvent(ev) {
			return renderACPTerminalLifecycleRows(blockID, ev, ev.CallID, output, width, ctx, ev.Err, panelExpanded, true, fullOutput, opts)
		}
		if isMutationPanelToolEvent(ev) {
			return renderACPMutationLifecycleRows(blockID, ev, ev.CallID, output, width, ctx, ev.Err, panelExpanded, true, opts)
		}
		if !panelExpanded {
			token := acpStandardCollapsedClickToken(ev.CallID, ev, output, ev.Err)
			return renderACPStandardToolCollapsedRows(blockID, ev, ev.CallID, width, ctx, ev.Err, true, token)
		}
		return renderACPStandardToolLifecycleRows(blockID, ev, ev.CallID, output, width, ctx, ev.Err, true, fullOutput)
	}
	if !renderableTextHasContent(output) || (!strings.Contains(output, "\n") && displayColumns(output) <= maxInt(24, width/2)) {
		return renderACPStandardToolCollapsedRows(blockID, ev, ev.CallID, width, ctx, ev.Err, ev.Done, "")
	}
	header := SubagentEvent{
		Kind:     SEToolCall,
		Name:     ev.Name,
		ToolKind: ev.ToolKind,
		Title:    ev.Title,
		Done:     true,
		Err:      ev.Err,
	}
	rows := renderACPStandardToolCollapsedRows(blockID, header, ev.CallID, width, ctx, ev.Err, true, "")
	prefix := "✓ "
	style := ctx.Theme.HelpHintTextStyle()
	if ev.Err {
		prefix = "✗ "
		style = ctx.Theme.ToolErrorStyle()
	}
	rows = append(rows, renderACPToolDetailRows(blockID, prefix, output, width, ctx, style)...)
	return rows
}

func acpToolPanelText(preview string, final SubagentEvent, hasFinal bool) (string, bool) {
	panelText := sanitizeRenderableText(preview)
	panelErr := false
	if hasFinal {
		panelText = sanitizeRenderableText(final.Output)
		panelErr = final.Err
		if !renderableTextHasContent(panelText) && !panelErr {
			panelText = "completed"
		}
	}
	return panelText, panelErr
}

func toolLifecycleHeaderEvent(start SubagentEvent, final SubagentEvent, hasFinal bool, settled bool) SubagentEvent {
	header := start
	if hasFinal {
		if name := strings.TrimSpace(final.Name); name != "" {
			header.Name = name
		}
		if toolKind := strings.TrimSpace(final.ToolKind); toolKind != "" {
			header.ToolKind = toolKind
		}
		if title := strings.TrimSpace(final.Title); title != "" {
			header.Title = title
		}
		if taskHandle := strings.TrimSpace(final.TaskHandle); taskHandle != "" {
			header.TaskHandle = taskHandle
		}
		if action := strings.TrimSpace(final.TaskAction); action != "" {
			header.TaskAction = action
		}
		if input := strings.TrimSpace(final.TaskInput); input != "" {
			header.TaskInput = input
		}
		if targetKind := strings.TrimSpace(final.TaskTargetKind); targetKind != "" {
			header.TaskTargetKind = targetKind
		}
		header.OutputGapBefore = header.OutputGapBefore || final.OutputGapBefore
		if args := strings.TrimSpace(final.Args); args != "" {
			if isTerminalPanelToolEvent(header) {
				header.Args = normalizeACPToolInline(args)
			} else {
				header.Args = compactACPToolInline(args, acpToolInlineArgsMaxWidth+12)
			}
		}
		header.Err = final.Err
	}
	header.Done = settled
	return header
}

// toolEventHasPresentation distinguishes an ACP tool identity from a sparse
// content/state patch. Arguments and output alone cannot name a tool, and a
// generic kind=other without a provider title has no trustworthy label. Keep
// such updates in reducer state so a later identity-bearing patch can
// materialize them, but never manufacture the user-visible label "Tool".
func toolEventHasPresentation(ev SubagentEvent) bool {
	if strings.TrimSpace(ev.Name) != "" || strings.TrimSpace(ev.Title) != "" ||
		strings.TrimSpace(ev.ExplorationVerb) != "" {
		return true
	}
	kind := strings.ToLower(strings.TrimSpace(ev.ToolKind))
	return kind != "" && kind != "other"
}

func shouldRenderACPToolPanel(text string, err bool) bool {
	if !renderableTextHasContent(text) {
		return err
	}
	if !err && isACPCompactToolAck(text) {
		return false
	}
	return true
}

func isACPCompactToolAck(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "completed", "message sent.", "message delivered.":
		return true
	default:
		return false
	}
}

func finalPanelToolName(start SubagentEvent, final SubagentEvent, hasFinal bool) string {
	if hasFinal && strings.TrimSpace(final.Name) != "" {
		return final.Name
	}
	return start.Name
}

func renderACPStandardToolLifecycleRows(blockID string, ev SubagentEvent, callID string, text string, width int, ctx BlockRenderContext, err bool, final bool, fullOutput bool) []RenderedRow {
	header := standardToolLifecycleHeader(ev, err)
	token := acpToolPanelClickTokenIf(callID, toolPanelCanExpandHiddenDetails(ev, text, final, err))
	rows := []RenderedRow{renderACPToolHeaderRow(blockID, header, width, ctx, token, err, final)}
	if !final || !fullOutput {
		text = summarizeACPToolPanelText(text, final)
	}
	if !renderableTextHasContent(text) {
		if !final || err {
			return rows
		}
		text = "completed"
	}
	style := ctx.Theme.HelpHintTextStyle()
	if err {
		style = ctx.Theme.ToolErrorStyle()
	}
	rows = append(rows, renderACPToolOutputRowsWithToken(blockID, "  └ ", text, width, ctx, style, token)...)
	return rows
}

func renderACPStandardToolCollapsedRows(blockID string, ev SubagentEvent, callID string, width int, ctx BlockRenderContext, err bool, completed bool, token string) []RenderedRow {
	header := standardToolLifecycleHeader(ev, err)
	return []RenderedRow{renderACPToolHeaderRow(blockID, header, width, ctx, token, err, completed)}
}

func acpStandardCollapsedClickToken(callID string, ev SubagentEvent, text string, err bool) string {
	if toolPanelEventHasHiddenToolArgs(ev) || shouldRenderACPToolPanel(text, err) {
		return acpToolPanelClickToken(callID)
	}
	return ""
}

func standardToolLifecycleHeader(ev SubagentEvent, err bool) string {
	semanticName := ev.Name
	presentation := transcript.ResolveToolPresentationWithHint(ev.Name, ev.ToolKind, ev.Title, ev.ExplorationVerb)
	switch semanticName {
	case surfaceToolRunCommand, surfaceToolSpawn, surfaceToolSendMessage:
		ev.Name = semanticName
		return terminalLifecycleHeader(ev)
	case surfaceToolTask:
		if taskEventAction(ev) == "write" {
			return taskWriteLifecycleHeader(ev, err)
		}
		return taskControlLifecycleHeader(ev)
	case surfaceToolWrite, surfaceToolPatch:
		ev.Name = semanticName
		return mutationLifecycleHeader(ev, err)
	case surfaceToolRemember:
		if title := strings.TrimSpace(ev.Title); title != "" {
			return "• " + title
		}
		return memoryLifecycleHeader(ev, err)
	default:
		if presentation.TitleAsLabel {
			// Provider titles are complete presentation labels. Only a generic
			// shell label is known to omit the command from that label.
			args := ""
			if genericExecuteTitle(presentation.DisplayName) {
				args = strings.TrimSpace(ev.Args)
				if strings.EqualFold(args, presentation.DisplayName) {
					args = ""
				}
			}
			return standardVerbLifecycleHeader(presentation.DisplayName, args, err)
		}
		if isExecuteToolKind(ev.ToolKind) {
			return terminalLifecycleHeader(ev)
		}
		if verb := surfaceExplorationVerb(semanticName, ev.ToolKind, ev.ExplorationVerb); verb != "" {
			return standardVerbLifecycleHeader(verb, ev.Args, err)
		}
		return standardVerbLifecycleHeader(presentation.DisplayName, ev.Args, err)
	}
}

func memoryLifecycleHeader(ev SubagentEvent, err bool) string {
	if err {
		return "• Update memory failed"
	}
	if ev.Done {
		return "• Updated memory"
	}
	return "• Updating memory"
}

func taskControlLifecycleHeader(ev SubagentEvent) string {
	verb, detail := splitTaskAction(ev.Args)
	if verb == "" {
		verb = "Task"
	}
	switch strings.ToLower(strings.TrimSpace(verb)) {
	case "wait", "read", "cancel":
		return standardVerbLifecycleHeader(verb, detail, false)
	default:
		return standardVerbLifecycleHeader("Task", ev.Args, false)
	}
}

func taskWriteLifecycleHeader(ev SubagentEvent, err bool) string {
	input := normalizeTaskWriteDisplayInput(ev.TaskInput)
	if input == "" {
		_, detail := splitTaskAction(ev.Args)
		handle := taskHandleDisplay(ev.TaskHandle)
		if handle != "" {
			detail = strings.TrimSpace(strings.TrimPrefix(detail, handle))
			detail = strings.TrimSpace(strings.TrimPrefix(detail, ":"))
		}
		input = normalizeTaskWriteDisplayInput(detail)
	}
	header := "• Interacted with background shell"
	if input != "" {
		header += ": " + input
	}
	if err {
		header += " failed"
	}
	return header
}

func standardVerbLifecycleHeader(verb string, args string, err bool) string {
	verb = strings.TrimSpace(verb)
	if verb == "" {
		verb = "Tool"
	}
	args = strings.TrimSpace(args)
	if err {
		if args != "" {
			return "• " + verb + " " + args + " failed"
		}
		return "• " + verb + " failed"
	}
	if args != "" {
		return "• " + verb + " " + args
	}
	return "• " + verb
}

func renderACPToolPanelRows(blockID string, callID string, toolName string, terminalPanel bool, text string, width int, ctx BlockRenderContext, err bool, token string, opts acpTranscriptRenderOptions) []RenderedRow {
	request := toolPanelRenderRequest{
		BlockID:       blockID,
		CallID:        callID,
		ToolName:      toolName,
		TerminalPanel: terminalPanel,
		Text:          text,
		Width:         width,
		Ctx:           ctx,
		Err:           err,
		ClickToken:    token,
	}
	if opts.ToolPanelRows != nil {
		return opts.ToolPanelRows(request)
	}
	return request.renderUncached()
}

func (r toolPanelRenderRequest) renderUncached() []RenderedRow {
	blockID := r.BlockID
	callID := r.CallID
	toolName := r.ToolName
	text := r.Text
	width := r.Width
	ctx := r.Ctx
	err := r.Err
	token := r.ClickToken
	text = sanitizeRenderableText(text)
	if isDiffPanelText(text) && !err {
		return applyClickTokenToRows(renderACPDiffPanelRows(blockID, text, width, ctx), token)
	}
	if r.TerminalPanel || surfaceIsTerminalPanelTool(toolName) {
		return renderACPTerminalPanelRows(blockID, callID, text, width, ctx, err, token)
	}
	style := ctx.Theme.HelpHintTextStyle()
	if err {
		style = ctx.Theme.ToolErrorStyle()
	}
	return renderACPToolOutputRowsWithToken(blockID, "  └ ", text, width, ctx, style, token)
}

func isTerminalPanelToolEvent(ev SubagentEvent) bool {
	// Task is a control surface. It may carry a relation to a live command TTY
	// so wait/read can repair the owning RunCommand panel, but it never owns a
	// terminal panel of its own. Task write only records the stdin interaction.
	if isTaskControlEvent(ev) {
		return false
	}
	return ev.Terminal || isExecuteToolKind(ev.ToolKind) || surfaceIsTerminalPanelTool(ev.Name)
}

func isMutationPanelTool(name string) bool {
	return isMutationPanelToolKind(name, "")
}

func isMutationPanelToolKind(name string, kind string) bool {
	switch name {
	case surfaceToolWrite, surfaceToolPatch:
		return true
	}
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "edit", "delete", "move":
		return true
	default:
		return false
	}
}

func isMutationPanelToolEvent(ev SubagentEvent) bool {
	return isMutationPanelToolKind(ev.Name, ev.ToolKind)
}

func renderACPTerminalLifecycleRows(blockID string, ev SubagentEvent, callID string, text string, width int, ctx BlockRenderContext, err bool, expanded bool, final bool, fullOutput bool, opts acpTranscriptRenderOptions) []RenderedRow {
	headerEvent := ev
	sendMessage := ev.Name == surfaceToolSendMessage
	if fullOutput {
		if fullArgs := strings.TrimSpace(ev.FullArgs); fullArgs != "" {
			headerEvent.Args = fullArgs
		}
	}
	header := terminalLifecycleHeader(headerEvent)
	if sendMessage && err {
		header = failedSendMessageHeader(headerEvent)
	}
	token := acpToolPanelClickTokenIf(callID, toolPanelCanExpandHiddenDetails(ev, text, final, err))
	tone, dim := acpToolHeaderMark(ctx, err, final)
	var headerRow RenderedRow
	if sendMessage && opts.AgentMessageTargetLinks && agentMessageTargetCanOpenOverlay(ev.MessageTarget) {
		token = agentMessageTargetOverlayClickToken(callID)
		headerRow = renderACPTranscriptLinkedHeaderRowMarked(blockID, header, ctx, token, tone, dim)
	} else {
		headerRow = renderACPTranscriptHeaderRowMarked(blockID, header, width, ctx, token, tone, dim)
	}
	rows := []RenderedRow{headerRow}
	if !expanded || !shouldRenderACPToolPanel(text, err) {
		return rows
	}
	if ev.OutputGapBefore {
		rows = append(rows, renderACPTerminalGapRow(blockID, width, ctx, token))
	}
	if final && fullOutput {
		rows = append(rows, renderACPFullTerminalPanelRows(blockID, callID, text, width, ctx, err, token)...)
		return rows
	}
	text = summarizeACPToolPanelText(text, final)
	rows = append(rows, renderACPToolPanelRows(blockID, callID, ev.Name, isTerminalPanelToolEvent(ev), text, width, ctx, err, token, opts)...)
	return rows
}

func failedSendMessageHeader(ev SubagentEvent) string {
	if args := strings.TrimSpace(ev.Args); args != "" {
		return "• Failed to send " + args
	}
	return "• Failed to send"
}

func isSpawnToolEvent(ev SubagentEvent) bool {
	return ev.Name == surfaceToolSpawn
}

func acpTranscriptEventsHaveRunningTool(events []SubagentEvent) bool {
	for _, event := range events {
		if event.Kind == SEToolCall && !event.Done && !isSpawnToolEvent(event) {
			return true
		}
	}
	return false
}

// renderACPSpawnToolRows renders product-owned Main Spawn as a retained child
// workspace link. Participant-owned Spawn remains in the standard tool-panel
// path because a Side ACP child is isolated behind its parent's ACP tool result.
func renderACPSpawnToolRows(blockID string, ev SubagentEvent, callID string, width int, ctx BlockRenderContext) []RenderedRow {
	header := terminalLifecycleHeader(ev)
	token := subagentOutputOverlayClickToken(callID)
	if token == "" {
		return []RenderedRow{renderACPTranscriptHeaderRow(blockID, header, width, ctx, "")}
	}
	return []RenderedRow{renderACPTranscriptLinkedHeaderRow(blockID, header, ctx, token)}
}

func agentMessageTargetCanOpenOverlay(target string) bool {
	target = normalizeTaskStreamHandle(target)
	return target != "" && !strings.EqualFold(target, "parent")
}

func renderACPTranscriptLinkedHeaderRow(blockID, header string, ctx BlockRenderContext, token string) RenderedRow {
	return renderACPTranscriptLinkedHeaderRowMarked(blockID, header, ctx, token, acpHeaderMarkDefault, false)
}

func renderACPTranscriptLinkedHeaderRowMarked(
	blockID string,
	header string,
	ctx BlockRenderContext,
	token string,
	tone acpHeaderMarkTone,
	dim bool,
) RenderedRow {
	header = sanitizeRenderableText(header)
	row := StyledPlainClickableRow(blockID, header, styleACPTranscriptHeaderWithMark(ctx, header, tone, dim), token)
	row.ACPHeader = true
	row.acpHeaderMarkTone = tone
	row.acpHeaderMarkDim = dim
	row.selectionIndent = 2
	return row
}

const terminalOutputGapNotice = "… earlier output unavailable …"

func renderACPTerminalGapRow(blockID string, width int, ctx BlockRenderContext, token string) RenderedRow {
	rows := renderACPToolOutputRowsWithToken(blockID, "  └ ", terminalOutputGapNotice, width, ctx, ctx.Theme.TranscriptMetaStyle(), token)
	if len(rows) == 0 {
		return PlainRow(blockID, terminalOutputGapNotice)
	}
	return rows[0]
}

func terminalLifecycleHeader(ev SubagentEvent) string {
	rawName := firstTrimmed(ev.Name, "TOOL")
	name := rawName
	args := strings.TrimSpace(ev.Args)
	switch name {
	case surfaceToolRunCommand:
		if args != "" {
			return "• Ran " + args
		}
		return "• Ran"
	case surfaceToolSpawn:
		args = surfaceSanitizeSpawnHeaderArgs(args)
		if args != "" {
			return "• Spawned " + args
		}
		return "• Spawned"
	case surfaceToolSendMessage:
		if args != "" {
			return "• Sent " + args
		}
		return "• Sent"
	default:
		if isExecuteToolKind(ev.ToolKind) {
			if command := executeToolCommandDisplay(rawName, args); command != "" {
				return "• Ran " + command
			}
			return "• Ran"
		}
		if args != "" {
			return "• " + rawName + " " + args
		}
		return "• " + rawName
	}
}

func sanitizeSpawnHeaderArgs(args string) string {
	return surfaceSanitizeSpawnHeaderArgs(args)
}

func isExecuteToolKind(kind string) bool {
	return strings.EqualFold(strings.TrimSpace(kind), "execute")
}

func executeToolCommandDisplay(rawName string, args string) string {
	rawName = strings.TrimSpace(rawName)
	args = strings.TrimSpace(args)
	if args == "" {
		return rawName
	}
	if shouldPrefixExecuteToolName(rawName, args) {
		return strings.TrimSpace(rawName + " " + args)
	}
	return args
}

func shouldPrefixExecuteToolName(rawName string, args string) bool {
	rawName = strings.TrimSpace(rawName)
	if rawName == "" {
		return false
	}
	if strings.ContainsAny(rawName, " \t\n\r") {
		return false
	}
	switch strings.ToLower(rawName) {
	case "bash", "sh", "zsh", "fish", "execute", "tool", "shell", "terminal", "command", "run", "running":
		return false
	}
	first := firstShellExecutableToken(args)
	return first == "" || !strings.EqualFold(first, rawName)
}

func firstShellExecutableToken(command string) string {
	for _, token := range shellCommandTokens(command) {
		if token.Class == shellTokenCommand {
			return strings.Trim(token.Text, `"'`)
		}
	}
	return ""
}

func renderACPMutationLifecycleRows(blockID string, ev SubagentEvent, callID string, text string, width int, ctx BlockRenderContext, err bool, expanded bool, completed bool, opts acpTranscriptRenderOptions) []RenderedRow {
	header := mutationLifecycleHeader(ev, err)
	token := ""
	if !err && shouldRenderACPToolPanel(text, err) && !mutationPanelTextIsHeaderOnly(ev, text) {
		token = acpToolPanelClickToken(callID)
	}
	rows := []RenderedRow{renderACPToolHeaderRow(blockID, header, width, ctx, token, err, completed)}
	if err {
		if msg := sanitizeRenderableText(text); renderableTextHasContent(msg) && msg != sanitizeRenderableText(ev.Args) {
			rows = append(rows, renderACPToolDetailRows(blockID, "  └ ", msg, width, ctx, ctx.Theme.ToolErrorStyle())...)
		}
		return rows
	}
	if !expanded || !shouldRenderACPToolPanel(text, err) {
		return rows
	}
	if mutationPanelTextIsHeaderOnly(ev, text) {
		return rows
	}
	rows = append(rows, renderACPToolPanelRows(blockID, callID, ev.Name, false, text, width, ctx, err, token, opts)...)
	return rows
}

func mutationPanelTextIsHeaderOnly(ev SubagentEvent, text string) bool {
	text = strings.TrimSpace(text)
	if text == "" || strings.Contains(text, "\n") {
		return false
	}
	return strings.EqualFold(text, strings.TrimSpace(ev.Args))
}

func mutationLifecycleHeader(ev SubagentEvent, err bool) string {
	name := ev.Name
	args := strings.TrimSpace(ev.Args)
	switch name {
	case surfaceToolWrite, surfaceToolPatch:
		return standardVerbLifecycleHeader("Edit", args, err)
	case surfaceToolRemember:
		return memoryLifecycleHeader(ev, err)
	default:
		if args == "" {
			args = strings.ToLower(name)
		}
		return standardVerbLifecycleHeader(name, args, err)
	}
}
