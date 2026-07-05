package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
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
		return renderACPStandardToolCollapsedRows(blockID, ev, "", width, ctx, ev.Err, ""), idx
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
	singleCompletedLifecycle := len(group) == 1 && start.Done && strings.TrimSpace(start.Args) != ""
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
	hasStart := (!start.Done || singleCompletedLifecycle) && strings.TrimSpace(start.Name) != ""
	hasFinal := false
	for _, item := range group {
		if !item.Done {
			if renderableTextHasContent(item.Output) {
				preview = item.Output
			}
			continue
		}
		if !shouldRenderToolEvent(item) {
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
		if shouldRenderToolEvent(ev) {
			return renderACPStandardToolCollapsedRows(blockID, ev, callID, width, ctx, ev.Err, ""), end
		}
		return nil, end
	}

	if isTerminalPanelToolEvent(start) {
		start.Args = normalizeACPToolInline(start.Args)
	} else {
		start.Args = compactACPToolInline(start.Args, width)
	}
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
		if isSubagentTaskWriteEvent(events, idx) {
			return renderACPStandardToolLifecycleRows(blockID, toolLifecycleHeaderEvent(start, final, hasFinal), callID, panelText, width, ctx, panelErr, hasFinal, fullOutput), end
		}
		if isTerminalPanelToolEvent(start) {
			return renderACPTerminalLifecycleRows(blockID, toolLifecycleHeaderEvent(start, final, hasFinal), callID, panelText, width, ctx, panelErr, panelExpanded, hasFinal, fullOutput, opts), end
		}
		if isMutationPanelToolEvent(start) {
			return renderACPMutationLifecycleRows(blockID, toolLifecycleHeaderEvent(start, final, hasFinal), callID, panelText, width, ctx, panelErr, panelExpanded, opts), end
		}
		if hasFinal && shouldDefaultCollapseToolEvent(final) && !panelExpanded {
			token := acpStandardCollapsedClickToken(callID, final, panelText, panelErr)
			return renderACPStandardToolCollapsedRows(blockID, toolLifecycleHeaderEvent(start, final, hasFinal), callID, width, ctx, final.Err, token), end
		}
		if !hasFinal && shouldDefaultCollapseToolEvent(start) && !shouldRenderACPToolPanel(panelText, panelErr) {
			return renderACPStandardToolCollapsedRows(blockID, toolLifecycleHeaderEvent(start, final, hasFinal), callID, width, ctx, panelErr, ""), end
		}
		if !shouldRenderACPToolPanel(panelText, panelErr) {
			return renderACPStandardToolCollapsedRows(blockID, toolLifecycleHeaderEvent(start, final, hasFinal), callID, width, ctx, panelErr, ""), end
		}
		if !panelExpanded {
			token := acpStandardCollapsedClickToken(callID, toolLifecycleHeaderEvent(start, final, hasFinal), panelText, panelErr)
			return renderACPStandardToolCollapsedRows(blockID, toolLifecycleHeaderEvent(start, final, hasFinal), callID, width, ctx, panelErr, token), end
		}
		return renderACPStandardToolLifecycleRows(blockID, toolLifecycleHeaderEvent(start, final, hasFinal), callID, panelText, width, ctx, panelErr, hasFinal, fullOutput), end
	}
	rows := renderACPStandardToolCollapsedRows(blockID, start, callID, width, ctx, false, "")
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
	output := sanitizeRenderableText(ev.Output)
	if opts.ToolOutputPanels && isTaskWritePanelEvent(ev) {
		fullOutput := false
		if opts.ToolPanelFullOutput != nil {
			fullOutput = opts.ToolPanelFullOutput(ev.CallID)
		}
		return renderACPStandardToolLifecycleRows(blockID, ev, ev.CallID, output, width, ctx, ev.Err, true, fullOutput)
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
			return renderACPMutationLifecycleRows(blockID, ev, ev.CallID, output, width, ctx, ev.Err, panelExpanded, opts)
		}
		if !panelExpanded {
			token := acpStandardCollapsedClickToken(ev.CallID, ev, output, ev.Err)
			return renderACPStandardToolCollapsedRows(blockID, ev, ev.CallID, width, ctx, ev.Err, token)
		}
		return renderACPStandardToolLifecycleRows(blockID, ev, ev.CallID, output, width, ctx, ev.Err, true, fullOutput)
	}
	if !renderableTextHasContent(output) || (!strings.Contains(output, "\n") && displayColumns(output) <= maxInt(24, width/2)) {
		return renderACPStandardToolCollapsedRows(blockID, ev, ev.CallID, width, ctx, ev.Err, "")
	}
	header := SubagentEvent{
		Kind: SEToolCall,
		Name: ev.Name,
		Done: true,
		Err:  ev.Err,
	}
	rows := renderACPStandardToolCollapsedRows(blockID, header, ev.CallID, width, ctx, ev.Err, "")
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

