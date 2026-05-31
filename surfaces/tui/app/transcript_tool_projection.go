package tuiapp

import (
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/acpprojector"
)

const (
	transcriptToolStatusStarted   = "started"
	transcriptToolStatusRunning   = "running"
	transcriptToolStatusCompleted = "completed"
	transcriptToolStatusFailed    = "failed"
)

type transcriptToolProjection struct {
	Scope      ACPProjectionScope
	ScopeID    string
	OccurredAt time.Time
	Actor      string
	Meta       map[string]any

	CallID    string
	ToolName  string
	ToolKind  string
	ToolTitle string
	Status    string

	RawInput  map[string]any
	RawOutput map[string]any
	Content   []schema.ToolCallContent
	Error     bool
}

func projectTranscriptToolCall(input transcriptToolProjection) TranscriptEvent {
	toolName := transcriptToolDisplayName(input.ToolName, input.ToolTitle, input.ToolKind)
	status := strings.TrimSpace(input.Status)
	if status == "" || strings.EqualFold(status, transcriptToolStatusStarted) {
		status = transcriptToolStatusRunning
	}
	semanticName := toolSemanticName(toolName, input.ToolKind)
	rawInput := cloneAnyMap(input.RawInput)
	if refinedName := refinedToolDisplayName(semanticName, input.ToolKind, input.ToolTitle, rawInput); refinedName != "" {
		toolName = refinedName
		semanticName = refinedName
	}
	toolTaskID := toolDisplayTaskID(rawInput, nil, input.Meta)
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
		ToolTaskID:         toolTaskID,
		ToolTaskAction:     toolDisplayTaskAction(rawInput, nil, input.Meta),
		ToolTaskInput:      toolDisplayTaskInput(rawInput, nil, input.Meta),
		ToolTaskTargetKind: toolDisplayTaskTargetKind(rawInput, nil, input.Meta),
	}
}

func projectTranscriptToolResult(input transcriptToolProjection, defaultSuccessStatus string) (TranscriptEvent, bool) {
	toolName := transcriptToolDisplayName(input.ToolName, input.ToolTitle, input.ToolKind)
	status := strings.TrimSpace(input.Status)
	toolErr := input.Error
	if status == "" {
		if toolErr {
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
	rawInput := cloneAnyMap(input.RawInput)
	rawOutput := cloneAnyMap(input.RawOutput)
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
	toolOutput := acpprojector.FormatToolContent(input.Content)
	toolOutputSynthetic := false
	if strings.TrimSpace(toolOutput) == "" {
		if mutationOutput := mutationToolOutputText(semanticName, rawInput, rawOutput, input.Meta); mutationOutput != "" && !toolErr {
			toolOutput = mutationOutput
		} else if terminalOutput := terminalToolOutputText(semanticName, input.ToolKind, rawOutput, input.Meta, input.Content, status, toolErr); terminalOutput != "" {
			toolOutput = terminalOutput
		} else if terminalNoOutputPlaceholder(semanticName, input.ToolKind, rawOutput, input.Meta, input.Content, status, toolErr) {
			toolOutput = "(no output)"
			toolOutputSynthetic = true
		} else if !terminalFinalWithoutContent(semanticName, input.ToolKind, status, toolErr) {
			toolOutput = standardToolOutput(status, toolErr)
			toolOutputSynthetic = strings.TrimSpace(toolOutput) != ""
		}
	}
	if taskWaitControlResult(semanticName, rawInput, displayOutput, input.Meta) && !toolErr {
		toolOutput = ""
		toolOutputSynthetic = false
	}
	if suppressToolResultOutput(semanticName, input.ToolKind, toolOutput, toolOutputSynthetic, toolErr) {
		toolOutput = ""
		toolOutputSynthetic = false
	}
	toolArgs := toolDisplayArgs(semanticName, displayInput, toolTitleDisplayArgs(semanticName, input.ToolKind, input.ToolTitle), acpprojector.FormatToolStart(toolName, displayInput))
	toolTaskID := toolDisplayTaskID(rawInput, displayOutput, input.Meta)
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
		ToolOutputSynthetic: toolOutputSynthetic,
		ToolTaskID:          toolTaskID,
		ToolTaskAction:      toolDisplayTaskAction(rawInput, displayOutput, input.Meta),
		ToolTaskInput:       toolDisplayTaskInput(rawInput, displayOutput, input.Meta),
		ToolTaskTargetKind:  toolDisplayTaskTargetKind(rawInput, displayOutput, input.Meta),
		Final:               transcriptToolStatusFinal(status, toolErr),
	}, true
}
