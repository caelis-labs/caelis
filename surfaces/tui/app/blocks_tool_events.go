package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
)

type ToolUpdateMeta struct {
	TaskHandle      string
	TaskAction      string
	TaskInput       string
	TaskTargetKind  string
	ToolKind        string
	FullArgs        string
	MessageID       string
	ToolStatus      string
	OutputNarrative bool
	// OutputAuthoritative marks a canonical semantic final that must replace a
	// transient preview for the same physical task panel.
	OutputAuthoritative bool
	Terminal            bool
	OutputSynthetic     bool
	OutputTerminal      bool
	OutputGapBefore     bool
}

type toolEventUpdate struct {
	CallID          string
	Name            string
	Args            string
	Output          string
	Final           bool
	Err             bool
	Meta            ToolUpdateMeta
	SkipErroredOpen bool
}

func applyToolEventUpdate(events []SubagentEvent, update toolEventUpdate, toolIndex map[string]int) (out []SubagentEvent, changed bool, collapse bool) {
	out = events
	callID := strings.TrimSpace(update.CallID)
	name := strings.TrimSpace(update.Name)
	args := strings.TrimSpace(update.Args)
	toolKind := strings.TrimSpace(update.Meta.ToolKind)
	fullArgs := strings.TrimSpace(update.Meta.FullArgs)
	messageID := strings.TrimSpace(update.Meta.MessageID)
	taskHandle := strings.TrimSpace(update.Meta.TaskHandle)
	taskAction := strings.ToLower(strings.TrimSpace(update.Meta.TaskAction))
	taskInput := strings.TrimSpace(update.Meta.TaskInput)
	taskTargetKind := strings.ToLower(strings.TrimSpace(update.Meta.TaskTargetKind))
	authoritativeFinal := update.Meta.OutputAuthoritative || toolFinalOutputAuthoritative(update.Err, update.Meta.ToolStatus)
	effectiveName, effectiveToolKind, openIdx := effectiveToolEventIdentity(out, update, toolIndex, name, toolKind)
	semanticName := toolSemanticName(effectiveName, effectiveToolKind)
	output := normalizeToolEventOutput(update.Output, effectiveName, effectiveToolKind, update.Meta.Terminal)
	if strings.EqualFold(semanticName, "TASK") && taskAction == "cancel" {
		args = taskCancelArgsWithLinkedCommand(args, out, taskHandle)
	}
	defer func() {
		var moved bool
		out, moved = relocateApprovalReviewEventsAfterTool(out, callID)
		changed = changed || moved
		updateToolEventIndex(toolIndex, out, callID)
	}()
	if updateLinkedTerminalEvent(out, callID, semanticName, taskHandle, output, update.Final, update.Err, update.Meta) {
		changed = true
		if strings.EqualFold(semanticName, "SPAWN") {
			return out, changed, false
		}
		output = ""
	}
	if shouldIgnoreStaleTerminalUpdate(out, callID, effectiveName, effectiveToolKind, update.Meta.Terminal, update.Final) {
		return out, changed, false
	}
	if !update.Final {
		if i := openIdx; i >= 0 {
			ev := &out[i]
			mergeOpenToolEvent(ev, name, toolKind, args, fullArgs, output, messageID, taskHandle, taskAction, taskInput, taskTargetKind, semanticName, update.Meta.Terminal, update.Meta.OutputNarrative, update.Meta.OutputTerminal, update.Meta.OutputGapBefore)
			return out, true, false
		}
		out = append(out, SubagentEvent{
			Kind:            SEToolCall,
			CallID:          callID,
			Name:            name,
			ToolKind:        toolKind,
			Args:            args,
			StartArgs:       args,
			FullArgs:        fullArgs,
			Output:          output,
			OutputMessageID: messageID,
			OutputMessage:   output,
			OutputNarrative: update.Meta.OutputNarrative,
			Terminal:        update.Meta.Terminal,
			OutputSynthetic: update.Meta.OutputSynthetic,
			OutputTerminal:  update.Meta.OutputTerminal,
			OutputGapBefore: update.Meta.OutputGapBefore,
			TaskHandle:      taskHandle,
			TaskAction:      taskAction,
			TaskInput:       taskInput,
			TaskTargetKind:  taskTargetKind,
		})
		return out, true, false
	}

	finalEvent := SubagentEvent{
		Kind:            SEToolCall,
		CallID:          callID,
		Name:            name,
		ToolKind:        toolKind,
		Args:            args,
		StartArgs:       args,
		FullArgs:        fullArgs,
		Output:          output,
		OutputMessageID: messageID,
		OutputMessage:   output,
		OutputNarrative: update.Meta.OutputNarrative,
		Terminal:        update.Meta.Terminal,
		OutputSynthetic: update.Meta.OutputSynthetic,
		OutputTerminal:  update.Meta.OutputTerminal,
		OutputGapBefore: update.Meta.OutputGapBefore,
		Done:            true,
		Err:             update.Err,
		TaskHandle:      taskHandle,
		TaskAction:      taskAction,
		TaskInput:       taskInput,
		TaskTargetKind:  taskTargetKind,
	}
	if i := openToolEventIndexForUpdate(out, update, toolIndex); i >= 0 {
		ev := &out[i]
		if !ev.Done {
			mergeOpenFinalToolEvent(ev, &finalEvent, authoritativeFinal)
			if shouldDefaultCollapseToolEvent(finalEvent) {
				collapse = true
			}
			return out, true, collapse
		}
	}
	for i := len(out) - 1; i >= 0; i-- {
		ev := &out[i]
		if ev.Kind != SEToolCall || strings.TrimSpace(ev.CallID) != callID {
			continue
		}
		fillMissingFinalToolEventFromExisting(&finalEvent, *ev)
		if shouldReplaceCompletedTerminalToolEvent(*ev, finalEvent) || shouldReplaceCompletedSubagentToolEvent(*ev, finalEvent) {
			mergeFinalToolEvent(ev, &finalEvent, authoritativeFinal)
			if shouldDefaultCollapseToolEvent(finalEvent) {
				collapse = true
			}
			return out, true, collapse
		}
		break
	}
	if shouldSuppressAnonymousSyntheticFinalToolEvent(finalEvent) {
		return out, false, false
	}
	out = append(out, finalEvent)
	if shouldDefaultCollapseToolEvent(finalEvent) {
		collapse = true
	}
	return out, true, collapse
}

