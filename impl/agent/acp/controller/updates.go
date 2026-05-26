package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/controller"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/client"
)

type turnHandle struct {
	cancelFn  context.CancelFunc
	eventsCh  chan turnHandleEvent
	mu        sync.Mutex
	cancelled bool
	closed    bool
}

type turnHandleEvent struct {
	event *session.Event
	err   error
}

func newTurnHandle(cancel context.CancelFunc) *turnHandle {
	return &turnHandle{
		cancelFn: cancel,
		eventsCh: make(chan turnHandleEvent, 64),
	}
}

func (h *turnHandle) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		for item := range h.eventsCh {
			if !yield(session.CloneEvent(item.event), item.err) {
				return
			}
		}
	}
}

func (h *turnHandle) Cancel() controller.CancelResult {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancelled {
		return controller.CancelResult{Status: controller.CancelStatusAlreadyCancelled}
	}
	h.cancelled = true
	if h.cancelFn != nil {
		h.cancelFn()
	}
	return controller.CancelResult{Status: controller.CancelStatusCancelled}
}

func (h *turnHandle) Close() error { return nil }

func (h *turnHandle) publishEvent(event *session.Event) {
	if h == nil || event == nil {
		return
	}
	h.publish(turnHandleEvent{event: session.CloneEvent(event)})
}

func (h *turnHandle) publishError(err error) {
	if h == nil || err == nil {
		return
	}
	h.publish(turnHandleEvent{err: err})
}

func (h *turnHandle) publish(item turnHandleEvent) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	select {
	case h.eventsCh <- item:
	default:
	}
}

func (h *turnHandle) finish() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	close(h.eventsCh)
}

