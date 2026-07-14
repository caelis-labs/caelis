package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
)

func updateLinkedTerminalEvent(events []SubagentEvent, callID string, toolName string, taskID string, output string, final bool, err bool, meta ToolUpdateMeta) bool {
	toolName = strings.TrimSpace(toolName)
	taskID = strings.TrimSpace(taskID)
	authoritativeFinal := toolFinalOutputAuthoritative(err, meta.ToolStatus)
	if strings.EqualFold(toolName, "SPAWN") {
		if updateLinkedTaskWriteEvent(events, taskID, output, meta.MessageID, final, err, meta.Terminal, meta.OutputNarrative, authoritativeFinal) {
			return true
		}
		return updateLinkedSpawnEvent(events, strings.TrimSpace(callID), taskID, output, meta.MessageID, final, err, meta.Terminal, meta.OutputNarrative, authoritativeFinal)
	}
	return false
}

func updateLinkedSpawnEvent(events []SubagentEvent, callID string, taskID string, output string, messageID string, final bool, err bool, terminal bool, outputNarrative bool, authoritativeFinal bool) bool {
	taskID = strings.TrimSpace(taskID)
	mergeOutput := shouldMergeOpenToolOutput("", output, terminal)
	if taskID == "" || (!mergeOutput && !final) {
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
		wasDone := ev.Done
		if mergeOutput {
			mergeLinkedSubagentOutput(ev, output, messageID, final, outputNarrative, authoritativeFinal)
		}
		if terminal {
			ev.Terminal = true
		}
		if final {
			ev.Done = true
			ev.Err = err
		} else if mergeOutput && !wasDone {
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

func updateLinkedTaskWriteEvent(events []SubagentEvent, taskID string, output string, messageID string, final bool, err bool, terminal bool, outputNarrative bool, authoritativeFinal bool) bool {
	taskID = strings.TrimSpace(taskID)
	mergeOutput := shouldMergeOpenToolOutput("", output, terminal)
	if taskID == "" || (!mergeOutput && !final) {
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
		wasDone := ev.Done
		if mergeOutput {
			mergeLinkedSubagentOutput(ev, output, messageID, final, outputNarrative, authoritativeFinal)
		}
		if terminal {
			ev.Terminal = true
		}
		if final {
			ev.Done = true
			ev.Err = err
		} else if mergeOutput && !wasDone {
			ev.Done = false
			ev.Err = false
		}
		return true
	}
	return false
}

func mergeLinkedSubagentOutput(ev *SubagentEvent, output string, messageID string, final bool, outputNarrative bool, authoritativeFinal bool) {
	if ev == nil {
		return
	}
	if ev.Done && !final {
		return
	}
	if final {
		if (authoritativeFinal && renderableTextHasContent(output)) || !ev.OutputNarrative || subagentFinalOutputShouldReplace(ev.Output, output) {
			ev.Output = output
		}
	} else if outputNarrative || ev.OutputNarrative {
		ev.Output, ev.OutputMessage = mergeSubagentNarrativeChunk(
			ev.Output,
			ev.OutputMessageID,
			ev.OutputMessage,
			output,
			messageID,
		)
		if messageID = strings.TrimSpace(messageID); messageID != "" {
			ev.OutputMessageID = messageID
		}
	} else {
		ev.Output = mergeSubagentStreamChunk(ev.Output, output)
	}
	ev.OutputNarrative = ev.OutputNarrative || outputNarrative
	ev.OutputSynthetic = false
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

func shouldIgnoreStaleTerminalUpdate(events []SubagentEvent, callID string, name string, toolKind string, terminal bool, final bool) bool {
	if final || strings.TrimSpace(callID) == "" || (!terminal && !display.IsTerminalPanelTool(name, toolKind)) {
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
