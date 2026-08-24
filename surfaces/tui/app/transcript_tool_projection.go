package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/surfaces/internal/transcript"
	"github.com/caelis-labs/caelis/surfaces/tui/acpprojector"
)

func projectTranscriptToolCall(input transcript.ToolProjectionInput) TranscriptEvent {
	toolName := strings.TrimSpace(input.ToolName)
	explorationVerb := toolDisplayExplorationVerb(input.Meta)
	presentation := transcript.ResolveToolPresentationWithHint(toolName, input.ToolKind, input.ToolTitle, explorationVerb)
	status := transcript.NormalizeToolStartStatus(input.Status)
	semanticName := toolName
	rawInput := transcript.CloneAnyMap(input.RawInput)
	toolTaskHandle := display.ToolTaskHandle(rawInput, nil, input.Meta)
	toolMessageTarget := ""
	if semanticName == surfaceToolSendMessage {
		toolMessageTarget = display.AgentMessageTarget(display.MapString(rawInput, "to"))
	}
	content := acpToolContentToDisplay(input.Content)
	toolTerminal := transcriptToolHasTerminal(input.Meta, content)
	outputCursor, outputCursorKnown, outputStartCursor, outputStartCursorKnown := transcriptToolOutputRange(input.Meta)
	// Start input is standard ACP RawInput only. DisplayToolInput is a
	// result-time recovery channel and is not authenticated on every external
	// ACP ingress path.
	displayInput := rawInput
	if semanticName == surfaceToolTask {
		displayInput = taskDisplayInputForResult(rawInput, toolDisplayMetaOutput(semanticName, input.Meta))
	}
	toolArgs, toolFullArgs := toolDisplayArguments(semanticName, input.ToolKind, displayInput, toolTitleDisplayArgs(semanticName, input.ToolKind, input.ToolTitle), acpprojector.FormatToolStart(presentation.DisplayName, displayInput))
	if semanticName == surfaceToolTask {
		toolArgs = taskDisplayArgsWithHandle(toolArgs, toolTaskHandle)
	}
	return TranscriptEvent{
		Kind:                       TranscriptEventTool,
		Scope:                      input.Scope,
		ScopeID:                    input.ScopeID,
		Actor:                      input.Actor,
		OccurredAt:                 input.OccurredAt,
		Meta:                       transcript.CloneAnyMap(input.Meta),
		ToolCallID:                 strings.TrimSpace(input.CallID),
		ToolName:                   toolName,
		ToolKind:                   strings.TrimSpace(input.ToolKind),
		ToolTitle:                  strings.TrimSpace(input.ToolTitle),
		ToolExplorationVerb:        explorationVerb,
		ToolArgs:                   toolArgs,
		ToolFullArgs:               toolFullArgs,
		ToolStatus:                 status,
		ToolStatusExplicit:         input.StatusExplicit,
		ToolTerminal:               toolTerminal,
		ToolOutputCursor:           outputCursor,
		ToolOutputCursorKnown:      outputCursorKnown,
		ToolOutputStartCursor:      outputStartCursor,
		ToolOutputStartCursorKnown: outputStartCursorKnown,
		ToolTaskHandle:             toolTaskHandle,
		ToolTaskAction:             display.ToolTaskAction(rawInput, nil, input.Meta),
		ToolTaskInput:              display.ToolTaskInput(rawInput, nil, input.Meta),
		ToolTaskTargetKind:         display.ToolTaskTargetKind(rawInput, nil, input.Meta),
		ToolMessageTarget:          toolMessageTarget,
	}
}