func normalizeACPUpdateEvent(
	clock func() time.Time,
	binding session.ControllerBinding,
	remoteSessionID string,
	turnID string,
	update client.Update,
) *session.Event {
	controller := session.ControllerRef{
		Kind:    session.ControllerKindACP,
		ID:      strings.TrimSpace(binding.ControllerID),
		EpochID: strings.TrimSpace(binding.EpochID),
	}
	scope := &session.EventScope{
		TurnID:     strings.TrimSpace(turnID),
		Source:     "acp",
		Controller: controller,
		ACP: session.ACPRef{
			SessionID: strings.TrimSpace(remoteSessionID),
		},
	}
	now := time.Now
	if clock != nil {
		now = clock
	}
	switch typed := update.(type) {
	case client.ContentChunk:
		text := contentChunkText(typed)
		if text == "" {
			return nil
		}
		event := &session.Event{
			Visibility: acpContentChunkVisibility(typed.SessionUpdate),
			Time:       now(),
			Actor:      session.ActorRef{Kind: session.ActorKindController, Name: strings.TrimSpace(binding.Label)},
			Scope:      scope,
			Text:       text,
			Message:    ptrMessage(messageForContentChunk(typed, text)),
			Protocol: &session.EventProtocol{
				UpdateType: typed.SessionUpdate,
				Update: &session.ProtocolUpdate{
					SessionUpdate: strings.TrimSpace(typed.SessionUpdate),
					Content:       typed.Content,
				},
			},
		}
		switch strings.TrimSpace(typed.SessionUpdate) {
		case client.UpdateUserMessage:
			event.Type = session.EventTypeUser
			event.Actor = session.ActorRef{Kind: session.ActorKindUser, Name: "user"}
		default:
			event.Type = session.EventTypeAssistant
		}
		scope.ACP.EventType = strings.TrimSpace(typed.SessionUpdate)
		return event
	case client.ToolCall:
		scope.ACP.EventType = strings.TrimSpace(typed.SessionUpdate)
		targetTool := &session.ProtocolToolCall{
			ID:       strings.TrimSpace(typed.ToolCallID),
			Name:     acpToolDisplayName(typed.Kind, typed.Title),
			Kind:     strings.TrimSpace(typed.Kind),
			Title:    strings.TrimSpace(typed.Title),
			Status:   strings.TrimSpace(typed.Status),
			RawInput: acpToolRawInput(typed.Kind, typed.Title, typed.RawInput),
			Content:  acpToolContent(typed.Content),
		}
		return &session.Event{
			Type:       session.EventTypeToolCall,
			Visibility: session.VisibilityUIOnly,
			Time:       now(),
			Actor:      session.ActorRef{Kind: session.ActorKindController, Name: strings.TrimSpace(binding.Label)},
			Scope:      scope,
			Text:       firstNonEmpty(strings.TrimSpace(typed.Title), strings.TrimSpace(typed.Kind), "tool call"),
			Protocol: &session.EventProtocol{
				UpdateType: typed.SessionUpdate,
				ToolCall:   targetTool,
				Update:     acpToolProtocolUpdate(typed.SessionUpdate, targetTool, typed.Meta),
			},
		}
	case client.ToolCallUpdate:
		scope.ACP.EventType = strings.TrimSpace(typed.SessionUpdate)
		targetTool := &session.ProtocolToolCall{
			ID:        strings.TrimSpace(typed.ToolCallID),
			Name:      acpToolDisplayName(derefString(typed.Kind), derefString(typed.Title)),
			Kind:      strings.TrimSpace(derefString(typed.Kind)),
			Title:     strings.TrimSpace(derefString(typed.Title)),
			Status:    strings.TrimSpace(derefString(typed.Status)),
			RawInput:  acpToolRawInput(derefString(typed.Kind), derefString(typed.Title), typed.RawInput),
			RawOutput: acpToolRawOutput(typed.RawOutput),
			Content:   acpToolContent(typed.Content),
		}
		return &session.Event{
			Type:       toolEventTypeFromStatus(derefString(typed.Status)),
			Visibility: session.VisibilityUIOnly,
			Time:       now(),
			Actor:      session.ActorRef{Kind: session.ActorKindController, Name: strings.TrimSpace(binding.Label)},
			Scope:      scope,
			Text:       firstNonEmpty(strings.TrimSpace(derefString(typed.Title)), strings.TrimSpace(derefString(typed.Kind)), "tool update"),
			Protocol: &session.EventProtocol{
				UpdateType: typed.SessionUpdate,
				ToolCall:   targetTool,
				Update:     acpToolProtocolUpdate(typed.SessionUpdate, targetTool, typed.Meta),
			},
		}
	case client.PlanUpdate:
		scope.ACP.EventType = strings.TrimSpace(typed.SessionUpdate)
		return &session.Event{
			Type:       session.EventTypePlan,
			Visibility: session.VisibilityUIOnly,
			Time:       now(),
			Actor:      session.ActorRef{Kind: session.ActorKindController, Name: strings.TrimSpace(binding.Label)},
			Scope:      scope,
			Text:       "plan updated",
			Protocol: &session.EventProtocol{
				UpdateType: typed.SessionUpdate,
				Plan:       &session.ProtocolPlan{Entries: planEntries(typed.Entries)},
				Update: &session.ProtocolUpdate{
					SessionUpdate: strings.TrimSpace(typed.SessionUpdate),
					Entries:       planEntries(typed.Entries),
				},
			},
		}
	}
	return nil
}

func acpContentChunkVisibility(updateType string) session.Visibility {
	switch strings.TrimSpace(updateType) {
	case client.UpdateUserMessage:
		return session.VisibilityCanonical
	default:
		return session.VisibilityUIOnly
	}
}

func contentChunkText(chunk client.ContentChunk) string {
	var text client.TextChunk
	if err := json.Unmarshal(chunk.Content, &text); err == nil {
		if text.Text != "" {
			return text.Text
		}
		return textFromRawContent(chunk.Content)
	}
	return textFromRawContent(chunk.Content)
}

func textFromRawContent(raw json.RawMessage) string {
	var content any
	if err := json.Unmarshal(raw, &content); err != nil {
		return strings.TrimSpace(string(raw))
	}
	return textFromContentValue(content)
}

func textFromContentValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		var out strings.Builder
		for _, item := range typed {
			out.WriteString(textFromContentValue(item))
		}
		return out.String()
	case map[string]any:
		for _, key := range []string{"text", "content", "detailedContent"} {
			if nested, ok := typed[key]; ok {
				if text := textFromContentValue(nested); text != "" {
					return text
				}
			}
		}
	}
	return ""
}

func controllerCommandsFromACP(in []map[string]any) []controller.ControllerCommand {
	if len(in) == 0 {
		return nil
	}
	out := make([]controller.ControllerCommand, 0, len(in))
	seen := map[string]struct{}{}
	for _, item := range in {
		name := normalizeACPCommandName(firstNonEmpty(
			stringMapValue(item, "name"),
			stringMapValue(item, "command"),
			stringMapValue(item, "id"),
			stringMapValue(item, "title"),
		))
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, exists := seen[key]; exists {
			continue
		}
		out = append(out, controller.ControllerCommand{
			Name:        name,
			Description: firstNonEmpty(stringMapValue(item, "description"), stringMapValue(item, "detail")),
		})
		seen[key] = struct{}{}
	}
	return out
}

