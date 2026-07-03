package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/ports/displaypolicy"
)

func updateLinkedTerminalEvent(events []SubagentEvent, callID string, toolName string, taskID string, output string, final bool, err bool, meta ToolUpdateMeta) bool {
	toolName = strings.TrimSpace(toolName)
	taskID = strings.TrimSpace(taskID)
	if strings.EqualFold(toolName, "SPAWN") {
		if updateLinkedTaskWriteEvent(events, taskID, output, final, err, meta.Terminal) {
			return true
		}
		return updateLinkedSpawnEvent(events, strings.TrimSpace(callID), taskID, output, final, err, meta.Terminal)
	}
	return false
}

func updateLinkedSpawnEvent(events []SubagentEvent, callID string, taskID string, output string, final bool, err bool, terminal bool) bool {
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
		if mergeOutput {
			if final || ev.Done {
				ev.Output = output
			} else {
				ev.Output = mergeSubagentStreamChunk(ev.Output, output)
			}
			ev.OutputSynthetic = false
		}
		if terminal {
			ev.Terminal = true
		}
		if final {
			ev.Done = true
			ev.Err = err
		} else if mergeOutput {
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

func updateLinkedTaskWriteEvent(events []SubagentEvent, taskID string, output string, final bool, err bool, terminal bool) bool {
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
		if mergeOutput {
			if final || ev.Done {
				ev.Output = output
			} else {
				ev.Output = mergeSubagentStreamChunk(ev.Output, output)
			}
			ev.OutputSynthetic = false
		}
		if terminal {
			ev.Terminal = true
		}
		if final {
			ev.Done = true
			ev.Err = err
		} else if mergeOutput {
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

func shouldIgnoreStaleTerminalUpdate(events []SubagentEvent, callID string, name string, toolKind string, terminal bool, final bool) bool {
	if final || strings.TrimSpace(callID) == "" || (!terminal && !displaypolicy.IsTerminalPanelTool(name, toolKind)) {
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
