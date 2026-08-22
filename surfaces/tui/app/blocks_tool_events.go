package tuiapp

import (
	"strings"
)

type ToolUpdateMeta struct {
	TaskHandle         string
	TaskAction         string
	TaskInput          string
	TaskTargetKind     string
	MessageTarget      string
	ToolKind           string
	ToolTitle          string
	FullArgs           string
	MessageID          string
	ToolStatus         string
	ToolStatusExplicit bool
	OutputNarrative    bool
	// OutputAuthoritative marks a canonical semantic final that must replace a
	// transient preview for the same physical task panel.
	OutputAuthoritative    bool
	Terminal               bool
	OutputSynthetic        bool
	OutputTerminal         bool
	OutputGapBefore        bool
	OutputCursor           int64
	OutputCursorKnown      bool
	OutputStartCursor      int64
	OutputStartCursorKnown bool
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
	toolTitle := strings.TrimSpace(update.Meta.ToolTitle)
	fullArgs := strings.TrimSpace(update.Meta.FullArgs)
	messageID := strings.TrimSpace(update.Meta.MessageID)
	taskHandle := strings.TrimSpace(update.Meta.TaskHandle)
	taskAction := strings.ToLower(strings.TrimSpace(update.Meta.TaskAction))
	taskInput := strings.TrimSpace(update.Meta.TaskInput)
	taskTargetKind := strings.ToLower(strings.TrimSpace(update.Meta.TaskTargetKind))
	messageTarget := strings.TrimSpace(update.Meta.MessageTarget)
	authoritativeFinal := update.Meta.OutputAuthoritative || toolFinalOutputAuthoritative(update.Err, update.Meta.ToolStatus)
	openIdx := openToolEventIndexForUpdate(out, update, toolIndex)
	settledIdx := settledToolEventIndexForUpdate(out, callID)
	effectiveName := name
	existingIdx := openIdx
	if existingIdx < 0 {
		existingIdx = settledIdx
	}
	if existingIdx >= 0 && effectiveName == "" {
		effectiveName = strings.TrimSpace(out[existingIdx].Name)
	}
	semanticName := effectiveName
	output := normalizeToolEventOutput(update.Output, effectiveName, update.Meta.Terminal)
	if semanticName == surfaceToolTask && taskAction == "cancel" {
		args = taskCancelArgsWithLinkedCommand(args, out, taskHandle)
	}
	defer func() {
		var moved bool
		out, moved = relocateApprovalReviewEventsAfterTool(out, callID)
		changed = changed || moved
		updateToolEventIndex(toolIndex, out, callID)
	}()
	if settledIdx >= 0 && !update.Final {
		if update.Meta.ToolStatusExplicit {
			return out, changed, false
		}
		// A status-less tool_call_update is a sparse content/state patch. Merge
		// its present fields into the settled call while leaving Done/Err intact;
		// only an explicit non-terminal status is a stale lifecycle downgrade.
		mergeOpenToolEvent(&out[settledIdx], name, toolKind, toolTitle, args, fullArgs, output, messageID, taskHandle, taskAction, taskInput, taskTargetKind, semanticName, update.Meta)
		return out, true, false
	}
	if updateLinkedTerminalEvent(out, callID, semanticName, taskHandle, output, update.Final, update.Err, update.Meta) {
		changed = true
		if semanticName == surfaceToolSpawn {
			return out, changed, false
		}
		output = ""
	}
	if !update.Final {
		if i := openIdx; i >= 0 {
			ev := &out[i]
			mergeOpenToolEvent(ev, name, toolKind, toolTitle, args, fullArgs, output, messageID, taskHandle, taskAction, taskInput, taskTargetKind, semanticName, update.Meta)
			return out, true, false
		}
		out = append(out, SubagentEvent{
			Kind:              SEToolCall,
			CallID:            callID,
			Name:              name,
			ToolKind:          toolKind,
			Title:             toolTitle,
			Args:              args,
			StartArgs:         args,
			FullArgs:          fullArgs,
			Output:            output,
			OutputMessageID:   messageID,
			OutputMessage:     output,
			OutputNarrative:   update.Meta.OutputNarrative,
			Terminal:          update.Meta.Terminal,
			OutputSynthetic:   update.Meta.OutputSynthetic,
			OutputTerminal:    update.Meta.OutputTerminal,
			OutputGapBefore:   update.Meta.OutputGapBefore,
			OutputCursor:      update.Meta.OutputCursor,
			OutputCursorKnown: update.Meta.OutputCursorKnown,
			TaskHandle:        taskHandle,
			TaskAction:        taskAction,
			TaskInput:         taskInput,
			TaskTargetKind:    taskTargetKind,
			MessageTarget:     messageTarget,
		})
		clearTaskWriteTerminalOwnership(&out[len(out)-1])
		return out, true, false
	}

	finalEvent := SubagentEvent{
		Kind:              SEToolCall,
		CallID:            callID,
		Name:              name,
		ToolKind:          toolKind,
		Title:             toolTitle,
		Args:              args,
		StartArgs:         args,
		FullArgs:          fullArgs,
		Output:            output,
		OutputMessageID:   messageID,
		OutputMessage:     output,
		OutputNarrative:   update.Meta.OutputNarrative,
		Terminal:          update.Meta.Terminal,
		OutputSynthetic:   update.Meta.OutputSynthetic,
		OutputTerminal:    update.Meta.OutputTerminal,
		OutputGapBefore:   update.Meta.OutputGapBefore,
		OutputCursor:      update.Meta.OutputCursor,
		OutputCursorKnown: update.Meta.OutputCursorKnown,
		Done:              true,
		Err:               update.Err,
		TaskHandle:        taskHandle,
		TaskAction:        taskAction,
		TaskInput:         taskInput,
		TaskTargetKind:    taskTargetKind,
		MessageTarget:     messageTarget,
	}
	clearTaskWriteTerminalOwnership(&finalEvent)
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
		if completedToolSnapshotsShareCallID(*ev, finalEvent) {
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

func settledToolEventIndexForUpdate(events []SubagentEvent, callID string) int {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return -1
	}
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.Kind == SEToolCall && strings.TrimSpace(event.CallID) == callID {
			if event.Done {
				return i
			}
			return -1
		}
	}
	return -1
}