func normalizeACPCommandName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "/")
	if fields := strings.Fields(name); len(fields) > 0 {
		name = fields[0]
	}
	return strings.ToLower(strings.TrimSpace(name))
}

func stringMapValue(item map[string]any, key string) string {
	if len(item) == 0 {
		return ""
	}
	raw, ok := item[key]
	if !ok || raw == nil {
		return ""
	}
	switch typed := raw.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func controllerConfigOptionsFromACP(in []client.SessionConfigOption) []controller.ControllerConfigOption {
	if len(in) == 0 {
		return nil
	}
	out := make([]controller.ControllerConfigOption, 0, len(in))
	for _, item := range in {
		option := controller.ControllerConfigOption{
			ID:           strings.TrimSpace(item.ID),
			Name:         strings.TrimSpace(item.Name),
			Type:         strings.TrimSpace(item.Type),
			Category:     strings.TrimSpace(item.Category),
			Description:  strings.TrimSpace(item.Description),
			CurrentValue: stringFromACPValue(item.CurrentValue),
			Options:      make([]controller.ControllerConfigChoice, 0, len(item.Options)),
		}
		for _, choice := range item.Options {
			value := strings.TrimSpace(choice.Value)
			if value == "" {
				continue
			}
			option.Options = append(option.Options, controller.ControllerConfigChoice{
				Value:       value,
				Name:        strings.TrimSpace(choice.Name),
				Description: strings.TrimSpace(choice.Description),
			})
		}
		out = append(out, option)
	}
	return out
}

func stringFromACPValue(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func currentModeID(modes *client.SessionModeState) string {
	if modes == nil {
		return ""
	}
	return strings.TrimSpace(modes.CurrentModeID)
}

func splitACPCurrentModelEffort(models *client.SessionModelState) (string, string, bool) {
	if models == nil {
		return "", "", false
	}
	testModel, effort, hasEffort := splitACPModelIDEffort(models.CurrentModelID)
	if hasEffort {
		return testModel, effort, true
	}
	modelID := strings.TrimSpace(models.CurrentModelID)
	return modelID, "", modelID != ""
}

func splitACPModelIDEffort(modelID string) (string, string, bool) {
	modelID = strings.TrimSpace(modelID)
	idx := strings.LastIndex(modelID, "/")
	if idx <= 0 || idx == len(modelID)-1 {
		return modelID, "", false
	}
	effort := strings.ToLower(strings.TrimSpace(modelID[idx+1:]))
	if !isReasoningEffortValue(effort) {
		return modelID, "", false
	}
	return strings.TrimSpace(modelID[:idx]), effort, true
}

func isReasoningEffortValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func controllerEffortChoicesFromModels(models *client.SessionModelState, model string) []controller.ControllerConfigChoice {
	return controllerEffortChoicesFromMap(controllerEffortChoicesByModelFromModels(models), model)
}

func controllerEffortChoicesByModelFromModels(models *client.SessionModelState) map[string][]controller.ControllerConfigChoice {
	if models == nil || len(models.AvailableModels) == 0 {
		return nil
	}
	out := map[string][]controller.ControllerConfigChoice{}
	seen := map[string]map[string]struct{}{}
	for _, item := range models.AvailableModels {
		base, effort, hasEffort := splitACPModelIDEffort(item.ModelID)
		base = strings.TrimSpace(base)
		if !hasEffort || base == "" {
			continue
		}
		modelKey := strings.ToLower(base)
		key := strings.ToLower(strings.TrimSpace(effort))
		if key == "" {
			continue
		}
		if seen[modelKey] == nil {
			seen[modelKey] = map[string]struct{}{}
		}
		if _, exists := seen[modelKey][key]; exists {
			continue
		}
		seen[modelKey][key] = struct{}{}
		out[modelKey] = append(out[modelKey], controller.ControllerConfigChoice{
			Value:       key,
			Name:        reasoningEffortDisplayName(key),
			Description: strings.TrimSpace(item.Description),
		})
	}
	return out
}

func controllerEffortChoicesFromMap(options map[string][]controller.ControllerConfigChoice, model string) []controller.ControllerConfigChoice {
	if len(options) == 0 {
		return nil
	}
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return nil
	}
	return cloneControllerConfigChoices(options[model])
}

func matchACPModelIDForEffort(models *client.SessionModelState, model string, effort string) (string, bool) {
	if models == nil {
		return "", false
	}
	model = strings.TrimSpace(model)
	effort = strings.ToLower(strings.TrimSpace(effort))
	if model == "" {
		model, _, _ = splitACPCurrentModelEffort(models)
	}
	if model == "" || effort == "" {
		return "", false
	}
	if base, existingEffort, hasEffort := splitACPModelIDEffort(model); hasEffort {
		return model, strings.EqualFold(existingEffort, effort) && base != ""
	}
	for _, item := range models.AvailableModels {
		base, itemEffort, hasEffort := splitACPModelIDEffort(item.ModelID)
		if !hasEffort {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(base), model) && strings.EqualFold(itemEffort, effort) {
			return strings.TrimSpace(item.ModelID), true
		}
	}
	return "", false
}

func withACPCurrentModelID(models *client.SessionModelState, modelID string) *client.SessionModelState {
	out := cloneACPSessionModelState(models)
	if out == nil {
		out = &client.SessionModelState{}
	}
	out.CurrentModelID = strings.TrimSpace(modelID)
	return out
}

func reasoningEffortDisplayName(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "xhigh":
		return "Xhigh"
	case "minimal":
		return "Minimal"
	case "low":
		return "Low"
	case "medium":
		return "Medium"
	case "high":
		return "High"
	case "max":
		return "Max"
	case "none":
		return "None"
	default:
		return strings.TrimSpace(effort)
	}
}

