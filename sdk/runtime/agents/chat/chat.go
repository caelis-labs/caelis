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
		},
	}
	if resp != nil {
		out.Meta = map[string]any{
			"model":             strings.TrimSpace(resp.Model),
			"provider":          strings.TrimSpace(resp.Provider),
			"finish_reason":     string(resp.FinishReason),
			"prompt_tokens":     resp.Usage.PromptTokens,
			"completion_tokens": resp.Usage.CompletionTokens,
			"total_tokens":      resp.Usage.TotalTokens,
		}
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
		baseMeta["model"] = strings.TrimSpace(resp.Model)
		baseMeta["provider"] = strings.TrimSpace(resp.Provider)
		baseMeta["finish_reason"] = string(resp.FinishReason)
		baseMeta["prompt_tokens"] = resp.Usage.PromptTokens
		baseMeta["completion_tokens"] = resp.Usage.CompletionTokens
		baseMeta["total_tokens"] = resp.Usage.TotalTokens
	}
	for i, call := range calls {
		rawInput := mustObject(call.Args)
		event := &sdksession.Event{
			Type: sdksession.EventTypeToolCall,
			Protocol: &sdksession.EventProtocol{
				UpdateType: string(sdksession.ProtocolUpdateTypeToolCall),
				ToolCall: &sdksession.ProtocolToolCall{
					ID:       strings.TrimSpace(call.ID),
					Name:     strings.TrimSpace(call.Name),
					Kind:     toolKindForName(call.Name),
					Title:    toolCallTitle(call),
					Status:   "pending",
					RawInput: rawInput,
				},
			},
			Meta: mergeEventMeta(baseMeta, caelisToolDisplayMeta(call.Name, "pending", rawInput, nil)),
		}
		if i == 0 {
			event.Message = &message
			event.Text = message.TextContent()
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
			Type:    sdksession.EventTypeToolResult,
			Message: &message,
			Text:    message.TextContent(),
			Protocol: &sdksession.EventProtocol{
				UpdateType: string(sdksession.ProtocolUpdateTypeToolUpdate),
				ToolCall: &sdksession.ProtocolToolCall{
					ID:        strings.TrimSpace(call.ID),
					Name:      strings.TrimSpace(call.Name),
					Kind:      toolKindForName(call.Name),
					Title:     toolCallTitle(call),
					Status:    "failed",
					RawInput:  rawInput,
					RawOutput: rawOutput,
				},
			},
			Meta: mergeEventMeta(map[string]any{
				"tool_name":    strings.TrimSpace(call.Name),
				"tool_call_id": strings.TrimSpace(call.ID),
				"is_error":     true,
			}, caelisToolDisplayMeta(call.Name, "failed", rawInput, rawOutput)),
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
		Type: sdksession.EventTypeToolResult,
		Protocol: &sdksession.EventProtocol{
			UpdateType: string(sdksession.ProtocolUpdateTypeToolUpdate),
			ToolCall: &sdksession.ProtocolToolCall{
				ID:        strings.TrimSpace(call.ID),
				Name:      strings.TrimSpace(call.Name),
				Kind:      toolKindForName(call.Name),
				Title:     toolCallTitle(call),
				Status:    toolCallStatus(result),
				RawInput:  rawInput,
				RawOutput: rawOutput,
			},
		},
		Meta: mergeEventMeta(
			map[string]any{
				"tool_name":    strings.TrimSpace(call.Name),
				"tool_call_id": strings.TrimSpace(call.ID),
				"is_error":     result.IsError,
			},
			result.Meta,
			caelisToolDisplayMeta(call.Name, toolCallStatus(result), rawInput, rawOutput),
		),
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
	case "BASH", "TASK":
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
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func caelisToolDisplayMeta(name string, status string, rawInput map[string]any, rawOutput map[string]any) map[string]any {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	display := map[string]any{
		"tool": map[string]any{
			"name":   name,
			"family": toolKindForName(name),
			"status": strings.TrimSpace(status),
		},
	}
	switch strings.ToUpper(name) {
	case "READ", "LIST", "GLOB", "SEARCH", "RG", "FIND":
		display["file"] = compactMetaMap(map[string]any{
			"path":       firstNonEmpty(metaString(rawOutput, "path"), metaString(rawInput, "path")),
			"pattern":    firstNonEmpty(metaString(rawOutput, "pattern"), metaString(rawInput, "pattern")),
			"query":      firstNonEmpty(metaString(rawOutput, "query"), metaString(rawInput, "query"), metaString(rawInput, "pattern")),
			"start_line": rawOutput["start_line"],
			"end_line":   rawOutput["end_line"],
			"count":      rawOutput["count"],
			"file_count": rawOutput["file_count"],
		})
	case "WRITE", "PATCH":
		display["diff"] = compactMetaMap(map[string]any{
			"path":          firstNonEmpty(metaString(rawOutput, "path"), metaString(rawInput, "path")),
			"hunk":          rawOutput["hunk"],
			"old":           rawInput["old"],
			"new":           rawInput["new"],
			"added_lines":   rawOutput["added_lines"],
			"removed_lines": rawOutput["removed_lines"],
			"created":       rawOutput["created"],
		})
	case "BASH", "SPAWN", "TASK":
		display["terminal"] = compactMetaMap(map[string]any{
			"command":        firstNonEmpty(metaString(rawInput, "command"), metaString(rawInput, "cmd")),
			"stdout":         rawOutput["stdout"],
			"stderr":         rawOutput["stderr"],
			"result":         rawOutput["result"],
			"output_preview": rawOutput["output_preview"],
			"exit_code":      rawOutput["exit_code"],
			"state":          rawOutput["state"],
			"running":        rawOutput["running"],
			"task_id":        rawOutput["task_id"],
		})
	}
	return map[string]any{"caelis": map[string]any{"display": display}}
}

func compactMetaMap(in map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range in {
		switch typed := value.(type) {
		case nil:
			continue
		case string:
			if strings.TrimSpace(typed) == "" {
				continue
			}
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func metaString(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func messagesFromContext(ctx sdkruntime.Context) []sdkmodel.Message {
	if ctx == nil {
		return nil
	}
	out := make([]sdkmodel.Message, 0, ctx.Events().Len())
	for event := range ctx.Events().All() {
		if !sdksession.IsMainInvocationVisibleEvent(event) || event == nil || event.Message == nil {
			continue
		}
		out = append(out, sdkmodel.CloneMessage(*event.Message))
	}
	return out
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