func toolLifecycleHeaderEvent(start SubagentEvent, final SubagentEvent, hasFinal bool) SubagentEvent {
	header := start
	if hasFinal {
		if name := strings.TrimSpace(final.Name); name != "" {
			header.Name = name
		}
		if toolKind := strings.TrimSpace(final.ToolKind); toolKind != "" {
			header.ToolKind = toolKind
		}
		if taskID := strings.TrimSpace(final.TaskID); taskID != "" {
			header.TaskID = taskID
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
		if args := strings.TrimSpace(final.Args); args != "" {
			if isTerminalPanelToolEvent(header) {
				header.Args = normalizeACPToolInline(args)
			} else {
				header.Args = compactACPToolInline(args, acpToolInlineArgsMaxWidth+12)
			}
		}
	}
	return header
}

func shouldRenderACPToolPanel(text string, err bool) bool {
	if !renderableTextHasContent(text) {
		return err
	}
	if !err && strings.EqualFold(strings.TrimSpace(text), "completed") {
		return false
	}
	return true
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
	rows := []RenderedRow{renderACPTranscriptHeaderRow(blockID, header, width, ctx, token)}
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

func renderACPStandardToolCollapsedRows(blockID string, ev SubagentEvent, callID string, width int, ctx BlockRenderContext, err bool, token string) []RenderedRow {
	header := standardToolLifecycleHeader(ev, err)
	return []RenderedRow{renderACPTranscriptHeaderRow(blockID, header, width, ctx, token)}
}

func acpStandardCollapsedClickToken(callID string, ev SubagentEvent, text string, err bool) string {
	if toolPanelEventHasHiddenToolArgs(ev) || shouldRenderACPToolPanel(text, err) {
		return acpToolPanelClickToken(callID)
	}
	return ""
}

func standardToolLifecycleHeader(ev SubagentEvent, err bool) string {
	semanticName := toolSemanticName(ev.Name, ev.ToolKind)
	switch strings.ToUpper(strings.TrimSpace(semanticName)) {
	case "RUN_COMMAND", "SPAWN":
		ev.Name = semanticName
		return terminalLifecycleHeader(ev)
	case "TASK":
		if taskEventAction(ev) == "write" {
			return taskWriteLifecycleHeader(ev, err)
		}
		return taskControlLifecycleHeader(ev)
	case "WRITE", "PATCH":
		ev.Name = semanticName
		return mutationLifecycleHeader(ev, err)
	default:
		if verb := display.ExplorationVerbForTool(semanticName); verb != "" {
			return standardVerbLifecycleHeader(verb, ev.Args, err)
		}
		return standardVerbLifecycleHeader(toolEventDisplayName(firstTrimmed(ev.Name, ev.ToolKind, "Tool")), ev.Args, err)
	}
}

func taskControlLifecycleHeader(ev SubagentEvent) string {
	verb, detail := splitTaskAction(ev.Args)
	if verb == "" {
		verb = "Task"
	}
	switch strings.ToLower(strings.TrimSpace(verb)) {
	case "wait", "cancel":
		return standardVerbLifecycleHeader(verb, detail, false)
	default:
		return standardVerbLifecycleHeader("Task", ev.Args, false)
	}
}

func taskWriteLifecycleHeader(ev SubagentEvent, err bool) string {
	if _, detail := splitTaskAction(ev.Args); detail != "" {
		return standardVerbLifecycleHeader("Write", detail, err)
	}
	handle := taskHandleDisplay(ev.TaskID)
	input := normalizeTaskWriteDisplayInput(ev.TaskInput)
	if input == "" {
		_, detail := splitTaskAction(ev.Args)
		if before, after, ok := strings.Cut(detail, ":"); ok && taskHandleDisplay(before) != "" {
			handle = firstNonEmpty(handle, taskHandleDisplay(before))
			input = normalizeTaskWriteDisplayInput(after)
		} else {
			input = normalizeTaskWriteDisplayInput(detail)
		}
	}
	args := ""
	switch {
	case handle != "" && input != "":
		args = handle + ": " + input
	case handle != "":
		args = handle
	case input != "":
		args = input
	}
	return standardVerbLifecycleHeader("Write", args, err)
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

func renderACPToolPanelRows(blockID string, callID string, toolName string, text string, width int, ctx BlockRenderContext, err bool, token string, opts acpTranscriptRenderOptions) []RenderedRow {
	request := toolPanelRenderRequest{
		BlockID:    blockID,
		CallID:     callID,
		ToolName:   toolName,
		Text:       text,
		Width:      width,
		Ctx:        ctx,
		Err:        err,
		ClickToken: token,
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
	if display.IsTerminalPanelTool(toolName, "") {
		return renderACPTerminalPanelRows(blockID, callID, text, width, ctx, err, token)
	}
	style := ctx.Theme.HelpHintTextStyle()
	if err {
		style = ctx.Theme.ToolErrorStyle()
	}
	return renderACPToolOutputRowsWithToken(blockID, "  └ ", text, width, ctx, style, token)
}

func isTerminalPanelToolEvent(ev SubagentEvent) bool {
	return ev.Terminal || display.IsTerminalPanelTool(ev.Name, ev.ToolKind)
}

func isMutationPanelTool(name string) bool {
	return isMutationPanelToolKind(name, "")
}

func isMutationPanelToolKind(name string, kind string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "WRITE", "PATCH":
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

func toolSemanticName(name string, kind string) string {
	return display.SemanticToolName(name, kind)
}

func isAttentionLoopTool(name string) bool {
	name = strings.ToUpper(strings.TrimSpace(name))
	if name == "" || name == "TASK" {
		return false
	}
	return !shouldDefaultCollapseToolPanel(name)
}

func renderACPTerminalLifecycleRows(blockID string, ev SubagentEvent, callID string, text string, width int, ctx BlockRenderContext, err bool, expanded bool, final bool, fullOutput bool, opts acpTranscriptRenderOptions) []RenderedRow {
	headerEvent := ev
	if fullOutput && strings.EqualFold(strings.TrimSpace(ev.Name), "SPAWN") {
		if fullArgs := strings.TrimSpace(ev.FullArgs); fullArgs != "" {
			headerEvent.Args = fullArgs
		}
	}
	displayText := text
	if strings.EqualFold(strings.TrimSpace(toolSemanticName(ev.Name, ev.ToolKind)), "SPAWN") {
		displayText = summarizeSubagentTerminalPanelText(displayText, final)
	}
	header := terminalLifecycleHeader(headerEvent)
	token := acpToolPanelClickTokenIf(callID, toolPanelCanExpandHiddenDetails(ev, displayText, final, err))
	rows := []RenderedRow{renderACPTranscriptHeaderRow(blockID, header, width, ctx, token)}
	if !renderableTextHasContent(text) && !final && strings.EqualFold(strings.TrimSpace(ev.Name), "SPAWN") {
		text = "(wait subagent output)"
	}
	if !expanded || !shouldRenderACPToolPanel(text, err) {
		return rows
	}
	if final && fullOutput {
		rows = append(rows, renderACPFullTerminalPanelRows(blockID, callID, text, width, ctx, err, token)...)
		return rows
	}
	if strings.EqualFold(strings.TrimSpace(toolSemanticName(ev.Name, ev.ToolKind)), "SPAWN") {
		text = displayText
		if !renderableTextHasContent(text) && !final {
			text = "(wait subagent output)"
		}
	}
	text = summarizeACPToolPanelText(text, final)
	rows = append(rows, renderACPToolPanelRows(blockID, callID, toolSemanticName(ev.Name, ev.ToolKind), text, width, ctx, err, token, opts)...)
	return rows
}

func terminalLifecycleHeader(ev SubagentEvent) string {
	rawName := firstTrimmed(ev.Name, "TOOL")
	name := strings.ToUpper(strings.TrimSpace(rawName))
	args := strings.TrimSpace(ev.Args)
	switch name {
	case "RUN_COMMAND":
		if args != "" {
			return "• Ran " + args
		}
		return "• Ran"
	case "SPAWN":
		args = display.SanitizeSpawnHeaderArgs(args)
		if args != "" {
			return "• Spawned " + args
		}
		return "• Spawned"
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
	return display.SanitizeSpawnHeaderArgs(args)
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

func renderACPMutationLifecycleRows(blockID string, ev SubagentEvent, callID string, text string, width int, ctx BlockRenderContext, err bool, expanded bool, opts acpTranscriptRenderOptions) []RenderedRow {
	header := mutationLifecycleHeader(ev, err)
	token := ""
	if !err && shouldRenderACPToolPanel(text, err) && !mutationPanelTextIsHeaderOnly(ev, text) {
		token = acpToolPanelClickToken(callID)
	}
	rows := []RenderedRow{renderACPTranscriptHeaderRow(blockID, header, width, ctx, token)}
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
	rows = append(rows, renderACPToolPanelRows(blockID, callID, ev.Name, text, width, ctx, err, token, opts)...)
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
	name := strings.ToUpper(strings.TrimSpace(toolSemanticName(ev.Name, ev.ToolKind)))
	args := strings.TrimSpace(ev.Args)
	if args == "" && name != "WRITE" && name != "PATCH" {
		args = strings.ToLower(name)
	}
	switch name {
	case "WRITE":
		if err {
			return "• Write failed " + args
		}
		return "• Wrote " + args
	case "PATCH":
		if err {
			return "• Patch failed " + args
		}
		return "• Patched " + args
	default:
		return "• " + name + " " + args
	}
}