func controllerModesFromACP(modes *client.SessionModeState) []controller.ControllerMode {
	if modes == nil || len(modes.AvailableModes) == 0 {
		return nil
	}
	out := make([]controller.ControllerMode, 0, len(modes.AvailableModes))
	for _, mode := range modes.AvailableModes {
		id := strings.TrimSpace(mode.ID)
		if id == "" {
			continue
		}
		out = append(out, controller.ControllerMode{
			ID:          id,
			Name:        strings.TrimSpace(mode.Name),
			Description: strings.TrimSpace(mode.Description),
		})
	}
	return out
}

func controllerModesFromConfigOptions(options []controller.ControllerConfigOption) []controller.ControllerMode {
	option, ok := pickModeConfigOption(options)
	if !ok || option == nil || len(option.Options) == 0 {
		return nil
	}
	out := make([]controller.ControllerMode, 0, len(option.Options))
	for _, choice := range option.Options {
		id := strings.TrimSpace(choice.Value)
		if id == "" {
			continue
		}
		out = append(out, controller.ControllerMode{
			ID:          id,
			Name:        strings.TrimSpace(choice.Name),
			Description: strings.TrimSpace(choice.Description),
		})
	}
	return out
}

func pickModelConfigOption(options []controller.ControllerConfigOption) (*controller.ControllerConfigOption, bool) {
	return pickControllerConfigOption(options, matchModelConfigOption)
}

func pickModeConfigOption(options []controller.ControllerConfigOption) (*controller.ControllerConfigOption, bool) {
	return pickControllerConfigOption(options, matchModeConfigOption)
}

func pickEffortConfigOption(options []controller.ControllerConfigOption) (*controller.ControllerConfigOption, bool) {
	return pickControllerConfigOption(options, func(option controller.ControllerConfigOption) (bool, int) {
		id := strings.ToLower(strings.TrimSpace(option.ID))
		category := strings.ToLower(strings.TrimSpace(option.Category))
		haystack := controllerConfigOptionHaystack(option)
		switch id {
		case "effort", "reasoning", "reasoning_effort", "reasoningeffort", "thought", "thought_level", "thoughtlevel", "thinking", "thinking_level", "thinkinglevel":
			return true, 0
		}
		switch category {
		case "thought_level", "reasoning", "reasoning_effort":
			return true, 0
		}
		if strings.Contains(haystack, "effort") || strings.Contains(haystack, "reasoning") {
			return true, 1
		}
		if strings.Contains(haystack, "thought") || strings.Contains(haystack, "thinking") {
			return true, 2
		}
		return false, 0
	})
}

