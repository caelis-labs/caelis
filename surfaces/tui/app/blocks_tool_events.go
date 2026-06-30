package tuiapp

import "strings"

type ToolUpdateMeta struct {
	TaskID          string
	TaskAction      string
	TaskInput       string
	TaskTargetKind  string
	ToolKind        string
	FullArgs        string
	Terminal        bool
	OutputSynthetic bool
	OutputTerminal  bool
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
	taskID := strings.TrimSpace(update.Meta.TaskID)
	taskAction := strings.ToLower(strings.TrimSpace(update.Meta.TaskAction))
	taskInput := strings.TrimSpace(update.Meta.TaskInput)
	taskTargetKind := strings.ToLower(strings.TrimSpace(update.Meta.TaskTargetKind))
	effectiveName, effectiveToolKind, openIdx := effectiveToolEventIdentity(out, update, toolIndex, name, toolKind)
	semanticName := toolSemanticName(effectiveName, effectiveToolKind)
	output := normalizeToolEventOutput(update.Output, effectiveName, effectiveToolKind, update.Meta.Terminal)
	if strings.EqualFold(semanticName, "TASK") && taskAction == "cancel" {
		args = taskCancelArgsWithLinkedCommand(args, out, taskID)
	}
	defer func() {
		var moved bool
		out, moved = relocateApprovalReviewEventsAfterTool(out, callID)
		changed = changed || moved
		updateToolEventIndex(toolIndex, out, callID)
	}()
	if updateLinkedTerminalEvent(out, callID, semanticName, taskID, output, update.Final, update.Err, update.Meta) {
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
			mergeOpenToolEvent(ev, name, toolKind, args, fullArgs, output, taskID, taskAction, taskInput, taskTargetKind, semanticName, update.Meta.Terminal)
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
			Terminal:        update.Meta.Terminal,
			OutputSynthetic: update.Meta.OutputSynthetic,
			OutputTerminal:  update.Meta.OutputTerminal,
			TaskID:          taskID,
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
		Terminal:        update.Meta.Terminal,
		OutputSynthetic: update.Meta.OutputSynthetic,
		OutputTerminal:  update.Meta.OutputTerminal,
		Done:            true,
		Err:             update.Err,
		TaskID:          taskID,
		TaskAction:      taskAction,
		TaskInput:       taskInput,
		TaskTargetKind:  taskTargetKind,
	}
	if i := openToolEventIndexForUpdate(out, update, toolIndex); i >= 0 {
		ev := &out[i]
		if !ev.Done {
			mergeOpenFinalToolEvent(ev, &finalEvent)
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
		if shouldReplaceCompletedTerminalToolEvent(*ev, finalEvent) {
			mergeFinalToolEvent(ev, &finalEvent)
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
	if terminal || isTerminalPanelToolKind(effectiveName, effectiveToolKind) {
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

func mergeOpenToolEvent(ev *SubagentEvent, name, toolKind, args, fullArgs, output, taskID, taskAction, taskInput, taskTargetKind string, semanticName string, terminal bool) {
	if ev == nil {
		return
	}
	if strings.TrimSpace(ev.Name) == "" {
		ev.Name = name
	}
	if strings.TrimSpace(ev.ToolKind) == "" {
		ev.ToolKind = toolKind
	}
	preferredTaskID := preferredDisplayTaskID(ev.TaskID, taskID)
	if strings.TrimSpace(ev.Args) == "" {
		ev.Args = args
	} else if strings.EqualFold(semanticName, "SPAWN") && shouldReplaceSpawnDisplayArgs(ev.Args, args) {
		ev.Args = args
	} else if strings.EqualFold(semanticName, "TASK") && preferredTaskID != strings.TrimSpace(ev.TaskID) && strings.TrimSpace(args) != "" {
		ev.Args = args
	}
	mergeStartArgs(ev, args, ev.Args)
	if strings.TrimSpace(ev.FullArgs) == "" {
		ev.FullArgs = fullArgs
	} else if strings.EqualFold(semanticName, "SPAWN") && shouldReplaceSpawnDisplayArgs(ev.FullArgs, fullArgs) {
		ev.FullArgs = fullArgs
	} else if strings.EqualFold(semanticName, "TASK") && preferredTaskID != strings.TrimSpace(ev.TaskID) && strings.TrimSpace(fullArgs) != "" {
		ev.FullArgs = fullArgs
	}
	ev.TaskID = preferredTaskID
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
	if shouldMergeOpenToolOutput(semanticName, output, isTerminalPanelToolEvent(*ev)) {
		if isTerminalPanelToolEvent(*ev) {
			ev.Output = mergeCommandStreamChunk(ev.Output, output)
		} else {
			ev.Output = mergeSubagentStreamChunk(ev.Output, output)
		}
		ev.OutputSynthetic = false
		if isTerminalPanelToolEvent(*ev) {
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

func mergeFinalToolEvent(ev *SubagentEvent, finalEvent *SubagentEvent) {
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
	if finalToolOutputShouldReplace(*ev, *finalEvent) {
		ev.Output = finalEvent.Output
		ev.OutputSynthetic = finalEvent.OutputSynthetic
		ev.OutputTerminal = finalEvent.OutputTerminal
	}
	ev.Done = true
	ev.Err = finalEvent.Err
	ev.TaskID = preferredDisplayTaskID(ev.TaskID, finalEvent.TaskID)
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

func mergeOpenFinalToolEvent(ev *SubagentEvent, finalEvent *SubagentEvent) {
	if ev == nil || finalEvent == nil {
		return
	}
	fillFinalToolEventFromExisting(finalEvent, *ev)
	mergeFinalToolEvent(ev, finalEvent)
}

func mergeStartArgs(dst *SubagentEvent, candidates ...string) {
	if dst == nil || strings.TrimSpace(dst.StartArgs) != "" {
		return
	}
	dst.StartArgs = firstTrimmed(candidates...)
}

func finalToolOutputShouldReplace(existing SubagentEvent, finalEvent SubagentEvent) bool {
	if finalEvent.OutputSynthetic && renderableTextHasContent(existing.Output) {
		return false
	}
	if !isTerminalPanelToolEvent(existing) {
		return true
	}
	if shouldPreserveTerminalOutputFromNonTerminalFinal(existing, finalEvent) {
		return false
	}
	return renderableTextHasContent(finalEvent.Output)
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

func preferredDisplayTaskID(current string, candidate string) string {
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