func projectTranscriptToolResult(input transcript.ToolProjectionInput, defaultSuccessStatus string) (TranscriptEvent, bool) {
	toolName := strings.TrimSpace(input.ToolName)
	explorationVerb := toolDisplayExplorationVerb(input.Meta)
	presentation := transcript.ResolveToolPresentationWithHint(toolName, input.ToolKind, input.ToolTitle, explorationVerb)
	semanticName := toolName
	rawInput := transcript.CloneAnyMap(input.RawInput)
	rawOutput := transcript.RawMap(input.RawOutput)
	status, toolErr := transcript.NormalizeToolResultStatus(input.Status, rawOutput, input.Error, defaultSuccessStatus)
	content := acpToolContentToDisplay(input.Content)
	toolTerminal := transcriptToolHasTerminal(input.Meta, content)
	suppressRunningSnapshotOutput := suppressRunningTerminalSnapshotOutput(semanticName, input.Meta, status, toolErr)
	summaryOutput := toolDisplaySummaryOutput(semanticName, rawOutput, input.Meta)
	displayOutput := toolDisplayMetaOutput(semanticName, input.Meta)
	taskOutput := transcript.CloneAnyMap(displayOutput)
	if taskOutput == nil {
		taskOutput = map[string]any{}
	}
	for key, value := range rawOutput {
		taskOutput[key] = value
	}
	displayMeta := metautil.Section(input.Meta, metautil.Display)
	recoveredInput := transcript.RawMap(displayMeta[metautil.DisplayToolInput])
	// DisplayToolInput is written by a strict controller-side completed-result
	// recovery path, but not authenticated on every external ingress. Never let
	// an in-progress or sparse live patch rewrite invocation arguments with it.
	if !transcript.ToolStatusFinal(status, toolErr) {
		recoveredInput = nil
	}
	displayInput := toolDisplayInputWithRecovered(rawInput, recoveredInput)
	if semanticName == surfaceToolSpawn {
		displayInput = spawnDisplayInputForResult(rawInput, displayOutput)
	}
	if semanticName == surfaceToolTask {
		displayInput = taskDisplayInputForResult(rawInput, displayOutput)
	}
	toolOutput := acpprojector.FormatToolContent(content)
	toolOutputHasTerminalData := false
	toolOutputSynthetic := false
	fallbackInput := transcript.ToolOutputFallbackInput{
		ToolName:  semanticName,
		ToolKind:  input.ToolKind,
		RawOutput: rawOutput,
		Meta:      input.Meta,
		Status:    status,
		Error:     toolErr,
	}
	if !suppressRunningSnapshotOutput {
		terminalOutput := transcript.TerminalToolOutputText(fallbackInput)
		if terminalOutput != "" {
			toolOutputHasTerminalData = toolTerminal
			if strings.TrimSpace(toolOutput) == "" {
				toolOutput = terminalOutput
			}
		}
	}
	if strings.TrimSpace(toolOutput) == "" && !toolOutputHasTerminalData {
		toolOutput = transcript.DelegatedTaskResultText(fallbackInput)
	}
	if strings.TrimSpace(toolOutput) == "" && !toolOutputHasTerminalData {
		if exitText := transcript.TerminalExitCodeOutputText(fallbackInput); exitText != "" {
			toolOutput = exitText
			toolOutputSynthetic = true
		} else if transcript.TerminalNoOutputPlaceholder(fallbackInput) {
			toolOutput = "(no output)"
			toolOutputSynthetic = true
		} else if !transcript.TerminalFinalWithoutContent(fallbackInput) {
			toolOutput = transcript.StandardToolOutput(status, toolErr)
			toolOutputSynthetic = strings.TrimSpace(toolOutput) != ""
		}
	}
	// Task read/wait keeps latest_output compact for the model, but the
	// presentation owner needs the exact observed bytes to reconcile with
	// transient terminal frames without normalization, truncation, or
	// delivery-order races.
	if semanticName == surfaceToolTask {
		if delta, ok := transcriptToolObservationDelta(input.Meta); ok {
			toolOutput = delta
			toolOutputHasTerminalData = true
			toolOutputSynthetic = false
		} else {
			// A terminal anchor identifies the observed process, but does not
			// make a compact Task payload an exact byte delta.
			toolOutputHasTerminalData = false
		}
	}
	toolOutputGapBefore := toolOutputHasTerminalData && transcript.MetaInt(input.Meta, "caelis", "runtime", "stream", "truncated_before") > 0
	outputCursor, outputCursorKnown, outputStartCursor, outputStartCursorKnown := transcriptToolOutputRange(input.Meta)
	if transcript.SuppressToolResultOutputWithHint(semanticName, input.ToolKind, explorationVerb, toolOutput, toolOutputSynthetic, toolErr) {
		toolOutput = ""
		// A suppressed completion acknowledgement is still synthetic. Retaining
		// that fact prevents a contentless sparse final from erasing content that
		// an earlier update already materialized for the same call.
		toolOutputSynthetic = true
	}
	toolArgs, toolFullArgs := toolDisplayArgumentsWithRecoveredInput(semanticName, input.ToolKind, displayInput, len(recoveredInput) > 0, toolTitleDisplayArgs(semanticName, input.ToolKind, input.ToolTitle), acpprojector.FormatToolStart(presentation.DisplayName, displayInput))
	toolTaskHandle := firstNonEmpty(
		display.MapString(rawOutput, "handle"),
		display.MapString(rawInput, "handle"),
		display.MapString(rawOutput, "task_id"),
		display.MapString(rawInput, "task_id"),
		display.ToolTaskHandle(rawInput, taskOutput, input.Meta),
	)
	toolTaskAction := firstNonEmpty(
		display.MapString(rawOutput, "action"),
		display.MapString(rawInput, "action"),
		display.ToolTaskAction(rawInput, taskOutput, input.Meta),
	)
	toolTaskInput := firstNonEmpty(
		display.MapString(rawOutput, "input"),
		display.MapString(rawInput, "input"),
		display.ToolTaskInput(rawInput, taskOutput, input.Meta),
	)
	toolTaskTargetKind := firstNonEmpty(
		display.MapString(rawOutput, "target_kind"),
		display.ToolTaskTargetKind(rawInput, taskOutput, input.Meta),
	)
	toolTaskState := strings.ToLower(strings.TrimSpace(display.MapString(rawOutput, "state")))
	// Task write only records the interaction in the transcript. The observed
	// process bytes are owned by the original RunCommand panel and arrive on its
	// live terminal stream; replay likewise renders that command's final result.
	// Keep failed write output so the rejection reason remains visible, but do
	// not expose the internal command handle in that presentation-only detail.
	if semanticName == surfaceToolTask && strings.EqualFold(toolTaskAction, "write") && !toolErr {
		toolOutput = ""
		toolOutputHasTerminalData = false
		toolOutputSynthetic = false
		toolOutputGapBefore = false
	} else if semanticName == surfaceToolTask && strings.EqualFold(toolTaskAction, "write") {
		toolOutput = taskWriteFailureDisplayOutput(rawOutput, input.Meta, toolOutput, toolTaskHandle, status, toolErr)
	}
	toolMessageTarget := ""
	if semanticName == surfaceToolSendMessage {
		toolMessageTarget = display.AgentMessageTarget(firstNonEmpty(
			display.MapString(rawOutput, "to"),
			display.MapString(rawInput, "to"),
		))
	}
	if semanticName == surfaceToolTask {
		toolArgs = taskDisplayArgsWithHandle(toolArgs, toolTaskHandle)
	}
	if !toolErr && toolFullArgs == "" {
		if summary := toolDisplayStructuredSummary(semanticName, rawInput, summaryOutput, input.Meta); summary != "" {
			if transcript.ToolStatusFinal(status, toolErr) {
				toolArgs = summary
			}
		} else if len(rawInput) > 0 || strings.TrimSpace(toolOutput) != "" {
			if header := toolDisplayResultHeader(semanticName, toolOutput); header != "" {
				toolArgs = header
			}
		}
	}
	toolOutput = toolDisplayPanelOutput(semanticName, toolOutput)
	if input.GatewayProjection && isExecuteToolKind(input.ToolKind) && len(input.Content) == 0 {
		toolOutput = ""
		toolOutputSynthetic = false
	}
	return TranscriptEvent{
		Kind:                       TranscriptEventTool,
		Scope:                      input.Scope,
		ScopeID:                    input.ScopeID,
		Actor:                      input.Actor,
		OccurredAt:                 input.OccurredAt,
		Meta:                       transcript.CloneAnyMap(input.Meta),
		ToolCallID:                 strings.TrimSpace(input.CallID),
		ToolName:                   toolName,
		ToolKind:                   strings.TrimSpace(input.ToolKind),
		ToolTitle:                  strings.TrimSpace(input.ToolTitle),
		ToolExplorationVerb:        explorationVerb,
		ToolArgs:                   toolArgs,
		ToolFullArgs:               toolFullArgs,
		ToolOutput:                 toolOutput,
		ToolStream:                 transcript.ToolStream(status, toolErr),
		ToolStatus:                 status,
		ToolStatusExplicit:         input.StatusExplicit,
		ToolError:                  toolErr,
		ToolTerminal:               toolTerminal,
		ToolOutputSynthetic:        toolOutputSynthetic,
		ToolOutputTerminal:         toolOutputHasTerminalData,
		ToolOutputCursor:           outputCursor,
		ToolOutputCursorKnown:      outputCursorKnown,
		ToolOutputStartCursor:      outputStartCursor,
		ToolOutputStartCursorKnown: outputStartCursorKnown,
		ToolOutputGapBefore:        toolOutputGapBefore,
		ToolTaskHandle:             toolTaskHandle,
		ToolTaskAction:             toolTaskAction,
		ToolTaskInput:              toolTaskInput,
		ToolTaskTargetKind:         toolTaskTargetKind,
		ToolTaskState:              toolTaskState,
		ToolMessageTarget:          toolMessageTarget,
		Final:                      transcript.ToolStatusFinal(status, toolErr),
	}, true
}