func matchModeConfigOption(option controller.ControllerConfigOption) (bool, int) {
	id := strings.ToLower(strings.TrimSpace(option.ID))
	category := strings.ToLower(strings.TrimSpace(option.Category))
	haystack := controllerConfigOptionHaystack(option)
	switch id {
	case "mode", "session_mode", "sessionmode":
		return true, 0
	}
	if category == "mode" {
		return true, 0
	}
	if strings.Contains(haystack, "mode") && !strings.Contains(haystack, "model") {
		return true, 1
	}
	return false, 0
}

func matchModelConfigOption(option controller.ControllerConfigOption) (bool, int) {
	id := strings.ToLower(strings.TrimSpace(option.ID))
	category := strings.ToLower(strings.TrimSpace(option.Category))
	haystack := controllerConfigOptionHaystack(option)
	if id == "model" || id == "models" || id == "model_id" || id == "modelid" {
		return true, 0
	}
	if strings.Contains(haystack, "reason") ||
		strings.Contains(haystack, "effort") ||
		strings.Contains(haystack, "thought") ||
		strings.Contains(haystack, "thinking") {
		return false, 0
	}
	if category == "model" {
		return true, 1
	}
	if strings.Contains(haystack, "model") {
		return true, 2
	}
	return false, 0
}

func currentModelFromConfigOptions(options []controller.ControllerConfigOption) string {
	if option, ok := pickModelConfigOption(options); ok && option != nil {
		return strings.TrimSpace(option.CurrentValue)
	}
	return ""
}

func currentModeFromConfigOptions(options []controller.ControllerConfigOption) string {
	if option, ok := pickModeConfigOption(options); ok && option != nil {
		return strings.TrimSpace(option.CurrentValue)
	}
	return ""
}

func setControllerConfigCurrentValue(options []controller.ControllerConfigOption, model string) []controller.ControllerConfigOption {
	model = strings.TrimSpace(model)
	if model == "" {
		return cloneControllerConfigOptions(options)
	}
	out := cloneControllerConfigOptions(options)
	bestIndex := -1
	bestScore := 1000
	for i := range out {
		ok, score := matchModelConfigOption(out[i])
		if !ok {
			continue
		}
		if bestIndex < 0 || score < bestScore {
			bestIndex = i
			bestScore = score
		}
	}
	if bestIndex >= 0 {
		out[bestIndex].CurrentValue = model
		return out
	}
	return append(out, controller.ControllerConfigOption{
		ID:           "model",
		Name:         "Model",
		Type:         "select",
		Category:     "model",
		CurrentValue: model,
	})
}

func pickControllerConfigOption(
	options []controller.ControllerConfigOption,
	match func(controller.ControllerConfigOption) (bool, int),
) (*controller.ControllerConfigOption, bool) {
	var picked *controller.ControllerConfigOption
	bestScore := 1000
	for i := range options {
		ok, score := match(options[i])
		if !ok {
			continue
		}
		if picked == nil || score < bestScore {
			picked = &options[i]
			bestScore = score
		}
	}
	return picked, picked != nil
}

func controllerConfigOptionHaystack(option controller.ControllerConfigOption) string {
	return strings.ToLower(strings.Join([]string{
		strings.TrimSpace(option.ID),
		strings.TrimSpace(option.Name),
		strings.TrimSpace(option.Category),
		strings.TrimSpace(option.Description),
	}, " "))
}

func matchControllerConfigChoice(options []controller.ControllerConfigChoice, requested string) (controller.ControllerConfigChoice, bool) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return controller.ControllerConfigChoice{}, false
	}
	for _, option := range options {
		if strings.EqualFold(strings.TrimSpace(option.Value), requested) || strings.EqualFold(strings.TrimSpace(option.Name), requested) {
			if strings.TrimSpace(option.Value) == "" {
				continue
			}
			return option, true
		}
	}
	return controller.ControllerConfigChoice{}, false
}

