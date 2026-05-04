package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"strings"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
)

// Factory constructs baseline chat agents from one runtime.AgentSpec.
type Factory struct {
	SystemPrompt string
}

// Agent is the minimal model-backed chat agent.
type Agent struct {
	name         string
	model        sdkmodel.LLM
	tools        []sdktool.Tool
	systemPrompt string
	reasoning    sdkmodel.ReasoningConfig
	request      sdkruntime.ModelRequestOptions
}

// New returns one concrete chat agent.
func New(name string, model sdkmodel.LLM, systemPrompt string) (*Agent, error) {
	if model == nil {
		return nil, errors.New("sdk/runtime/agents/chat: model is required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "chat"
	}
	return &Agent{
		name:         name,
		model:        model,
		systemPrompt: strings.TrimSpace(systemPrompt),
	}, nil
}

// NewWithTools returns one chat agent with builtin tool access.
func NewWithTools(name string, model sdkmodel.LLM, tools []sdktool.Tool, systemPrompt string) (*Agent, error) {
	agent, err := New(name, model, systemPrompt)
	if err != nil {
		return nil, err
	}
	agent.tools = append([]sdktool.Tool(nil), tools...)
	return agent, nil
}

// NewAgent constructs one chat agent from one runtime.AgentSpec.
func (f Factory) NewAgent(_ context.Context, spec sdkruntime.AgentSpec) (sdkruntime.Agent, error) {
	systemPrompt := ""
	if raw, ok := spec.Metadata["system_prompt"].(string); ok {
		systemPrompt = strings.TrimSpace(raw)
	}
	if systemPrompt == "" {
		systemPrompt = strings.TrimSpace(f.SystemPrompt)
	}
	agent, err := NewWithTools(spec.Name, spec.Model, spec.Tools, systemPrompt)
	if err != nil {
		return nil, err
	}
	agent.reasoning = reasoningFromMetadata(spec.Metadata)
	agent.request = spec.Request
	return agent, nil
}

func (a *Agent) Name() string {
	return a.name
}

func (a *Agent) Run(ctx sdkruntime.Context) iter.Seq2[*sdksession.Event, error] {
	return func(yield func(*sdksession.Event, error) bool) {
		messages := messagesFromContext(ctx)
		stream := a.request.StreamEnabled(false)
		for {
			request := &sdkmodel.Request{
				Messages:  messages,
				Tools:     sdktool.ModelSpecs(a.tools),
				Reasoning: a.reasoning,
				Stream:    stream,
			}
			request.Instructions = append(request.Instructions, instructionsFromContext(ctx, a.systemPrompt)...)

			final, err := collectFinalResponse(ctx, a.model, request, func(event *sdksession.Event) bool {
				return yield(event, nil)
			})
			if err != nil {
				yield(nil, err)
				return
			}

			assistantMessage := sdkmodel.CloneMessage(final.Message)
			calls := assistantMessage.ToolCalls()
			if len(calls) == 0 {
				assistantEvent := modelResponseEvent(assistantMessage, final)
				if !yield(assistantEvent, nil) {
					return
				}
				return
			}
			toolCallEvents := modelToolCallEvents(assistantMessage, final)
			for _, event := range toolCallEvents {
				if !yield(event, nil) {
					return
				}
			}
			messages = append(messages, assistantMessage)
			for _, call := range calls {
				toolMessage, toolEvent, err := a.executeToolCallWithProgress(ctx, call, func(event *sdksession.Event) bool {
					return yield(event, nil)
				})
				if err != nil {
					yield(nil, err)
					return
				}
				if !yield(toolEvent, nil) {
					return
				}
				messages = append(messages, toolMessage)
			}
		}
	}
}

type toolObserver struct {
	results chan<- sdktool.Result
}

func (r toolObserver) ObserveToolResult(result sdktool.Result) {
	if r.results == nil {
		return
	}
	cloned, _ := sdktool.CloneResult(result, nil)
	select {
	case r.results <- cloned:
	default:
	}
}

type toolExecutionResult struct {
	message sdkmodel.Message
	event   *sdksession.Event
	err     error
}

func (a *Agent) executeToolCallWithProgress(
	ctx context.Context,
	call sdkmodel.ToolCall,
	yieldProgress func(*sdksession.Event) bool,
) (sdkmodel.Message, *sdksession.Event, error) {
	progressCh := make(chan sdktool.Result, 16)
	doneCh := make(chan toolExecutionResult, 1)
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		message, event, err := a.executeToolCall(callCtx, call, toolObserver{results: progressCh})
		doneCh <- toolExecutionResult{message: message, event: event, err: err}
	}()

	for {
		select {
		case progress := <-progressCh:
			if yieldProgress == nil {
				continue
			}
			if !yieldProgress(sdksession.MarkUIOnly(toolResultEvent(call, progress, nil))) {
				return sdkmodel.Message{}, nil, context.Canceled
			}
		case done := <-doneCh:
			for {
				select {
				case progress := <-progressCh:
					if yieldProgress != nil && !yieldProgress(sdksession.MarkUIOnly(toolResultEvent(call, progress, nil))) {
						return sdkmodel.Message{}, nil, context.Canceled
					}
				default:
					return done.message, done.event, done.err
				}
			}
		case <-ctx.Done():
			return sdkmodel.Message{}, nil, ctx.Err()
		}
	}
}

