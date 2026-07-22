package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/surfaces/transcript"
	"github.com/caelis-labs/caelis/surfaces/tui/acpprojector"
)

func projectTranscriptToolCall(input transcript.ToolProjectionInput) TranscriptEvent {
	toolName := transcriptToolDisplayName(input.ToolName, input.ToolTitle, input.ToolKind)
	status := transcript.NormalizeToolStartStatus(input.Status)
	semanticName := toolSemanticName(toolName, input.ToolKind)
	rawInput := transcript.CloneAnyMap(input.RawInput)
	if refinedName := toolDisplaySemanticOverride(semanticName, input.ToolKind, input.ToolTitle, rawInput); refinedName != "" {
		toolName = refinedName
		semanticName = refinedName
	}
	toolTaskHandle := display.ToolTaskHandle(rawInput, nil, input.Meta)
	content := acpToolContentToDisplay(input.Content)
	toolTerminal := transcriptToolHasTerminal(input.Meta, content)
	displayInput := rawInput
	if strings.EqualFold(semanticName, "TASK") {
		displayInput = taskDisplayInputForResult(rawInput, toolDisplayMetaOutput(semanticName, input.Meta))
	}
	toolArgs := toolDisplayArgs(semanticName, displayInput, toolTitleDisplayArgs(semanticName, input.ToolKind, input.ToolTitle), acpprojector.FormatToolStart(toolName, displayInput))
	if strings.EqualFold(semanticName, "TASK") {
		toolArgs = taskDisplayArgsWithHandle(toolArgs, toolTaskHandle)
	}
	return TranscriptEvent{
		Kind:               TranscriptEventTool,
		Scope:              input.Scope,
		ScopeID:            input.ScopeID,
		Actor:              input.Actor,
		OccurredAt:         input.OccurredAt,
		Meta:               transcript.CloneAnyMap(input.Meta),
		ToolCallID:         strings.TrimSpace(input.CallID),
		ToolName:           toolName,
		ToolKind:           strings.TrimSpace(input.ToolKind),
		ToolTitle:          strings.TrimSpace(input.ToolTitle),
		ToolArgs:           toolArgs,
		ToolFullArgs:       toolDisplayFullArgs(semanticName, rawInput),
		ToolStatus:         status,
		ToolTerminal:       toolTerminal,
		ToolTaskHandle:     toolTaskHandle,
		ToolTaskAction:     display.ToolTaskAction(rawInput, nil, input.Meta),
		ToolTaskInput:      display.ToolTaskInput(rawInput, nil, input.Meta),
		ToolTaskTargetKind: display.ToolTaskTargetKind(rawInput, nil, input.Meta),
	}
}

func projectTranscriptToolResult(input transcript.ToolProjectionInput, defaultSuccessStatus string) (TranscriptEvent, bool) {
	toolName := transcriptToolDisplayName(input.ToolName, input.ToolTitle, input.ToolKind)
	semanticName := toolSemanticName(toolName, input.ToolKind)
	rawInput := transcript.CloneAnyMap(input.RawInput)
	rawOutput := transcript.RawMap(input.RawOutput)
	status, toolErr := transcript.NormalizeToolResultStatus(input.Status, rawOutput, input.Error, defaultSuccessStatus)
	content := acpToolContentToDisplay(input.Content)
	toolTerminal := transcriptToolHasTerminal(input.Meta, content)
	suppressRunningSnapshotOutput := suppressRunningTerminalSnapshotOutput(semanticName, input.ToolKind, input.Meta, status, toolErr)
	if refinedName := toolDisplaySemanticOverride(semanticName, input.ToolKind, input.ToolTitle, rawInput); refinedName != "" {
		toolName = refinedName
		semanticName = refinedName
	}
	summaryOutput := toolDisplaySummaryOutput(semanticName, rawOutput, input.Meta)
	displayOutput := toolDisplayMetaOutput(semanticName, input.Meta)
	taskOutput := transcript.CloneAnyMap(displayOutput)
	if taskOutput == nil {
		taskOutput = map[string]any{}
	}
	for key, value := range rawOutput {
		taskOutput[key] = value
	}
	displayInput := rawInput
	if strings.EqualFold(semanticName, "SPAWN") {
		displayInput = spawnDisplayInputForResult(rawInput, displayOutput)
	}
	if strings.EqualFold(semanticName, "TASK") {
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
	toolOutputGapBefore := toolOutputHasTerminalData && transcript.MetaInt(input.Meta, "caelis", "runtime", "stream", "truncated_before") > 0
	if transcript.SuppressToolResultOutput(semanticName, input.ToolKind, toolOutput, toolOutputSynthetic, toolErr) {
		toolOutput = ""
		toolOutputSynthetic = false
	}
	toolArgs := toolDisplayArgs(semanticName, displayInput, toolTitleDisplayArgs(semanticName, input.ToolKind, input.ToolTitle), acpprojector.FormatToolStart(toolName, displayInput))
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
	if strings.EqualFold(semanticName, "TASK") {
		toolArgs = taskDisplayArgsWithHandle(toolArgs, toolTaskHandle)
	}
	if !toolErr {
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
	return TranscriptEvent{
		Kind:                TranscriptEventTool,
		Scope:               input.Scope,
		ScopeID:             input.ScopeID,
		Actor:               input.Actor,
		OccurredAt:          input.OccurredAt,
		Meta:                transcript.CloneAnyMap(input.Meta),
		ToolCallID:          strings.TrimSpace(input.CallID),
		ToolName:            toolName,
		ToolKind:            strings.TrimSpace(input.ToolKind),
		ToolTitle:           strings.TrimSpace(input.ToolTitle),
		ToolArgs:            toolArgs,
		ToolFullArgs:        toolDisplayFullArgs(semanticName, displayInput),
		ToolOutput:          toolOutput,
		ToolStream:          transcript.ToolStream(status, toolErr),
		ToolStatus:          status,
		ToolError:           toolErr,
		ToolTerminal:        toolTerminal,
		ToolOutputSynthetic: toolOutputSynthetic,
		ToolOutputTerminal:  toolOutputHasTerminalData,
		ToolOutputGapBefore: toolOutputGapBefore,
		ToolTaskHandle:      toolTaskHandle,
		ToolTaskAction:      toolTaskAction,
		ToolTaskInput:       toolTaskInput,
		ToolTaskTargetKind:  toolTaskTargetKind,
		Final:               transcript.ToolStatusFinal(status, toolErr),
	}, true
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

func suppressRunningTerminalSnapshotOutput(toolName string, toolKind string, meta map[string]any, status string, isErr bool) bool {
	if isErr || transcript.ToolStatusFinal(status, isErr) {
		return false
	}
	if !display.IsTerminalPanelTool(toolName, toolKind) {
		return false
	}
	if transcript.MetaString(meta, "caelis", "runtime", "stream", "mode") != "" {
		return false
	}
	taskMeta := transcript.RuntimeTaskMeta(meta)
	return firstNonEmpty(asString(taskMeta["task_id"]), asString(taskMeta["internal_task_id"]), asString(taskMeta["terminal_id"])) != ""
}