func shouldReplaceCompletedSubagentToolEvent(existing SubagentEvent, incoming SubagentEvent) bool {
	if !existing.Done || !incoming.Done || strings.TrimSpace(existing.CallID) == "" || strings.TrimSpace(existing.CallID) != strings.TrimSpace(incoming.CallID) {
		return false
	}
	existingName := toolSemanticName(existing.Name, existing.ToolKind)
	incomingName := toolSemanticName(incoming.Name, incoming.ToolKind)
	return strings.EqualFold(existingName, incomingName) &&
		(strings.EqualFold(existingName, "SPAWN") || strings.EqualFold(existingName, "TASK"))
}

func effectiveToolEventIdentity(events []SubagentEvent, update toolEventUpdate, toolIndex map[string]int, name string, toolKind string) (string, string, int) {
	idx := openToolEventIndexForUpdate(events, update, toolIndex)
	if idx < 0 {
		return name, toolKind, idx
	}
	existing := events[idx]
	if strings.TrimSpace(name) == "" {
		name = strings.TrimSpace(existing.Name)
	}
	if strings.TrimSpace(toolKind) == "" {
		toolKind = strings.TrimSpace(existing.ToolKind)
	}
	return name, toolKind, idx
}

func normalizeToolEventOutput(output string, effectiveName string, effectiveToolKind string, terminal bool) string {
	if terminal || display.IsTerminalPanelTool(effectiveName, effectiveToolKind) {
		return output
	}
	return strings.TrimSpace(output)
}

func openToolEventIndexForUpdate(events []SubagentEvent, update toolEventUpdate, toolIndex map[string]int) int {
	callID := strings.TrimSpace(update.CallID)
	if callID == "" {
		return -1
	}
	if toolIndex != nil {
		if idx, ok := toolIndex[callID]; ok && validOpenToolEventForUpdate(events, idx, callID, update.SkipErroredOpen) {
			return idx
		}
	}
	for i := len(events) - 1; i >= 0; i-- {
		if validOpenToolEventForUpdate(events, i, callID, update.SkipErroredOpen) {
			return i
		}
	}
	return -1
}

