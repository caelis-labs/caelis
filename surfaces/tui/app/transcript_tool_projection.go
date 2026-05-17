package tuiapp

import (
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/acpprojector"
)

type transcriptToolProjection struct {
	Event      kernel.Event
	Scope      ACPProjectionScope
	ScopeID    string
	OccurredAt time.Time
	Actor      string

	CallID    string
	ToolName  string
	ToolKind  string
	ToolTitle string
	Status    string

	RawInput  map[string]any
	RawOutput map[string]any
	Content   []session.ProtocolToolCallContent
	Error     bool
}

func projectTranscriptToolCall(input transcriptToolProjection) TranscriptEvent {
	toolName := gatewayToolDisplayName(input.ToolName, input.ToolTitle, input.ToolKind)
	status := strings.TrimSpace(input.Status)
	if status == "" || strings.EqualFold(status, string(kernel.ToolStatusStarted)) {
		status = string(kernel.ToolStatusRunning)
	}
	semanticName := toolSemanticName(toolName, input.ToolKind)
	rawInput := cloneAnyMap(input.RawInput)
	if refinedName := refinedToolDisplayName(semanticName, input.ToolKind, input.ToolTitle, rawInput); refinedName != "" {
		toolName = refinedName
		semanticName = refinedName
	}
	toolTaskID := toolDisplayTaskID(rawInput, nil, input.Event.Meta)
	displayInput := rawInput
	if strings.EqualFold(semanticName, "TASK") {
		displayInput = taskDisplayInputForResult(rawInput, toolDisplayMetaOutput(semanticName, input.Event.Meta))
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
		ToolTaskID:         toolTaskID,
		ToolTaskAction:     toolDisplayTaskAction(rawInput, nil, input.Event.Meta),
		ToolTaskInput:      toolDisplayTaskInput(rawInput, nil, input.Event.Meta),
		ToolTaskTargetKind: toolDisplayTaskTargetKind(rawInput, nil, input.Event.Meta),
	}
}

func projectTranscriptToolResult(input transcriptToolProjection, defaultSuccessStatus string) (TranscriptEvent, bool) {
	toolName := gatewayToolDisplayName(input.ToolName, input.ToolTitle, input.ToolKind)
	status := strings.TrimSpace(input.Status)
	toolErr := input.Error
	if status == "" {
		if toolErr {
			status = string(kernel.ToolStatusFailed)
		} else {
			status = strings.TrimSpace(defaultSuccessStatus)
		}
	}
	if status == "" {
		status = string(kernel.ToolStatusCompleted)
	}
	toolErr = toolErr || strings.EqualFold(status, string(kernel.ToolStatusFailed))
	semanticName := toolSemanticName(toolName, input.ToolKind)
	rawInput := cloneAnyMap(input.RawInput)
	rawOutput := cloneAnyMap(input.RawOutput)
	if refinedName := refinedToolDisplayName(semanticName, input.ToolKind, input.ToolTitle, rawInput); refinedName != "" {
		toolName = refinedName
		semanticName = refinedName
	}
	displayOutput := toolDisplayMetaOutput(semanticName, input.Event.Meta)
	displayInput := rawInput
	if strings.EqualFold(semanticName, "SPAWN") {
		displayInput = spawnDisplayInputForResult(rawInput, displayOutput)
	}
	if strings.EqualFold(semanticName, "TASK") {
		displayInput = taskDisplayInputForResult(rawInput, displayOutput)
	}
	toolOutput := acpprojector.FormatToolContent(input.Content)
	toolOutputSynthetic := false
	if strings.TrimSpace(toolOutput) == "" {
		if terminalOutput := terminalToolOutputText(semanticName, input.ToolKind, rawOutput, input.Event.Meta, input.Content, status, toolErr); terminalOutput != "" {
			toolOutput = terminalOutput
		} else if !terminalFinalWithoutContent(semanticName, input.ToolKind, status, toolErr) {
			toolOutput = standardToolOutput(status, toolErr)
			toolOutputSynthetic = strings.TrimSpace(toolOutput) != ""
		}
	}
	if taskWaitControlResult(semanticName, rawInput, displayOutput, input.Event.Meta) && !toolErr {
		toolOutput = ""
		toolOutputSynthetic = false
	}
	if suppressToolResultOutput(semanticName, input.ToolKind, toolOutput, toolOutputSynthetic, toolErr) {
		toolOutput = ""
		toolOutputSynthetic = false
	}
	toolArgs := toolDisplayArgs(semanticName, displayInput, toolTitleDisplayArgs(semanticName, input.ToolKind, input.ToolTitle), acpprojector.FormatToolStart(toolName, displayInput))
	toolTaskID := toolDisplayTaskID(rawInput, displayOutput, input.Event.Meta)
	if strings.EqualFold(semanticName, "TASK") {
		toolArgs = taskDisplayArgsWithTaskID(toolArgs, toolTaskID)
	}
	if !toolErr && (len(rawInput) > 0 || strings.TrimSpace(toolOutput) != "") {
		if header := toolDisplayResultHeader(semanticName, toolOutput); header != "" {
			toolArgs = header
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
		ToolOutputSynthetic: toolOutputSynthetic,
		ToolTaskID:          toolTaskID,
		ToolTaskAction:      toolDisplayTaskAction(rawInput, displayOutput, input.Event.Meta),
		ToolTaskInput:       toolDisplayTaskInput(rawInput, displayOutput, input.Event.Meta),
		ToolTaskTargetKind:  toolDisplayTaskTargetKind(rawInput, displayOutput, input.Event.Meta),
		Final:               transcriptToolStatusFinal(status, toolErr),
	}, true
}