func reasoningFromMetadata(meta map[string]any) sdkmodel.ReasoningConfig {
	var reasoning sdkmodel.ReasoningConfig
	if raw, ok := meta["reasoning_effort"].(string); ok {
		reasoning.Effort = strings.TrimSpace(raw)
	}
	switch raw := meta["reasoning_budget_tokens"].(type) {
	case int:
		reasoning.BudgetTokens = raw
	case int64:
		reasoning.BudgetTokens = int(raw)
	case float64:
		reasoning.BudgetTokens = int(raw)
	}
	return reasoning
}

func collectFinalResponse(
	ctx context.Context,
	model sdkmodel.LLM,
	req *sdkmodel.Request,
	yieldChunk func(*sdksession.Event) bool,
) (*sdkmodel.Response, error) {
	var final *sdkmodel.Response
	for event, err := range model.Generate(ctx, req) {
		if err != nil {
			return nil, err
		}
		if req != nil && req.Stream {
			if chunk := chunkEventFromStreamEvent(event); chunk != nil && yieldChunk != nil {
				if !yieldChunk(chunk) {
					return nil, context.Canceled
				}
			}
		}
		if event != nil && event.Response != nil && event.TurnComplete {
			final = event.Response
		}
	}
	if final == nil {
		return nil, errors.New("sdk/runtime/agents/chat: model returned no final response")
	}
	return final, nil
}

func chunkEventFromStreamEvent(event *sdkmodel.StreamEvent) *sdksession.Event {
	if event == nil || event.PartDelta == nil {
		return nil
	}
	delta := event.PartDelta
	switch delta.Kind {
	case sdkmodel.PartKindReasoning:
		if delta.TextDelta == "" {
			return nil
		}
		message := sdkmodel.NewReasoningMessage(sdkmodel.RoleAssistant, delta.TextDelta, sdkmodel.ReasoningVisibilityVisible)
		return sdksession.MarkUIOnly(&sdksession.Event{
			Type:    sdksession.EventTypeAssistant,
			Message: &message,
			Text:    delta.TextDelta,
			Protocol: &sdksession.EventProtocol{
				UpdateType: string(sdksession.ProtocolUpdateTypeAgentThought),
				Update: &sdksession.ProtocolUpdate{
					SessionUpdate: string(sdksession.ProtocolUpdateTypeAgentThought),
					Content:       sdksession.ProtocolTextContent(delta.TextDelta),
				},
			},
		})
	case sdkmodel.PartKindText:
		if delta.TextDelta == "" {
			return nil
		}
		message := sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, delta.TextDelta)
		return sdksession.MarkUIOnly(&sdksession.Event{
			Type:    sdksession.EventTypeAssistant,
			Message: &message,
			Text:    delta.TextDelta,
			Protocol: &sdksession.EventProtocol{
				UpdateType: string(sdksession.ProtocolUpdateTypeAgentMessage),
				Update: &sdksession.ProtocolUpdate{
					SessionUpdate: string(sdksession.ProtocolUpdateTypeAgentMessage),
					Content:       sdksession.ProtocolTextContent(delta.TextDelta),
				},
			},
		})
	default:
		return nil
	}
}