func validOpenToolEventForUpdate(events []SubagentEvent, idx int, callID string, skipErroredOpen bool) bool {
	if idx < 0 || idx >= len(events) {
		return false
	}
	ev := events[idx]
	return ev.Kind == SEToolCall &&
		strings.TrimSpace(ev.CallID) == callID &&
		!ev.Done &&
		(!skipErroredOpen || !ev.Err)
}

func updateToolEventIndex(index map[string]int, events []SubagentEvent, callID string) {
	callID = strings.TrimSpace(callID)
	if index == nil || callID == "" {
		return
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == SEToolCall && strings.TrimSpace(events[i].CallID) == callID {
			index[callID] = i
			return
		}
	}
	delete(index, callID)
}

func mergeOpenToolEvent(ev *SubagentEvent, name, toolKind, args, fullArgs, output, messageID, taskHandle, taskAction, taskInput, taskTargetKind string, semanticName string, terminal bool, outputNarrative bool, outputTerminal bool, outputGapBefore bool) {
	if ev == nil {
		return
	}
	if strings.TrimSpace(name) != "" {
		ev.Name = name
	}
	if strings.TrimSpace(toolKind) != "" {
		ev.ToolKind = toolKind
	}
	preferredTaskHandle := preferredDisplayTaskHandle(ev.TaskHandle, taskHandle)
	if strings.TrimSpace(args) != "" {
		ev.Args = args
	}
	mergeStartArgs(ev, args, ev.Args)
	if strings.TrimSpace(fullArgs) != "" {
		ev.FullArgs = fullArgs
	}
	ev.TaskHandle = preferredTaskHandle
	if ev.TaskAction == "" {
		ev.TaskAction = taskAction
	}
	if ev.TaskInput == "" {
		ev.TaskInput = taskInput
	}
	if ev.TaskTargetKind == "" {
		ev.TaskTargetKind = taskTargetKind
	}
	if terminal {
		ev.Terminal = true
	}
	ev.OutputGapBefore = ev.OutputGapBefore || outputGapBefore
	// Spawn keeps terminal-panel styling, but its live output is structured
	// child narrative rather than terminal bytes and must retain message scope.
	terminalOutput := isTerminalPanelToolEvent(*ev) && !strings.EqualFold(semanticName, "SPAWN")
	if shouldMergeOpenToolOutput(semanticName, output, terminalOutput) {
		if terminalOutput {
			if outputTerminal {
				// ACP terminal_output is an exact byte delta. Repeated lines and
				// prefix-like chunks are real output, not evidence of a cumulative
				// snapshot, so append them without overlap guessing.
				ev.Output += output
			} else {
				ev.Output = mergeCommandStreamChunk(ev.Output, output)
			}
		} else {
			if outputNarrative && ev.OutputNarrativeBoundary {
				ev.Output = joinSubagentNarrativeMessages(ev.Output, output)
				ev.OutputMessage = output
				ev.OutputMessageID = ""
				ev.OutputNarrativeBoundary = false
			} else {
				ev.Output, ev.OutputMessage = mergeSubagentNarrativeChunk(ev.Output, ev.OutputMessageID, ev.OutputMessage, output, messageID)
			}
			if messageID != "" {
				ev.OutputMessageID = messageID
			}
		}
		ev.OutputNarrative = ev.OutputNarrative || outputNarrative
		ev.OutputSynthetic = false
		if terminalOutput {
			ev.OutputTerminal = true
		}
	}
}

func shouldMergeOpenToolOutput(semanticName string, output string, terminal bool) bool {
	if output == "" {
		return false
	}
	if renderableTextHasContent(output) {
		return true
	}
	return terminal
}