func toolDisplayExplorationVerb(meta map[string]any) string {
	return metautil.String(meta, metautil.Root, metautil.Display, metautil.DisplayExplorationVerb)
}

func taskWriteFailureDisplayOutput(rawOutput map[string]any, meta map[string]any, output string, taskHandle string, status string, toolErr bool) string {
	if detail := firstNonEmpty(display.MapString(rawOutput, "error"), display.MapString(rawOutput, "stderr")); detail != "" {
		output = detail
	} else if _, hasObservationDelta := transcriptToolObservationDelta(meta); hasObservationDelta {
		// The observation delta belongs to the RunCommand panel even when the
		// write itself fails. With no separate rejection detail, show only the
		// control failure state rather than duplicating process output.
		output = transcript.StandardToolOutput(status, toolErr)
	}
	taskHandle = strings.TrimSpace(taskHandle)
	if taskHandle == "" {
		return output
	}
	for _, handle := range []string{`"` + taskHandle + `"`, `'` + taskHandle + `'`, taskHandle} {
		output = strings.ReplaceAll(output, handle, "")
	}
	lines := strings.Split(output, "\n")
	for i := range lines {
		lines[i] = strings.Join(strings.Fields(lines[i]), " ")
	}
	return strings.Join(lines, "\n")
}