func modelResponseEvent(message sdkmodel.Message, resp *sdkmodel.Response) *sdksession.Event {
	out := &sdksession.Event{
		Type:       sdksession.EventTypeOf(&sdksession.Event{Message: &message}),
		Visibility: sdksession.VisibilityCanonical,
		Message:    &message,
		Text:       message.TextContent(),
		Protocol: &sdksession.EventProtocol{
			UpdateType: string(sdksession.ProtocolUpdateTypeAgentMessage),
			Update: &sdksession.ProtocolUpdate{
				SessionUpdate: string(sdksession.ProtocolUpdateTypeAgentMessage),
				Content:       sdksession.ProtocolTextContent(message.TextContent()),
			},
		},
	}
	if resp != nil {
		out.Meta = responseMeta(resp)
	}
	return out
}

func modelToolCallEvents(message sdkmodel.Message, resp *sdkmodel.Response) []*sdksession.Event {
	calls := message.ToolCalls()
	if len(calls) == 0 {
		return nil
	}
	out := make([]*sdksession.Event, 0, len(calls))
	baseMeta := map[string]any{}
	if resp != nil {
		baseMeta = responseMeta(resp)
	}
	for i, call := range calls {
		rawInput := mustObject(call.Args)
		meta := toolMeta(call.Name)
		if i == 0 {
			meta = mergeEventMeta(baseMeta, meta)
		}
		event := &sdksession.Event{
			Type:     sdksession.EventTypeToolCall,
			Protocol: toolCallProtocol(call, sdksession.ProtocolUpdateTypeToolCall, "pending", rawInput, nil),
			Meta:     meta,
		}
		if i == 0 {
			event.Message = &message
			event.Text = message.TextContent()
			event.Protocol = protocolWithText(event.Protocol, event.Text)
		}
		out = append(out, event)
	}
	return out
}

func (a *Agent) executeToolCall(ctx context.Context, call sdkmodel.ToolCall, observer sdktool.Observer) (sdkmodel.Message, *sdksession.Event, error) {
	rawInput := mustObject(call.Args)
	tool, ok := a.lookupTool(call.Name)
	if !ok {
		rawOutput := map[string]any{"error": fmt.Sprintf("tool %q not found", call.Name)}
		message := toolResultMessage(call, sdktool.Result{
			ID:      call.ID,
			Name:    call.Name,
			IsError: true,
			Content: []sdkmodel.Part{sdkmodel.NewJSONPart(mustJSON(rawOutput))},
		})
		return message, &sdksession.Event{
			Type:     sdksession.EventTypeToolResult,
			Message:  &message,
			Text:     message.TextContent(),
			Protocol: toolCallProtocol(call, sdksession.ProtocolUpdateTypeToolUpdate, "failed", rawInput, rawOutput),
			Meta:     mergeEventMeta(toolMeta(call.Name)),
		}, nil
	}

	result, err := tool.Call(ctx, sdktool.Call{
		ID:       strings.TrimSpace(call.ID),
		Name:     strings.TrimSpace(call.Name),
		Input:    json.RawMessage(strings.TrimSpace(call.Args)),
		Observer: observer,
	})
	if err != nil {
		result = sdktool.Result{
			ID:      strings.TrimSpace(call.ID),
			Name:    strings.TrimSpace(call.Name),
			IsError: true,
			Content: []sdkmodel.Part{sdkmodel.NewJSONPart(mustJSON(map[string]any{"error": err.Error()}))},
		}
	}
	message := toolResultMessage(call, result)
	event := toolResultEvent(call, result, &message)
	return message, event, nil
}

func toolResultEvent(call sdkmodel.ToolCall, result sdktool.Result, message *sdkmodel.Message) *sdksession.Event {
	rawInput := mustObject(call.Args)
	rawOutput := maps.Clone(result.Meta)
	event := &sdksession.Event{
		Type:     sdksession.EventTypeToolResult,
		Protocol: toolCallProtocol(call, sdksession.ProtocolUpdateTypeToolUpdate, toolCallStatus(result), rawInput, rawOutput),
		Meta:     mergeEventMeta(toolMeta(call.Name), result.Metadata),
	}
	if message != nil {
		event.Message = message
		event.Text = message.TextContent()
	}
	return event
}

func (a *Agent) lookupTool(name string) (sdktool.Tool, bool) {
	name = strings.TrimSpace(strings.ToUpper(name))
	for _, item := range a.tools {
		if item == nil {
			continue
		}
		if strings.TrimSpace(strings.ToUpper(item.Definition().Name)) == name {
			return item, true
		}
	}
	return nil, false
}