func mergeControllerConfigChoices(primary []controller.ControllerConfigChoice, fallback []controller.ControllerConfigChoice) []controller.ControllerConfigChoice {
	if len(primary) == 0 {
		return cloneControllerConfigChoices(fallback)
	}
	out := cloneControllerConfigChoices(primary)
	seen := map[string]struct{}{}
	for _, item := range out {
		if value := strings.ToLower(strings.TrimSpace(item.Value)); value != "" {
			seen[value] = struct{}{}
		}
	}
	for _, item := range fallback {
		value := strings.TrimSpace(item.Value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		out = append(out, item)
		seen[key] = struct{}{}
	}
	return out
}

func matchControllerMode(options []controller.ControllerMode, requested string) (controller.ControllerMode, bool) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return controller.ControllerMode{}, false
	}
	for _, option := range options {
		id := strings.TrimSpace(option.ID)
		if id == "" {
			continue
		}
		if strings.EqualFold(id, requested) || strings.EqualFold(strings.TrimSpace(option.Name), requested) {
			return option, true
		}
	}
	return controller.ControllerMode{}, false
}

func mergeControllerConfigOptions(existing []controller.ControllerConfigOption, updates []controller.ControllerConfigOption) []controller.ControllerConfigOption {
	if len(updates) == 0 {
		return cloneControllerConfigOptions(existing)
	}
	if len(existing) == 0 {
		return cloneControllerConfigOptions(updates)
	}
	out := cloneControllerConfigOptions(existing)
	indexByID := map[string]int{}
	for i, item := range out {
		if id := strings.ToLower(strings.TrimSpace(item.ID)); id != "" {
			indexByID[id] = i
		}
	}
	for _, item := range updates {
		id := strings.ToLower(strings.TrimSpace(item.ID))
		if id != "" {
			if idx, exists := indexByID[id]; exists {
				out[idx] = mergeControllerConfigOption(out[idx], item)
				continue
			}
			indexByID[id] = len(out)
		}
		out = append(out, cloneControllerConfigOption(item))
	}
	return out
}

func mergeControllerConfigOption(existing controller.ControllerConfigOption, update controller.ControllerConfigOption) controller.ControllerConfigOption {
	out := cloneControllerConfigOption(existing)
	if value := strings.TrimSpace(update.ID); value != "" {
		out.ID = value
	}
	if value := strings.TrimSpace(update.Name); value != "" {
		out.Name = value
	}
	if value := strings.TrimSpace(update.Type); value != "" {
		out.Type = value
	}
	if value := strings.TrimSpace(update.Category); value != "" {
		out.Category = value
	}
	if value := strings.TrimSpace(update.Description); value != "" {
		out.Description = value
	}
	out.CurrentValue = strings.TrimSpace(update.CurrentValue)
	out.Options = mergeControllerConfigChoices(existing.Options, update.Options)
	return out
}

func fillControllerConfigOptions(existing []controller.ControllerConfigOption, fallback []controller.ControllerConfigOption) []controller.ControllerConfigOption {
	if len(existing) == 0 {
		return cloneControllerConfigOptions(fallback)
	}
	if len(fallback) == 0 {
		return cloneControllerConfigOptions(existing)
	}
	out := cloneControllerConfigOptions(existing)
	indexByID := map[string]int{}
	for i, item := range out {
		if id := strings.ToLower(strings.TrimSpace(item.ID)); id != "" {
			indexByID[id] = i
		}
	}
	for _, item := range fallback {
		id := strings.ToLower(strings.TrimSpace(item.ID))
		if id != "" {
			if idx, exists := indexByID[id]; exists {
				out[idx] = fillControllerConfigOption(out[idx], item)
				continue
			}
			indexByID[id] = len(out)
		}
		out = append(out, cloneControllerConfigOption(item))
	}
	return out
}

func fillControllerConfigOption(existing controller.ControllerConfigOption, fallback controller.ControllerConfigOption) controller.ControllerConfigOption {
	out := cloneControllerConfigOption(existing)
	if strings.TrimSpace(out.ID) == "" {
		out.ID = strings.TrimSpace(fallback.ID)
	}
	if strings.TrimSpace(out.Name) == "" {
		out.Name = strings.TrimSpace(fallback.Name)
	}
	if strings.TrimSpace(out.Type) == "" {
		out.Type = strings.TrimSpace(fallback.Type)
	}
	if strings.TrimSpace(out.Category) == "" {
		out.Category = strings.TrimSpace(fallback.Category)
	}
	if strings.TrimSpace(out.Description) == "" {
		out.Description = strings.TrimSpace(fallback.Description)
	}
	if strings.TrimSpace(out.CurrentValue) == "" {
		out.CurrentValue = strings.TrimSpace(fallback.CurrentValue)
	}
	out.Options = mergeControllerConfigChoices(existing.Options, fallback.Options)
	return out
}

