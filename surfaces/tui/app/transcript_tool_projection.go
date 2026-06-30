package tuiapp

import (
	"strconv"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/displaypolicy"
	"github.com/OnslaughtSnail/caelis/surfaces/transcript"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/acpprojector"
)

const (
	transcriptToolStatusStarted     = "started"
	transcriptToolStatusRunning     = "running"
	transcriptToolStatusCompleted   = "completed"
	transcriptToolStatusFailed      = "failed"
	transcriptToolStatusInterrupted = "interrupted"
	transcriptToolStatusCancelled   = "cancelled"
)

func projectTranscriptToolCall(input transcript.ToolProjectionInput) TranscriptEvent {
	toolName := transcriptToolDisplayName(input.ToolName, input.ToolTitle, input.ToolKind)
	status := strings.TrimSpace(input.Status)
	if status == "" || strings.EqualFold(status, transcriptToolStatusStarted) {
		status = transcriptToolStatusRunning
	}
	semanticName := toolSemanticName(toolName, input.ToolKind)
	rawInput := transcript.CloneAnyMap(input.RawInput)
	if refinedName := refinedToolDisplayName(semanticName, input.ToolKind, input.ToolTitle, rawInput); refinedName != "" {
		toolName = refinedName
		semanticName = refinedName
	}
	toolTaskID := displaypolicy.ToolTaskID(rawInput, nil, input.Meta)
	content := acpToolContentToDisplay(input.Content)
	toolTerminal := transcriptToolHasTerminal(input.Meta, content)
	displayInput := rawInput
	if strings.EqualFold(semanticName, "TASK") {
		displayInput = taskDisplayInputForResult(rawInput, toolDisplayMetaOutput(semanticName, input.Meta))
	}
	toolArgs := toolDisplayArgs(semanticName, displayInput, toolTitleDisplayArgs(semanticName, input.ToolKind, input.ToolTitle), acpprojector.FormatToolStart(toolName, displayInput))
	if strings.EqualFold(semanticName, "TASK") {
		toolArgs = taskDisplayArgsWithTaskID(toolArgs, toolTaskID)
	}
	return TranscriptEvent{
		Kind:               TranscriptEventTool,
		Scope:              input.Scope,
		ScopeID:            input.ScopeID,
		Actor:              input.Actor,
		OccurredAt:         input.OccurredAt,
		ToolCallID:         strings.TrimSpace(input.CallID),
		ToolName:           toolName,
		ToolKind:           strings.TrimSpace(input.ToolKind),
		ToolTitle:          strings.TrimSpace(input.ToolTitle),
		ToolArgs:           toolArgs,
		ToolFullArgs:       toolDisplayFullArgs(semanticName, rawInput),
		ToolStatus:         status,
		ToolTerminal:       toolTerminal,
		ToolTaskID:         toolTaskID,
		ToolTaskAction:     displaypolicy.ToolTaskAction(rawInput, nil, input.Meta),
		ToolTaskInput:      displaypolicy.ToolTaskInput(rawInput, nil, input.Meta),
		ToolTaskTargetKind: displaypolicy.ToolTaskTargetKind(rawInput, nil, input.Meta),
	}
}