func transcriptToolOutputRange(meta map[string]any) (end int64, endKnown bool, start int64, startKnown bool) {
	streamMode := metautil.String(meta,
		metautil.Root, metautil.Runtime, metautil.RuntimeStream, metautil.RuntimeStreamMode)
	if streamMode != "" {
		end, endKnown = metautil.Int64(meta,
			metautil.Root, metautil.Runtime, metautil.RuntimeStream, metautil.RuntimeOutputCursor)
	} else {
		end, endKnown = metautil.Int64(meta,
			metautil.Root, metautil.Runtime, metautil.RuntimeTask, metautil.RuntimeOutputCursor)
	}
	start, startKnown = metautil.Int64(meta,
		metautil.Root, metautil.Runtime, metautil.RuntimeTask, metautil.RuntimeOutputStart)
	if end < 0 {
		end, endKnown = 0, false
	}
	if start < 0 || (endKnown && start > end) {
		start, startKnown = 0, false
	}
	return end, endKnown, start, startKnown
}

func transcriptToolObservationDelta(meta map[string]any) (string, bool) {
	taskMeta := metautil.RuntimeSection(meta, metautil.RuntimeTask)
	delta, ok := taskMeta[metautil.RuntimeOutputDelta].(string)
	return delta, ok && delta != ""
}

func transcriptToolHasTerminal(meta map[string]any, content []acpprojector.ToolContent) bool {
	if transcript.HasTerminalPanelMeta(meta) {
		return true
	}
	return transcriptToolContentHasTerminal(content)
}

func transcriptToolContentHasTerminal(content []acpprojector.ToolContent) bool {
	for _, item := range content {
		if strings.EqualFold(strings.TrimSpace(item.Type), "terminal") {
			return true
		}
	}
	return false
}

func suppressRunningTerminalSnapshotOutput(toolName string, meta map[string]any, status string, isErr bool) bool {
	if isErr || transcript.ToolStatusFinal(status, isErr) {
		return false
	}
	if !surfaceIsTerminalPanelTool(toolName) {
		return false
	}
	if transcript.MetaString(meta, "caelis", "runtime", "stream", "mode") != "" {
		return false
	}
	taskMeta := transcript.RuntimeTaskMeta(meta)
	return firstNonEmpty(asString(taskMeta["task_id"]), asString(taskMeta["internal_task_id"]), asString(taskMeta["terminal_id"])) != ""
}