func toolResultMessage(call sdkmodel.ToolCall, result sdktool.Result) sdkmodel.Message {
	if len(result.Content) == 0 {
		result.Content = []sdkmodel.Part{sdkmodel.NewJSONPart(mustJSON(result.Meta))}
	}
	parts := sdkmodel.CloneParts(result.Content)
	if len(parts) == 0 {
		parts = []sdkmodel.Part{sdkmodel.NewJSONPart(mustJSON(map[string]any{}))}
	}
	return sdkmodel.Message{
		Role: sdkmodel.RoleTool,
		Parts: []sdkmodel.Part{{
			Kind: sdkmodel.PartKindToolResult,
			ToolResult: &sdkmodel.ToolResultPart{
				ToolUseID: strings.TrimSpace(firstNonEmpty(result.ID, call.ID)),
				Name:      strings.TrimSpace(firstNonEmpty(result.Name, call.Name)),
				Content:   parts,
				IsError:   result.IsError,
			},
		}},
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func mustJSON(value map[string]any) json.RawMessage {
	if value == nil {
		value = map[string]any{}
	}
	raw, _ := json.Marshal(value)
	return raw
}

func mustObject(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func toolKindForName(name string) string {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "READ":
		return "read"
	case "WRITE", "PATCH":
		return "edit"
	case "SEARCH", "GLOB", "LIST":
		return "search"
	case "PLAN":
		return "other"
	case "BASH", "SPAWN", "TASK":
		return "execute"
	default:
		return "other"
	}
}

func toolCallTitle(call sdkmodel.ToolCall) string {
	name := strings.TrimSpace(call.Name)
	args := mustObject(call.Args)
	switch strings.ToUpper(name) {
	case "READ", "WRITE", "PATCH", "SEARCH", "LIST", "GLOB":
		if path, _ := args["path"].(string); strings.TrimSpace(path) != "" {
			return fmt.Sprintf("%s %s", name, strings.TrimSpace(path))
		}
	case "BASH":
		if command, _ := args["command"].(string); strings.TrimSpace(command) != "" {
			return fmt.Sprintf("BASH %s", strings.TrimSpace(command))
		}
	case "SPAWN":
		if agent, _ := args["agent"].(string); strings.TrimSpace(agent) != "" {
			return fmt.Sprintf("SPAWN %s", strings.TrimSpace(agent))
		}
		if prompt, _ := args["prompt"].(string); strings.TrimSpace(prompt) != "" {
			return fmt.Sprintf("SPAWN %s", strings.TrimSpace(prompt))
		}
	case "TASK":
		action, _ := args["action"].(string)
		taskID, _ := args["task_id"].(string)
		if strings.TrimSpace(action) != "" && strings.TrimSpace(taskID) != "" {
			return fmt.Sprintf("TASK %s %s", strings.TrimSpace(action), strings.TrimSpace(taskID))
		}
	}
	return name
}

func toolCallStatus(result sdktool.Result) string {
	if state, _ := result.Meta["state"].(string); strings.TrimSpace(state) != "" {
		switch strings.TrimSpace(state) {
		case "running", "waiting_input", "waiting_approval":
			return strings.TrimSpace(state)
		}
	}
	if result.IsError {
		return "failed"
	}
	return "completed"
}

func mergeEventMeta(parts ...map[string]any) map[string]any {
	out := map[string]any{}
	for _, part := range parts {
		for key, value := range part {
			if existing, ok := out[key].(map[string]any); ok {
				if incoming, ok := value.(map[string]any); ok {
					out[key] = mergeAnyMap(existing, incoming)
					continue
				}
			}
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func mergeAnyMap(base map[string]any, overlay map[string]any) map[string]any {
	out := maps.Clone(base)
	for key, value := range overlay {
		if existing, ok := out[key].(map[string]any); ok {
			if incoming, ok := value.(map[string]any); ok {
				out[key] = mergeAnyMap(existing, incoming)
				continue
			}
		}
		out[key] = value
	}
	return out
}

func responseMeta(resp *sdkmodel.Response) map[string]any {
	if resp == nil {
		return nil
	}
	usage := map[string]any{
		"prompt_tokens":       resp.Usage.PromptTokens,
		"cached_input_tokens": resp.Usage.CachedInputTokens,
		"completion_tokens":   resp.Usage.CompletionTokens,
		"total_tokens":        resp.Usage.TotalTokens,
	}
	return map[string]any{
		"caelis": map[string]any{
			"version": 1,
			"sdk": map[string]any{
				"model":         strings.TrimSpace(resp.Model),
				"provider":      strings.TrimSpace(resp.Provider),
				"finish_reason": string(resp.FinishReason),
				"usage":         usage,
			},
		},
	}
}

func toolMeta(name string) map[string]any {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	return map[string]any{
		"caelis": map[string]any{
			"version": 1,
			"runtime": map[string]any{
				"tool": map[string]any{
					"name": name,
				},
			},
		},
	}
}

func toolCallProtocol(call sdkmodel.ToolCall, updateType sdksession.ProtocolUpdateType, status string, rawInput map[string]any, rawOutput map[string]any) *sdksession.EventProtocol {
	tool := &sdksession.ProtocolToolCall{
		ID:        strings.TrimSpace(call.ID),
		Name:      strings.TrimSpace(call.Name),
		Kind:      toolKindForName(call.Name),
		Title:     toolCallTitle(call),
		Status:    strings.TrimSpace(status),
		RawInput:  maps.Clone(rawInput),
		RawOutput: maps.Clone(rawOutput),
	}
	return &sdksession.EventProtocol{
		UpdateType: string(updateType),
		ToolCall:   tool,
		Update: &sdksession.ProtocolUpdate{
			SessionUpdate: string(updateType),
			ToolCallID:    tool.ID,
			Kind:          tool.Kind,
			Title:         tool.Title,
			Status:        tool.Status,
			RawInput:      maps.Clone(rawInput),
			RawOutput:     maps.Clone(rawOutput),
		},
	}
}

func protocolWithText(protocol *sdksession.EventProtocol, text string) *sdksession.EventProtocol {
	if strings.TrimSpace(text) == "" {
		return protocol
	}
	out := cloneChatEventProtocol(protocol)
	if out.Update == nil {
		out.Update = &sdksession.ProtocolUpdate{}
	}
	if out.Update.Content == nil {
		out.Update.Content = sdksession.ProtocolTextContent(text)
	}
	return &out
}

func cloneChatEventProtocol(protocol *sdksession.EventProtocol) sdksession.EventProtocol {
	if protocol == nil {
		return sdksession.EventProtocol{}
	}
	out := *protocol
	if protocol.Update != nil {
		update := *protocol.Update
		update.RawInput = maps.Clone(protocol.Update.RawInput)
		update.RawOutput = maps.Clone(protocol.Update.RawOutput)
		out.Update = &update
	}
	if protocol.ToolCall != nil {
		toolCall := *protocol.ToolCall
		toolCall.RawInput = maps.Clone(protocol.ToolCall.RawInput)
		toolCall.RawOutput = maps.Clone(protocol.ToolCall.RawOutput)
		out.ToolCall = &toolCall
	}
	if protocol.Plan != nil {
		plan := *protocol.Plan
		plan.Entries = append([]sdksession.ProtocolPlanEntry(nil), protocol.Plan.Entries...)
		out.Plan = &plan
	}
	return out
}

func messagesFromContext(ctx sdkruntime.Context) []sdkmodel.Message {
	if ctx == nil {
		return nil
	}
	events := make([]*sdksession.Event, 0, ctx.Events().Len())
	for event := range ctx.Events().All() {
		events = append(events, event)
	}
	out := make([]sdkmodel.Message, 0, len(events))
	for i := 0; i < len(events); {
		event := events[i]
		if event == nil || !sdksession.IsMainInvocationVisibleEvent(event) {
			i++
			continue
		}
		if sdksession.EventTypeOf(event) == sdksession.EventTypeToolCall {
			if message, next, ok := toolCallMessageFromEventRun(events, i); ok {
				out = append(out, message)
				i = next
				continue
			}
		}
		message, ok := messageFromInvocationEvent(event)
		if !ok {
			i++
			continue
		}
		out = append(out, message)
		i++
	}
	return normalizeToolCallHistory(out)
}

func toolCallMessageFromEventRun(events []*sdksession.Event, start int) (sdkmodel.Message, int, bool) {
	if start < 0 || start >= len(events) {
		return sdkmodel.Message{}, start + 1, false
	}
	calls := make([]sdkmodel.ToolCall, 0, 1)
	text := ""
	next := start
	for next < len(events) {
		event := events[next]
		if event == nil || !sdksession.IsMainInvocationVisibleEvent(event) || sdksession.EventTypeOf(event) != sdksession.EventTypeToolCall {
			break
		}
		if text == "" {
			text = strings.TrimSpace(sdksession.EventText(event))
		}
		call, ok := toolCallFromProtocolEvent(event)
		if !ok {
			if event.Message != nil {
				for _, item := range event.Message.ToolCalls() {
					if strings.TrimSpace(item.ID) != "" && strings.TrimSpace(item.Name) != "" {
						calls = append(calls, item)
					}
				}
			}
			next++
			continue
		}
		calls = append(calls, call)
		next++
	}
	if len(calls) == 0 {
		return sdkmodel.Message{}, start + 1, false
	}
	return sdkmodel.MessageFromToolCalls(sdkmodel.RoleAssistant, calls, text), next, true
}

func toolCallFromProtocolEvent(event *sdksession.Event) (sdkmodel.ToolCall, bool) {
	update := sdksession.ProtocolUpdateOf(event)
	if update == nil {
		return sdkmodel.ToolCall{}, false
	}
	call := sdkmodel.ToolCall{
		ID:   strings.TrimSpace(update.ToolCallID),
		Name: toolNameFromEvent(event),
		Args: string(mustJSON(update.RawInput)),
	}
	if call.ID == "" || call.Name == "" {
		return sdkmodel.ToolCall{}, false
	}
	return call, true
}

func normalizeToolCallHistory(messages []sdkmodel.Message) []sdkmodel.Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]sdkmodel.Message, 0, len(messages))
	for i := 0; i < len(messages); i++ {
		calls := messages[i].ToolCalls()
		if len(calls) == 0 {
			if len(messages[i].ToolResults()) > 0 {
				continue
			}
			out = append(out, messages[i])
			continue
		}
		required := map[string]struct{}{}
		for _, call := range calls {
			if id := strings.TrimSpace(call.ID); id != "" {
				required[id] = struct{}{}
			}
		}
		if len(required) == 0 {
			continue
		}
		run := []sdkmodel.Message{messages[i]}
		next := i + 1
		valid := true
		for len(required) > 0 {
			if next >= len(messages) {
				valid = false
				break
			}
			results := messages[next].ToolResults()
			if len(results) == 0 {
				valid = false
				break
			}
			matched := false
			for _, result := range results {
				if id := strings.TrimSpace(result.ToolUseID); id != "" {
					if _, ok := required[id]; ok {
						delete(required, id)
						matched = true
					}
				}
			}
			if !matched {
				valid = false
				break
			}
			run = append(run, messages[next])
			next++
		}
		if valid {
			out = append(out, run...)
		}
		for next < len(messages) && len(messages[next].ToolResults()) > 0 {
			next++
		}
		i = next - 1
	}
	return out
}

func messageFromInvocationEvent(event *sdksession.Event) (sdkmodel.Message, bool) {
	if event == nil || !sdksession.IsMainInvocationVisibleEvent(event) {
		return sdkmodel.Message{}, false
	}
	if event.Scope == nil || strings.TrimSpace(event.Scope.Participant.ID) == "" {
		if event.Message != nil {
			return sdkmodel.CloneMessage(*event.Message), true
		}
		return messageFromProtocolEvent(event)
	}
	text := strings.TrimSpace(sdksession.EventText(event))
	if text == "" {
		return sdkmodel.Message{}, false
	}
	label := participantInvocationLabel(*event)
	switch sdksession.EventTypeOf(event) {
	case sdksession.EventTypeUser:
		return sdkmodel.NewTextMessage(sdkmodel.RoleUser, fmt.Sprintf("User to %s: %s", label, text)), true
	case sdksession.EventTypeAssistant:
		return sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, fmt.Sprintf("Assistant(%s): %s", label, text)), true
	default:
		if event.Message != nil {
			return sdkmodel.CloneMessage(*event.Message), true
		}
		return messageFromProtocolEvent(event)
	}
}

func messageFromProtocolEvent(event *sdksession.Event) (sdkmodel.Message, bool) {
	update := sdksession.ProtocolUpdateOf(event)
	switch sdksession.EventTypeOf(event) {
	case sdksession.EventTypeUser:
		if text := strings.TrimSpace(sdksession.EventText(event)); text != "" {
			return sdkmodel.NewTextMessage(sdkmodel.RoleUser, text), true
		}
	case sdksession.EventTypeAssistant:
		if text := strings.TrimSpace(sdksession.EventText(event)); text != "" {
			return sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, text), true
		}
	case sdksession.EventTypeToolCall:
		if update == nil {
			return sdkmodel.Message{}, false
		}
		call := sdkmodel.ToolCall{
			ID:   strings.TrimSpace(update.ToolCallID),
			Name: toolNameFromEvent(event),
			Args: string(mustJSON(update.RawInput)),
		}
		if call.ID == "" || call.Name == "" {
			return sdkmodel.Message{}, false
		}
		return sdkmodel.MessageFromToolCalls(sdkmodel.RoleAssistant, []sdkmodel.ToolCall{call}, ""), true
	case sdksession.EventTypeToolResult:
		if update == nil {
			return sdkmodel.Message{}, false
		}
		name := toolNameFromEvent(event)
		if update.ToolCallID == "" || name == "" {
			return sdkmodel.Message{}, false
		}
		message := sdkmodel.Message{
			Role: sdkmodel.RoleTool,
			Parts: []sdkmodel.Part{sdkmodel.NewToolResultJSONPart(
				strings.TrimSpace(update.ToolCallID),
				name,
				maps.Clone(update.RawOutput),
				strings.EqualFold(strings.TrimSpace(update.Status), "failed"),
			)},
		}
		return message, true
	}
	return sdkmodel.Message{}, false
}