func projectTranscriptToolResult(input transcript.ToolProjectionInput, defaultSuccessStatus string) (TranscriptEvent, bool) {
	toolName := transcriptToolDisplayName(input.ToolName, input.ToolTitle, input.ToolKind)
	status := strings.TrimSpace(input.Status)
	toolErr := input.Error
	if status == "" {
		rawOutput := transcript.RawMap(input.RawOutput)
		if inferred, ok := inferFinalStatusFromRawOutput(rawOutput); ok {
			status = inferred
		} else if toolErr {
			status = transcriptToolStatusFailed
		} else {
			status = strings.TrimSpace(defaultSuccessStatus)
		}
	}
	if status == "" {
		status = transcriptToolStatusCompleted
	}
	toolErr = toolErr || strings.EqualFold(status, transcriptToolStatusFailed)
	semanticName := toolSemanticName(toolName, input.ToolKind)
	rawInput := transcript.CloneAnyMap(input.RawInput)
	rawOutput := transcript.RawMap(input.RawOutput)
	content := acpToolContentToDisplay(input.Content)
	toolTerminal := transcriptToolHasTerminal(input.Meta, content)
	suppressRunningSnapshotOutput := suppressRunningTerminalSnapshotOutput(semanticName, input.ToolKind, input.Meta, status, toolErr)
	if refinedName := refinedToolDisplayName(semanticName, input.ToolKind, input.ToolTitle, rawInput); refinedName != "" {
		toolName = refinedName
		semanticName = refinedName
	}
	summaryOutput := toolDisplaySummaryOutput(semanticName, rawOutput, input.Meta)
	displayOutput := toolDisplayMetaOutput(semanticName, input.Meta)
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
	if !suppressRunningSnapshotOutput {
		terminalOutput := terminalToolOutputText(semanticName, input.ToolKind, rawOutput, input.Meta, content, status, toolErr)
		if terminalOutput != "" {
			toolOutputHasTerminalData = toolTerminal
			if strings.TrimSpace(toolOutput) == "" {
				toolOutput = terminalOutput
			}
		}
	}
	if strings.TrimSpace(toolOutput) == "" && !toolOutputHasTerminalData {
		if exitText := terminalExitCodeOutputText(semanticName, input.ToolKind, rawOutput, status, toolErr); exitText != "" {
			toolOutput = exitText
			toolOutputSynthetic = true
		} else if terminalNoOutputPlaceholder(semanticName, input.ToolKind, rawOutput, input.Meta, content, status, toolErr) {
			toolOutput = "(no output)"
			toolOutputSynthetic = true
		} else if !terminalFinalWithoutContent(semanticName, input.ToolKind, status, toolErr) {
			toolOutput = standardToolOutput(status, toolErr)
			toolOutputSynthetic = strings.TrimSpace(toolOutput) != ""
		}
	}
	if taskControlResult(semanticName, rawInput, displayOutput, input.Meta) {
		toolOutput = ""
		toolOutputSynthetic = false
	}
	if suppressToolResultOutput(semanticName, input.ToolKind, toolOutput, toolOutputSynthetic, toolErr) {
		toolOutput = ""
		toolOutputSynthetic = false
	}
	toolArgs := toolDisplayArgs(semanticName, displayInput, toolTitleDisplayArgs(semanticName, input.ToolKind, input.ToolTitle), acpprojector.FormatToolStart(toolName, displayInput))
	toolTaskID := displaypolicy.ToolTaskID(rawInput, displayOutput, input.Meta)
	if strings.EqualFold(semanticName, "TASK") {
		toolArgs = taskDisplayArgsWithTaskID(toolArgs, toolTaskID)
	}
	if !toolErr {
		if summary := toolDisplayStructuredSummary(semanticName, rawInput, summaryOutput, input.Meta); summary != "" {
			if transcriptToolStatusFinal(status, toolErr) {
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
		ToolCallID:          strings.TrimSpace(input.CallID),
		ToolName:            toolName,
		ToolKind:            strings.TrimSpace(input.ToolKind),
		ToolTitle:           strings.TrimSpace(input.ToolTitle),
		ToolArgs:            toolArgs,
		ToolFullArgs:        toolDisplayFullArgs(semanticName, displayInput),
		ToolOutput:          toolOutput,
		ToolStream:          transcriptToolStream(status, toolErr),
		ToolStatus:          status,
		ToolError:           toolErr,
		ToolTerminal:        toolTerminal,
		ToolOutputSynthetic: toolOutputSynthetic,
		ToolOutputTerminal:  toolOutputHasTerminalData,
		ToolTaskID:          toolTaskID,
		ToolTaskAction:      displaypolicy.ToolTaskAction(rawInput, displayOutput, input.Meta),
		ToolTaskInput:       displaypolicy.ToolTaskInput(rawInput, displayOutput, input.Meta),
		ToolTaskTargetKind:  displaypolicy.ToolTaskTargetKind(rawInput, displayOutput, input.Meta),
		Final:               transcriptToolStatusFinal(status, toolErr),
	}, true
}

func transcriptToolHasTerminal(meta map[string]any, content []acpprojector.ToolContent) bool {
	if hasTerminalPanelMeta(meta) {
		return true
	}
	for _, item := range content {
		if strings.EqualFold(strings.TrimSpace(item.Type), "terminal") {
			return true
		}
	}
	return false
}

func suppressRunningTerminalSnapshotOutput(toolName string, toolKind string, meta map[string]any, status string, isErr bool) bool {
	if isErr || transcriptToolStatusFinal(status, isErr) {
		return false
	}
	if !isTerminalPanelToolKind(toolName, toolKind) {
		return false
	}
	if transcript.MetaString(meta, "caelis", "runtime", "stream", "mode") != "" {
		return false
	}
	taskMeta := eventRuntimeTaskMeta(meta)
	return firstNonEmpty(asString(taskMeta["task_id"]), asString(taskMeta["internal_task_id"]), asString(taskMeta["terminal_id"])) != ""
}

func inferFinalStatusFromRawOutput(rawOutput map[string]any) (string, bool) {
	if len(rawOutput) == 0 {
		return "", false
	}
	if state := strings.ToLower(strings.TrimSpace(asString(rawOutput["state"]))); state != "" {
		switch state {
		case "completed", "failed", "interrupted", "cancelled", "canceled":
			if state == "canceled" {
				return transcriptToolStatusCancelled, true
			}
			return state, true
		case "terminated", "timed_out", "timeout":
			return transcriptToolStatusInterrupted, true
		}
	}
	exitCode, ok := transcriptRawExitCode(rawOutput)
	if !ok {
		return "", false
	}
	if exitCode < 0 {
		return transcriptToolStatusCancelled, true
	}
	if exitCode == 0 {
		return transcriptToolStatusCompleted, true
	}
	return transcriptToolStatusFailed, true
}

func transcriptRawExitCode(rawOutput map[string]any) (int, bool) {
	raw, ok := rawOutput["exit_code"]
	if !ok || raw == nil {
		return 0, false
	}
	switch typed := raw.(type) {
	case int:
		return typed, true
	case int8:
		return int(typed), true
	case int16:
		return int(typed), true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case uint:
		return int(typed), true
	case uint8:
		return int(typed), true
	case uint16:
		return int(typed), true
	case uint32:
		return int(typed), true
	case uint64:
		return int(typed), true
	case float64:
		return int(typed), true
	case float32:
		return int(typed), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err == nil {
			return parsed, true
		}
	}
	return 0, false
}