func completedToolSnapshotsShareCallID(existing SubagentEvent, incoming SubagentEvent) bool {
	if !existing.Done || !incoming.Done || strings.TrimSpace(existing.CallID) == "" || strings.TrimSpace(existing.CallID) != strings.TrimSpace(incoming.CallID) {
		return false
	}
	return true
}

func normalizeToolEventOutput(output string, effectiveName string, terminal bool) string {
	if terminal || surfaceIsTerminalPanelTool(effectiveName) {
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

func mergeOpenToolEvent(ev *SubagentEvent, name, toolKind, toolTitle, args, fullArgs, output, messageID, taskHandle, taskAction, taskInput, taskTargetKind string, semanticName string, meta ToolUpdateMeta) {
	if ev == nil {
		return
	}
	if strings.TrimSpace(ev.Name) == "" && strings.TrimSpace(name) != "" {
		ev.Name = name
	}
	if strings.TrimSpace(toolKind) != "" {
		ev.ToolKind = toolKind
	}
	if strings.TrimSpace(toolTitle) != "" {
		ev.Title = toolTitle
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
	if ev.MessageTarget == "" {
		ev.MessageTarget = strings.TrimSpace(meta.MessageTarget)
	}
	if isTaskWriteInteractionEvent(*ev) {
		// Task write records the stdin interaction only. Command bytes stay on
		// the RunCommand panel; child narrative stays on the Spawn overlay.
		ev.Terminal = false
		return
	}
	if meta.Terminal {
		ev.Terminal = true
	}
	ev.OutputGapBefore = ev.OutputGapBefore || meta.OutputGapBefore
	// Spawn may carry a terminal relation for Task linkage, but its live output
	// is structured child narrative rather than terminal bytes and must retain
	// message scope.
	terminalOutput := isTerminalPanelToolEvent(*ev) && semanticName != surfaceToolSpawn
	if shouldMergeOpenToolOutput(semanticName, output, terminalOutput) {
		if terminalOutput {
			if meta.OutputTerminal && meta.OutputCursorKnown {
				mergeTerminalOutputByCursor(ev, output, meta)
			} else if meta.OutputTerminal {
				// Legacy ACP terminal_output without a cursor is still an exact
				// byte delta and must not use textual overlap guessing.
				ev.Output += output
			} else {
				ev.Output = mergeCommandStreamChunk(ev.Output, output)
			}
		} else {
			if meta.OutputNarrative && ev.OutputNarrativeBoundary {
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
		ev.OutputNarrative = ev.OutputNarrative || meta.OutputNarrative
		ev.OutputSynthetic = false
		if terminalOutput {
			ev.OutputTerminal = true
		}
	}
}

func clearTaskWriteTerminalOwnership(ev *SubagentEvent) {
	if ev == nil || !isTaskWriteInteractionEvent(*ev) {
		return
	}
	ev.Terminal = false
	ev.OutputTerminal = false
	ev.OutputGapBefore = false
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
	if strings.TrimSpace(finalEvent.Title) == "" {
		finalEvent.Title = strings.TrimSpace(existing.Title)
	}
	if strings.TrimSpace(finalEvent.MessageTarget) == "" {
		finalEvent.MessageTarget = strings.TrimSpace(existing.MessageTarget)
	}
	if !finalEvent.Terminal {
		finalEvent.Terminal = existing.Terminal
	}
	if !finalEvent.OutputCursorKnown && existing.OutputCursorKnown {
		finalEvent.OutputCursor = existing.OutputCursor
		finalEvent.OutputCursorKnown = true
	}
	clearTaskWriteTerminalOwnership(finalEvent)
}

func shouldUseExistingArgsForFinal(finalEvent SubagentEvent, existing SubagentEvent) bool {
	if strings.TrimSpace(finalEvent.Args) == "" {
		return true
	}
	if strings.TrimSpace(finalEvent.FullArgs) == "" && toolPanelEventHasHiddenToolArgs(existing) {
		// Preserve an existing preview/full pair unless the completion supplies
		// a new full representation for the replacement preview.
		return true
	}
	if finalEvent.Name != surfaceToolSpawn {
		return false
	}
	// A final preview accompanied by its full Spawn invocation is one
	// authoritative display pair. Comparing the folded previews by rune count
	// can otherwise prefer the start row solely because middle truncation
	// trimmed one more space, discarding a handle added by the result.
	if strings.TrimSpace(finalEvent.FullArgs) != "" {
		return false
	}
	return shouldReplaceSpawnDisplayArgs(finalEvent.Args, existing.Args)
}

func shouldUseExistingFullArgsForFinal(finalEvent SubagentEvent, existing SubagentEvent) bool {
	if strings.TrimSpace(finalEvent.FullArgs) == "" {
		return true
	}
	if finalEvent.Name != surfaceToolSpawn {
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
	if strings.TrimSpace(finalEvent.Title) == "" {
		finalEvent.Title = strings.TrimSpace(existing.Title)
	}
	if strings.TrimSpace(finalEvent.MessageTarget) == "" {
		finalEvent.MessageTarget = strings.TrimSpace(existing.MessageTarget)
	}
	if !finalEvent.Terminal {
		finalEvent.Terminal = existing.Terminal
	}
	if !finalEvent.OutputCursorKnown && existing.OutputCursorKnown {
		finalEvent.OutputCursor = existing.OutputCursor
		finalEvent.OutputCursorKnown = true
	}
	clearTaskWriteTerminalOwnership(finalEvent)
}

func mergeFinalToolEvent(ev *SubagentEvent, finalEvent *SubagentEvent, authoritativeFinal bool) {
	if ev == nil || finalEvent == nil {
		return
	}
	fillMissingFinalToolEventFromExisting(finalEvent, *ev)
	// Exact runtime identity is established once for a call. Sparse ACP patches
	// may omit it, and a later compatibility snapshot must not replace it with
	// an alias or a coarse standard kind.
	if existingName := strings.TrimSpace(ev.Name); existingName != "" {
		finalEvent.Name = existingName
	}
	ev.Name = finalEvent.Name
	ev.ToolKind = finalEvent.ToolKind
	ev.Title = finalEvent.Title
	ev.Args = finalEvent.Args
	mergeStartArgs(ev, finalEvent.StartArgs, finalEvent.Args)
	ev.FullArgs = finalEvent.FullArgs
	ev.MessageTarget = firstNonEmpty(finalEvent.MessageTarget, ev.MessageTarget)
	ev.Terminal = ev.Terminal || finalEvent.Terminal
	ev.OutputGapBefore = ev.OutputGapBefore || finalEvent.OutputGapBefore
	clearTaskWriteTerminalOwnership(ev)
	outputReplaced := false
	if finalToolOutputShouldReplace(*ev, *finalEvent, authoritativeFinal) {
		ev.Output = finalEvent.Output
		ev.OutputMessageID = finalEvent.OutputMessageID
		ev.OutputMessage = finalEvent.OutputMessage
		ev.OutputSynthetic = finalEvent.OutputSynthetic
		ev.OutputTerminal = finalEvent.OutputTerminal
		outputReplaced = true
	}
	// A contentless close frame proves lifecycle completion, not delivery of
	// bytes through its end cursor. Keep the last represented cursor so a
	// later durable Task observation can still repair the missing suffix.
	if outputReplaced && finalEvent.Output != "" && finalEvent.OutputCursorKnown &&
		(!ev.OutputCursorKnown || finalEvent.OutputCursor >= ev.OutputCursor) {
		ev.OutputCursor = finalEvent.OutputCursor
		ev.OutputCursorKnown = true
	}
	if !isTaskWriteInteractionEvent(*ev) {
		ev.OutputNarrative = ev.OutputNarrative || finalEvent.OutputNarrative
	}
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
	if isTaskWriteInteractionEvent(existing) || isTaskWriteInteractionEvent(finalEvent) {
		return finalEvent.Err && renderableTextHasContent(finalEvent.Output)
	}
	semanticName := existing.Name
	subagentTool := semanticName == surfaceToolSpawn
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