func cloneControllerCommands(in []controller.ControllerCommand) []controller.ControllerCommand {
	if len(in) == 0 {
		return nil
	}
	return append([]controller.ControllerCommand(nil), in...)
}

func mergeControllerCommands(existing []controller.ControllerCommand, fallback []controller.ControllerCommand) []controller.ControllerCommand {
	if len(existing) == 0 {
		return cloneControllerCommands(fallback)
	}
	if len(fallback) == 0 {
		return cloneControllerCommands(existing)
	}
	out := cloneControllerCommands(existing)
	seen := map[string]struct{}{}
	for _, command := range out {
		if name := normalizeACPCommandName(command.Name); name != "" {
			seen[name] = struct{}{}
		}
	}
	for _, command := range fallback {
		name := normalizeACPCommandName(command.Name)
		if name != "" {
			if _, exists := seen[name]; exists {
				continue
			}
			seen[name] = struct{}{}
		}
		out = append(out, command)
	}
	return out
}

func cloneControllerConfigOptions(in []controller.ControllerConfigOption) []controller.ControllerConfigOption {
	if len(in) == 0 {
		return nil
	}
	out := make([]controller.ControllerConfigOption, 0, len(in))
	for _, item := range in {
		out = append(out, cloneControllerConfigOption(item))
	}
	return out
}

func cloneControllerConfigOption(in controller.ControllerConfigOption) controller.ControllerConfigOption {
	out := in
	out.Options = cloneControllerConfigChoices(in.Options)
	return out
}

func cloneControllerConfigChoices(in []controller.ControllerConfigChoice) []controller.ControllerConfigChoice {
	if len(in) == 0 {
		return nil
	}
	return append([]controller.ControllerConfigChoice(nil), in...)
}

func cloneControllerModes(in []controller.ControllerMode) []controller.ControllerMode {
	if len(in) == 0 {
		return nil
	}
	return append([]controller.ControllerMode(nil), in...)
}

func mergeControllerModes(existing []controller.ControllerMode, fallback []controller.ControllerMode) []controller.ControllerMode {
	if len(existing) == 0 {
		return cloneControllerModes(fallback)
	}
	if len(fallback) == 0 {
		return cloneControllerModes(existing)
	}
	out := cloneControllerModes(existing)
	seen := map[string]struct{}{}
	for _, mode := range out {
		if id := strings.ToLower(strings.TrimSpace(mode.ID)); id != "" {
			seen[id] = struct{}{}
		}
	}
	for _, mode := range fallback {
		id := strings.ToLower(strings.TrimSpace(mode.ID))
		if id != "" {
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
		}
		out = append(out, mode)
	}
	return out
}

func cloneACPSessionModelState(in *client.SessionModelState) *client.SessionModelState {
	if in == nil {
		return nil
	}
	out := &client.SessionModelState{
		CurrentModelID:  strings.TrimSpace(in.CurrentModelID),
		AvailableModels: make([]client.ModelInfo, 0, len(in.AvailableModels)),
	}
	for _, item := range in.AvailableModels {
		modelID := strings.TrimSpace(item.ModelID)
		if modelID == "" {
			continue
		}
		out.AvailableModels = append(out.AvailableModels, client.ModelInfo{
			ModelID:     modelID,
			Name:        strings.TrimSpace(item.Name),
			Description: strings.TrimSpace(item.Description),
		})
	}
	if out.CurrentModelID == "" && len(out.AvailableModels) == 0 {
		return nil
	}
	return out
}

func acpToolDisplayName(kind string, title string) string {
	if kind = strings.TrimSpace(kind); kind != "" {
		return kind
	}
	return strings.TrimSpace(title)
}

func acpToolRawInput(kind string, title string, raw any) map[string]any {
	out := acpRawMap(raw)
	if len(out) == 0 {
		return nil
	}
	return out
}

func acpToolRawOutput(raw any) map[string]any {
	out := acpRawMap(raw)
	if len(out) == 0 {
		return nil
	}
	return out
}