func fillFinalToolEventFromExisting(finalEvent *SubagentEvent, existing SubagentEvent) {
	if finalEvent == nil {
		return
	}
	if strings.TrimSpace(finalEvent.Name) == "" {
		finalEvent.Name = strings.TrimSpace(existing.Name)
	}
	if shouldUseExistingArgsForFinal(*finalEvent, existing) {
		finalEvent.Args = strings.TrimSpace(existing.Args)
	}
	mergeStartArgs(finalEvent, existing.StartArgs, existing.Args, finalEvent.Args)
	if shouldUseExistingFullArgsForFinal(*finalEvent, existing) {
		finalEvent.FullArgs = strings.TrimSpace(existing.FullArgs)
	}
	if strings.TrimSpace(finalEvent.ToolKind) == "" {
		finalEvent.ToolKind = strings.TrimSpace(existing.ToolKind)
	}
	if !finalEvent.Terminal {
		finalEvent.Terminal = existing.Terminal
	}
}

func shouldUseExistingArgsForFinal(finalEvent SubagentEvent, existing SubagentEvent) bool {
	if strings.TrimSpace(finalEvent.Args) == "" {
		return true
	}
	if !strings.EqualFold(toolSemanticName(finalEvent.Name, finalEvent.ToolKind), "SPAWN") {
		return false
	}
	return shouldReplaceSpawnDisplayArgs(finalEvent.Args, existing.Args)
}

func shouldUseExistingFullArgsForFinal(finalEvent SubagentEvent, existing SubagentEvent) bool {
	if strings.TrimSpace(finalEvent.FullArgs) == "" {
		return true
	}
	if !strings.EqualFold(toolSemanticName(finalEvent.Name, finalEvent.ToolKind), "SPAWN") {
		return false
	}
	return shouldReplaceSpawnDisplayArgs(finalEvent.FullArgs, existing.FullArgs)
}

func fillMissingFinalToolEventFromExisting(finalEvent *SubagentEvent, existing SubagentEvent) {
	if finalEvent == nil {
		return
	}
	if strings.TrimSpace(finalEvent.Name) == "" {
		finalEvent.Name = strings.TrimSpace(existing.Name)
	}
	if strings.TrimSpace(finalEvent.Args) == "" {
		finalEvent.Args = strings.TrimSpace(existing.Args)
	}
	mergeStartArgs(finalEvent, existing.StartArgs, existing.Args, finalEvent.Args)
	if strings.TrimSpace(finalEvent.FullArgs) == "" {
		finalEvent.FullArgs = strings.TrimSpace(existing.FullArgs)
	}
	if strings.TrimSpace(finalEvent.ToolKind) == "" {
		finalEvent.ToolKind = strings.TrimSpace(existing.ToolKind)
	}
	if !finalEvent.Terminal {
		finalEvent.Terminal = existing.Terminal
	}
}

func mergeFinalToolEvent(ev *SubagentEvent, finalEvent *SubagentEvent, authoritativeFinal bool) {
	if ev == nil || finalEvent == nil {
		return
	}
	fillMissingFinalToolEventFromExisting(finalEvent, *ev)
	ev.Name = finalEvent.Name
	ev.ToolKind = finalEvent.ToolKind
	ev.Args = finalEvent.Args
	mergeStartArgs(ev, finalEvent.StartArgs, finalEvent.Args)
	ev.FullArgs = finalEvent.FullArgs
	ev.Terminal = ev.Terminal || finalEvent.Terminal
	ev.OutputGapBefore = ev.OutputGapBefore || finalEvent.OutputGapBefore
	if finalToolOutputShouldReplace(*ev, *finalEvent, authoritativeFinal) {
		ev.Output = finalEvent.Output
		ev.OutputMessageID = finalEvent.OutputMessageID
		ev.OutputMessage = finalEvent.OutputMessage
		ev.OutputSynthetic = finalEvent.OutputSynthetic
		ev.OutputTerminal = finalEvent.OutputTerminal
	}
	ev.OutputNarrative = ev.OutputNarrative || finalEvent.OutputNarrative
	ev.Done = true
	ev.Err = finalEvent.Err
	ev.TaskHandle = preferredDisplayTaskHandle(ev.TaskHandle, finalEvent.TaskHandle)
	if ev.TaskAction == "" {
		ev.TaskAction = finalEvent.TaskAction
	}
	if ev.TaskInput == "" {
		ev.TaskInput = finalEvent.TaskInput
	}
	if ev.TaskTargetKind == "" {
		ev.TaskTargetKind = finalEvent.TaskTargetKind
	}
}