func toolNameFromEvent(event *sdksession.Event) string {
	if event == nil {
		return ""
	}
	if name := strings.TrimSpace(stringFromNestedMap(event.Meta, "caelis", "runtime", "tool", "name")); name != "" {
		return name
	}
	if event.Protocol != nil && event.Protocol.ToolCall != nil {
		if name := strings.TrimSpace(event.Protocol.ToolCall.Name); name != "" {
			return name
		}
	}
	if update := sdksession.ProtocolUpdateOf(event); update != nil {
		if title := strings.Fields(strings.TrimSpace(update.Title)); len(title) > 0 {
			return title[0]
		}
		return strings.TrimSpace(update.Kind)
	}
	return ""
}

func stringFromNestedMap(values map[string]any, path ...string) string {
	var current any = values
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = mapped[key]
	}
	text, _ := current.(string)
	return strings.TrimSpace(text)
}

func participantInvocationLabel(event sdksession.Event) string {
	if mention := strings.TrimSpace(stringFromFlatMap(event.Meta, "mention")); mention != "" {
		return mention
	}
	if handle := strings.TrimSpace(stringFromFlatMap(event.Meta, "handle")); handle != "" {
		if strings.HasPrefix(handle, "@") {
			return handle
		}
		return "@" + handle
	}
	if agent := strings.TrimSpace(stringFromFlatMap(event.Meta, "agent")); agent != "" {
		return agent
	}
	if name := strings.TrimSpace(event.Actor.Name); name != "" && !strings.EqualFold(name, "user") {
		return name
	}
	if id := strings.TrimSpace(event.Actor.ID); id != "" && event.Actor.Kind == sdksession.ActorKindParticipant {
		return id
	}
	if id := strings.TrimSpace(event.Actor.ID); id != "" {
		return id
	}
	if event.Scope != nil {
		if id := strings.TrimSpace(event.Scope.Participant.ID); id != "" {
			return id
		}
	}
	return "participant"
}

func stringFromFlatMap(values map[string]any, key string) string {
	if len(values) == 0 {
		return ""
	}
	text, _ := values[key].(string)
	return strings.TrimSpace(text)
}

func instructionsFromContext(_ sdkruntime.Context, systemPrompt string) []sdkmodel.Part {
	out := make([]sdkmodel.Part, 0, 1)
	if strings.TrimSpace(systemPrompt) != "" {
		out = append(out, sdkmodel.NewTextPart(strings.TrimSpace(systemPrompt)))
	}
	return out
}

// Metadata returns one stable agent metadata map for upstream assembly.
func Metadata(systemPrompt string) map[string]any {
	systemPrompt = strings.TrimSpace(systemPrompt)
	if systemPrompt == "" {
		return nil
	}
	return map[string]any{"system_prompt": systemPrompt}
}

// CloneMetadata returns one shallow metadata copy.
func CloneMetadata(values map[string]any) map[string]any {
	return maps.Clone(values)
}