func acpToolProtocolUpdate(updateType string, tool *session.ProtocolToolCall, meta map[string]any) *session.ProtocolUpdate {
	if tool == nil {
		return &session.ProtocolUpdate{SessionUpdate: strings.TrimSpace(updateType)}
	}
	update := &session.ProtocolUpdate{
		SessionUpdate: strings.TrimSpace(updateType),
		ToolCallID:    strings.TrimSpace(tool.ID),
		Kind:          strings.TrimSpace(tool.Kind),
		Title:         strings.TrimSpace(tool.Title),
		Status:        strings.TrimSpace(tool.Status),
		RawInput:      maps.Clone(tool.RawInput),
		RawOutput:     maps.Clone(tool.RawOutput),
		Meta:          maps.Clone(meta),
	}
	if len(tool.Content) > 0 {
		update.Content = session.CloneProtocolToolCallContent(tool.Content)
	}
	return update
}

func acpToolContent(content []client.ToolCallContent) []session.ProtocolToolCallContent {
	if len(content) == 0 {
		return nil
	}
	out := make([]session.ProtocolToolCallContent, 0, len(content))
	for _, item := range content {
		var oldText *string
		if item.OldText != nil {
			value := *item.OldText
			oldText = &value
		}
		out = append(out, session.ProtocolToolCallContent{
			Type:       strings.TrimSpace(item.Type),
			Content:    item.Content,
			TerminalID: strings.TrimSpace(item.TerminalID),
			Path:       strings.TrimSpace(item.Path),
			OldText:    oldText,
			NewText:    item.NewText,
		})
	}
	return session.CloneProtocolToolCallContent(out)
}

func acpRawMap(raw any) map[string]any {
	switch typed := raw.(type) {
	case nil:
		return nil
	case map[string]any:
		return maps.Clone(typed)
	default:
		if text := strings.TrimSpace(textFromContentValue(typed)); text != "" {
			return map[string]any{"text": text}
		}
		if text := strings.TrimSpace(fmt.Sprint(typed)); text != "" && text != "<nil>" {
			return map[string]any{"text": text}
		}
		return nil
	}
}

func messageForContentChunk(chunk client.ContentChunk, text string) model.Message {
	role := model.RoleAssistant
	if strings.TrimSpace(chunk.SessionUpdate) == client.UpdateUserMessage {
		role = model.RoleUser
	}
	if strings.TrimSpace(chunk.SessionUpdate) == client.UpdateAgentThought {
		return model.NewReasoningMessage(role, text, model.ReasoningVisibilityVisible)
	}
	return model.NewTextMessage(role, text)
}

func planEntries(in []client.PlanEntry) []session.ProtocolPlanEntry {
	out := make([]session.ProtocolPlanEntry, 0, len(in))
	for _, item := range in {
		out = append(out, session.ProtocolPlanEntry{
			Content:  strings.TrimSpace(item.Content),
			Status:   strings.TrimSpace(item.Status),
			Priority: "",
		})
	}
	return out
}

func toolEventTypeFromStatus(status string) session.EventType {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "cancelled":
		return session.EventTypeToolResult
	default:
		return session.EventTypeToolCall
	}
}

func buildPromptParts(input string, parts []model.ContentPart) []json.RawMessage {
	if len(parts) == 0 {
		input = strings.TrimSpace(input)
		if input == "" {
			return nil
		}
		raw, _ := json.Marshal(client.TextContent{
			Type: "text",
			Text: input,
		})
		return []json.RawMessage{raw}
	}
	out := make([]json.RawMessage, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case model.ContentPartImage:
			raw, _ := json.Marshal(client.ImageContent{
				Type:     "image",
				MimeType: strings.TrimSpace(part.MimeType),
				Data:     strings.TrimSpace(part.Data),
				Name:     strings.TrimSpace(part.FileName),
			})
			out = append(out, raw)
		default:
			text := strings.TrimSpace(part.Text)
			if text == "" {
				continue
			}
			raw, _ := json.Marshal(client.TextContent{
				Type: "text",
				Text: text,
			})
			out = append(out, raw)
		}
	}
	if len(out) == 0 && strings.TrimSpace(input) != "" {
		raw, _ := json.Marshal(client.TextContent{
			Type: "text",
			Text: strings.TrimSpace(input),
		})
		out = append(out, raw)
	}
	return out
}

func ptrMessage(msg model.Message) *model.Message {
	return &msg
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func pickWorkDir(preferred string, fallback string) string {
	if strings.TrimSpace(preferred) != "" {
		return strings.TrimSpace(preferred)
	}
	return strings.TrimSpace(fallback)
}

func derefString(in *string) string {
	if in == nil {
		return ""
	}
	return strings.TrimSpace(*in)
}