func mergeOpenFinalToolEvent(ev *SubagentEvent, finalEvent *SubagentEvent, authoritativeFinal bool) {
	if ev == nil || finalEvent == nil {
		return
	}
	fillFinalToolEventFromExisting(finalEvent, *ev)
	mergeFinalToolEvent(ev, finalEvent, authoritativeFinal)
}

func mergeStartArgs(dst *SubagentEvent, candidates ...string) {
	if dst == nil || strings.TrimSpace(dst.StartArgs) != "" {
		return
	}
	dst.StartArgs = firstTrimmed(candidates...)
}

func finalToolOutputShouldReplace(existing SubagentEvent, finalEvent SubagentEvent, authoritativeFinal bool) bool {
	semanticName := toolSemanticName(existing.Name, existing.ToolKind)
	subagentTool := strings.EqualFold(semanticName, "SPAWN") || strings.EqualFold(semanticName, "TASK")
	if authoritativeFinal && subagentTool && renderableTextHasContent(finalEvent.Output) {
		return true
	}
	if finalEvent.OutputSynthetic && renderableTextHasContent(existing.Output) {
		return false
	}
	if subagentTool && existing.OutputNarrative {
		return subagentFinalOutputShouldReplace(existing.Output, finalEvent.Output)
	}
	// A terminal close/final frame may legitimately carry no bytes. Repeated
	// canonical finals must never turn an already rendered command transcript
	// into an empty panel merely because the first final marked it Done.
	if isTerminalPanelToolEvent(existing) &&
		renderableTextHasContent(existing.Output) &&
		!renderableTextHasContent(finalEvent.Output) {
		return false
	}
	if existing.Done {
		return true
	}
	if !isTerminalPanelToolEvent(existing) {
		return true
	}
	if shouldPreserveTerminalOutputFromNonTerminalFinal(existing, finalEvent) {
		return false
	}
	return renderableTextHasContent(finalEvent.Output)
}

func toolFinalOutputAuthoritative(isErr bool, status string) bool {
	if isErr {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "cancelled", "canceled", "interrupted":
		return true
	default:
		return false
	}
}

// subagentFinalOutputShouldReplace keeps already rendered semantic child
// narrative when the parent tool result only contains a truncated final
// preview. A final value is authoritative when no live narrative was received
// or when it contains the live value as a prefix and therefore demonstrably
// completes it.
func subagentFinalOutputShouldReplace(existing string, final string) bool {
	existing = strings.TrimSpace(sanitizeRenderableText(existing))
	final = strings.TrimSpace(sanitizeRenderableText(final))
	switch {
	case final == "":
		return false
	case existing == "":
		return true
	case final == existing:
		return true
	case strings.HasPrefix(final, existing):
		return true
	default:
		return false
	}
}

func shouldPreserveTerminalOutputFromNonTerminalFinal(existing SubagentEvent, finalEvent SubagentEvent) bool {
	if finalEvent.OutputTerminal || !renderableTextHasContent(existing.Output) {
		return false
	}
	return existing.Terminal
}

func shouldSuppressAnonymousSyntheticFinalToolEvent(ev SubagentEvent) bool {
	if !ev.Done || !ev.OutputSynthetic {
		return false
	}
	return strings.TrimSpace(ev.Name) == "" &&
		strings.TrimSpace(ev.ToolKind) == "" &&
		strings.TrimSpace(ev.Args) == "" &&
		strings.TrimSpace(ev.FullArgs) == ""
}

func preferredDisplayTaskHandle(current string, candidate string) string {
	current = strings.TrimSpace(current)
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return current
	}
	if current == "" {
		return candidate
	}
	if strings.EqualFold(current, candidate) {
		return current
	}
	candidateHandle := taskHandleDisplay(candidate)
	if candidateHandle == "" {
		return current
	}
	currentHandle := taskHandleDisplay(current)
	if currentHandle == "" || strings.EqualFold(currentHandle, "self") {
		return candidate
	}
	return current
}
