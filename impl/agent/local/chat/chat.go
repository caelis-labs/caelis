package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/OnslaughtSnail/caelis/internal/displaypolicy"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

// Factory constructs baseline chat agents from one runtime.AgentSpec.
type Factory struct {
	SystemPrompt string
}

// Agent is the minimal model-backed chat agent.
type Agent struct {
	name         string
	model        model.LLM
	tools        []tool.Tool
	systemPrompt string
	reasoning    model.ReasoningConfig
	request      agent.ModelRequestOptions
}

// New returns one concrete chat agent.
func New(name string, model model.LLM, systemPrompt string) (*Agent, error) {
	if model == nil {
		return nil, errors.New("impl/agent/local/chat: model is required")
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
func NewWithTools(name string, model model.LLM, tools []tool.Tool, systemPrompt string) (*Agent, error) {
	agent, err := New(name, model, systemPrompt)
	if err != nil {
		return nil, err
	}
	agent.tools = append([]tool.Tool(nil), tools...)
	return agent, nil
}

// NewAgent constructs one chat agent from one runtime.AgentSpec.
func (f Factory) NewAgent(_ context.Context, spec agent.AgentSpec) (agent.Agent, error) {
	systemPrompt := ""
	if raw, ok := spec.Metadata["system_prompt"].(string); ok {
		systemPrompt = strings.TrimSpace(raw)
	}
	if systemPrompt == "" {
		systemPrompt = strings.TrimSpace(f.SystemPrompt)
	}
	chatAgent, err := NewWithTools(spec.Name, spec.Model, spec.Tools, systemPrompt)
	if err != nil {
		return nil, err
	}
	chatAgent.reasoning = reasoningFromMetadata(spec.Metadata)
	chatAgent.request = spec.Request.WithDefaults(agent.ModelRequestOptions{})
	return chatAgent, nil
}

func (a *Agent) Name() string {
	return a.name
}

func (a *Agent) Run(ctx agent.Context) iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		messages := messagesFromContext(ctx)
		stream := a.request.StreamEnabled(false)
		for {
			request := &model.Request{
				Messages:  messages,
				Tools:     tool.ModelSpecs(a.tools),
				Reasoning: a.reasoning,
				Output:    a.request.OutputSpec(),
				Stream:    stream,
			}
			request.Instructions = append(request.Instructions, instructionsFromContext(ctx, a.systemPrompt)...)

			final, err := collectFinalResponse(ctx, a.model, request, func(event *session.Event) bool {
				return yield(event, nil)
			})
			if err != nil {
				yield(nil, err)
				return
			}

			assistantMessage := model.CloneMessage(final.Message)
			calls := assistantMessage.ToolCalls()
			if len(calls) == 0 {
				assistantEvent := modelResponseEvent(assistantMessage, final)
				if !yield(assistantEvent, nil) {
					return
				}
				messages = append(messages, assistantMessage)
				if a.drainPendingSubmissions(ctx, &messages, func(event *session.Event) bool {
					return yield(event, nil)
				}) {
					continue
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
				toolMessage, toolEvent, err := a.executeToolCallWithProgress(ctx, call, func(event *session.Event) bool {
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
			a.drainPendingSubmissions(ctx, &messages, func(event *session.Event) bool {
				return yield(event, nil)
			})
		}
	}
}

func (a *Agent) drainPendingSubmissions(ctx agent.Context, messages *[]model.Message, yield func(*session.Event) bool) bool {
	if ctx == nil {
		return false
	}
	drained := ctx.DrainSubmissions()
	accepted := false
	for _, submission := range drained {
		if !isConversationSubmission(submission) {
			continue
		}
		text := strings.TrimSpace(submission.Text)
		if text == "" {
			continue
		}
		message := model.NewTextMessage(model.RoleUser, text)
		event := &session.Event{
			Type:       session.EventTypeUser,
			Visibility: session.VisibilityCanonical,
			Actor:      session.ActorRef{Kind: session.ActorKindUser, Name: "user"},
			Message:    &message,
			Text:       message.TextContent(),
			Meta:       pendingSubmissionMeta(submission),
		}
		if !yield(event) {
			return accepted
		}
		*messages = append(*messages, message)
		accepted = true
	}
	return accepted
}

func isConversationSubmission(sub agent.Submission) bool {
	switch sub.Kind {
	case agent.SubmissionKindConversation:
		return true
	default:
		return false
	}
}

func pendingSubmissionMeta(sub agent.Submission) map[string]any {
	meta := maps.Clone(sub.Metadata)
	if len(meta) == 0 {
		return nil
	}
	return meta
}

type toolObserver struct {
	results chan<- tool.Result
}

func (r toolObserver) ObserveToolResult(result tool.Result) {
	if r.results == nil {
		return
	}
	cloned, _ := tool.CloneResult(result, nil)
	select {
	case r.results <- cloned:
	default:
	}
}

type toolExecutionResult struct {
	message model.Message
	event   *session.Event
	err     error
}

func (a *Agent) executeToolCallWithProgress(
	ctx context.Context,
	call model.ToolCall,
	yieldProgress func(*session.Event) bool,
) (model.Message, *session.Event, error) {
	progressCh := make(chan tool.Result, 16)
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
			if !yieldProgress(session.MarkUIOnly(toolResultEvent(call, progress, nil))) {
				return model.Message{}, nil, context.Canceled
			}
		case done := <-doneCh:
			for {
				select {
				case progress := <-progressCh:
					if yieldProgress != nil && !yieldProgress(session.MarkUIOnly(toolResultEvent(call, progress, nil))) {
						return model.Message{}, nil, context.Canceled
					}
				default:
					return done.message, done.event, done.err
				}
			}
		case <-ctx.Done():
			return model.Message{}, nil, ctx.Err()
		}
	}
}

func reasoningFromMetadata(meta map[string]any) model.ReasoningConfig {
	var reasoning model.ReasoningConfig
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
	llm model.LLM,
	req *model.Request,
	yieldChunk func(*session.Event) bool,
) (*model.Response, error) {
	var final *model.Response
	for event, err := range llm.Generate(ctx, req) {
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
		return nil, errors.New("impl/agent/local/chat: model returned no final response")
	}
	return final, nil
}

func chunkEventFromStreamEvent(event *model.StreamEvent) *session.Event {
	if event == nil || event.PartDelta == nil {
		return nil
	}
	delta := event.PartDelta
	switch delta.Kind {
	case model.PartKindReasoning:
		if delta.TextDelta == "" {
			return nil
		}
		message := model.NewReasoningMessage(model.RoleAssistant, delta.TextDelta, model.ReasoningVisibilityVisible)
		return session.MarkUIOnly(&session.Event{
			Type:    session.EventTypeAssistant,
			Message: &message,
			Text:    delta.TextDelta,
			Protocol: &session.EventProtocol{
				UpdateType: string(session.ProtocolUpdateTypeAgentThought),
				Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeAgentThought),
					Content:       session.ProtocolTextContent(delta.TextDelta),
				},
			},
		})
	case model.PartKindText:
		if delta.TextDelta == "" {
			return nil
		}
		message := model.NewTextMessage(model.RoleAssistant, delta.TextDelta)
		return session.MarkUIOnly(&session.Event{
			Type:    session.EventTypeAssistant,
			Message: &message,
			Text:    delta.TextDelta,
			Protocol: &session.EventProtocol{
				UpdateType: string(session.ProtocolUpdateTypeAgentMessage),
				Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
					Content:       session.ProtocolTextContent(delta.TextDelta),
				},
			},
		})
	default:
		return nil
	}
}

func modelResponseEvent(message model.Message, resp *model.Response) *session.Event {
	out := &session.Event{
		Type:       session.EventTypeOf(&session.Event{Message: &message}),
		Visibility: session.VisibilityCanonical,
		Message:    &message,
		Text:       message.TextContent(),
	}
	if resp != nil {
		out.Meta = responseMeta(resp)
	}
	return out
}

func modelToolCallEvents(message model.Message, resp *model.Response) []*session.Event {
	calls := message.ToolCalls()
	if len(calls) == 0 {
		return nil
	}
	out := make([]*session.Event, 0, len(calls))
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
		event := &session.Event{
			Type:       session.EventTypeToolCall,
			Visibility: session.VisibilityCanonical,
			Tool:       toolEventPayload(call, "pending", rawInput, nil, nil),
			Meta:       meta,
		}
		if i == 0 {
			event.Message = &message
			event.Text = message.TextContent()
		}
		out = append(out, event)
	}
	return out
}

func (a *Agent) executeToolCall(ctx context.Context, call model.ToolCall, observer tool.Observer) (model.Message, *session.Event, error) {
	selectedTool, ok := a.lookupTool(call.Name)
	if !ok {
		rawOutput := tool.ErrorPayload(tool.NewError(tool.ErrorCodeNotFound, fmt.Sprintf("tool %q not found", call.Name)))
		result := tool.Result{
			ID:      call.ID,
			Name:    call.Name,
			IsError: true,
			Content: []model.Part{model.NewJSONPart(mustJSON(rawOutput))},
		}
		canonical, truncationMeta := canonicalToolResult(result)
		message := toolResultMessageFromCanonical(call, canonical)
		return message, toolResultEvent(call, canonical, &message, truncationMeta), nil
	}

	result, err := selectedTool.Call(ctx, tool.Call{
		ID:       strings.TrimSpace(call.ID),
		Name:     strings.TrimSpace(call.Name),
		Input:    json.RawMessage(strings.TrimSpace(call.Args)),
		Observer: observer,
	})
	if err != nil {
		result = tool.Result{
			ID:      strings.TrimSpace(call.ID),
			Name:    strings.TrimSpace(call.Name),
			IsError: true,
			Content: []model.Part{model.NewJSONPart(mustJSON(tool.ErrorPayload(err)))},
		}
	}
	canonical, truncationMeta := canonicalToolResult(result)
	message := toolResultMessageFromCanonical(call, canonical)
	event := toolResultEvent(call, canonical, &message, truncationMeta)
	return message, event, nil
}

func toolResultEvent(call model.ToolCall, result tool.Result, message *model.Message, extraMeta ...map[string]any) *session.Event {
	rawInput := mustObject(call.Args)
	rawOutput := toolResultRawOutput(result)
	metaParts := []map[string]any{toolMeta(call.Name), result.Metadata}
	metaParts = append(metaParts, extraMeta...)
	status := toolCallStatus(result, rawOutput)
	meta := mergeEventMeta(metaParts...)
	event := &session.Event{
		Type: session.EventTypeToolResult,
		Tool: toolEventPayload(call, status, rawInput, rawOutput, toolResultContent(call, rawInput, rawOutput, meta, status, result.IsError)),
		Meta: meta,
	}
	if message != nil {
		event.Message = message
		event.Text = message.TextContent()
	}
	return event
}

func toolResultRawOutput(result tool.Result) map[string]any {
	for _, part := range result.Content {
		if part.JSON == nil || len(part.JSON.Value) == 0 {
			continue
		}
		var decoded any
		if err := json.Unmarshal(part.JSON.Value, &decoded); err != nil {
			return map[string]any{"result": string(part.JSON.Value)}
		}
		if payload, ok := decoded.(map[string]any); ok {
			return maps.Clone(payload)
		}
		return map[string]any{"result": decoded}
	}
	for _, part := range result.Content {
		if part.Text != nil {
			return map[string]any{"result": part.Text.Text}
		}
	}
	if result.IsError {
		return map[string]any{"error": "tool call failed"}
	}
	return map[string]any{}
}

func toolResultContent(call model.ToolCall, input map[string]any, output map[string]any, meta map[string]any, status string, isErr bool) []session.EventToolContent {
	name := strings.ToUpper(strings.TrimSpace(call.Name))
	displayOutput := toolResultDisplayOutput(name, output, meta)
	if name == "TASK" && !isErr && !strings.EqualFold(strings.TrimSpace(status), "failed") &&
		strings.EqualFold(displaypolicy.ToolTaskAction(input, displayOutput, meta), "wait") {
		return nil
	}
	text := toolResultDisplayText(name, input, displayOutput, meta, status, isErr)
	if strings.TrimSpace(text) == "" {
		text = toolResultStatusText(status, isErr)
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	item := session.EventToolContent{
		Type: "content",
		Text: text,
	}
	switch name {
	case "BASH", "SPAWN", "TASK":
		item.Type = "terminal"
		item.TerminalID = toolResultTerminalID(call, displayOutput, meta)
	}
	return []session.EventToolContent{item}
}

func toolResultDisplayOutput(name string, output map[string]any, meta map[string]any) map[string]any {
	out := maps.Clone(output)
	if out == nil {
		out = map[string]any{}
	}
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "WRITE", "PATCH":
		for _, key := range []string{
			"created",
			"previous_empty",
			"bytes_written",
			"line_count",
			"added_lines",
			"removed_lines",
			"revision",
			"hunk",
			"diff_hunks",
			"diff_truncated",
		} {
			if value, ok := runtimeToolMeta(meta)[key]; ok {
				out[key] = value
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func toolResultDisplayText(name string, input map[string]any, output map[string]any, meta map[string]any, status string, isErr bool) string {
	name = strings.ToUpper(strings.TrimSpace(name))
	switch name {
	case "READ":
		if summary := readResultSummary(input, output); summary != "" {
			return summary
		}
		return toolString(output["content"])
	case "LIST":
		return listResultSummary(input, output)
	case "GLOB":
		return globResultSummary(input, output, meta)
	case "SEARCH", "RG", "FIND":
		return searchResultSummary(input, output, meta)
	case "WRITE", "PATCH":
		if isErr || strings.EqualFold(status, "failed") {
			return firstNonEmpty(toolString(output["error"]), toolString(output["summary"]))
		}
		return mutationResultSummary(input, output)
	case "BASH":
		return terminalResultText(output, status, isErr)
	case "SPAWN":
		return spawnResultText(output, status, isErr)
	case "TASK":
		if toolStatusFinal(status, isErr) {
			if summary := displaypolicy.CleanSubagentFinalOutput(toolString(output["final_message"])); summary != "" {
				return summary
			}
		}
		return terminalResultText(output, status, isErr)
	case "REQUEST_PERMISSIONS":
		if isErr || !toolBool(output["approved"]) {
			return firstNonEmpty(toolString(output["error"]), toolString(output["review_text"]), "denied")
		}
		return "completed"
	default:
		return genericResultText(output, isErr)
	}
}

func readResultSummary(input map[string]any, output map[string]any) string {
	path := firstNonEmpty(toolPath(output), toolPath(input))
	if path == "" {
		return ""
	}
	start := toolInt(output["start_line"])
	end := toolInt(output["end_line"])
	if start <= 0 {
		if offset := toolInt(input["offset"]); offset >= 0 {
			start = offset + 1
		}
	}
	if end <= 0 {
		if limit := toolInt(input["limit"]); limit > 0 && start > 0 {
			end = start + limit - 1
		}
	}
	if start > 0 && end > 0 {
		return filepath.Base(path) + " " + strconv.Itoa(start) + "~" + strconv.Itoa(end)
	}
	return filepath.Base(path)
}

func listResultSummary(input map[string]any, output map[string]any) string {
	path := firstNonEmpty(toolPath(output), toolPath(input))
	count := toolInt(output["count"])
	if path == "" && count <= 0 {
		return ""
	}
	if count > 0 {
		return strings.TrimSpace(filepath.Base(path) + " " + pluralize(count, "entry"))
	}
	return filepath.Base(path)
}

func globResultSummary(input map[string]any, output map[string]any, meta map[string]any) string {
	pattern := firstNonEmpty(toolString(input["pattern"]), toolString(output["pattern"]), toolString(runtimeToolMeta(meta)["pattern"]))
	count := toolInt(output["count"])
	switch {
	case pattern != "" && count >= 0:
		return pattern + " " + pluralize(count, "match")
	case pattern != "":
		return pattern
	default:
		return ""
	}
}

func searchResultSummary(input map[string]any, output map[string]any, meta map[string]any) string {
	query := firstNonEmpty(toolString(output["query"]), toolString(input["query"]), toolString(input["pattern"]), toolString(runtimeToolMeta(meta)["query"]))
	count := toolInt(output["count"])
	files := toolInt(output["file_count"])
	if query == "" && count <= 0 {
		return ""
	}
	summary := ""
	if query != "" {
		summary = strconv.Quote(query)
	}
	if count >= 0 {
		summary = strings.TrimSpace(summary + " " + pluralize(count, "hit"))
	}
	if files > 0 {
		summary += " in " + pluralize(files, "file")
	}
	return summary
}

func mutationResultSummary(input map[string]any, output map[string]any) string {
	path := firstNonEmpty(toolPath(output), toolPath(input))
	if path == "" {
		return firstNonEmpty(toolString(output["summary"]), "completed")
	}
	header := filepath.Base(path)
	added := toolInt(output["added_lines"])
	removed := toolInt(output["removed_lines"])
	if added > 0 || removed > 0 {
		header += fmt.Sprintf(" +%d -%d", added, removed)
	}
	if diffLines := mutationDiffLines(output); len(diffLines) > 0 {
		return strings.Join(append([]string{header, "diff / hunk"}, diffLines...), "\n")
	}
	if hunk := strings.TrimSpace(toolString(output["hunk"])); hunk != "" {
		return strings.Join([]string{header, "diff / hunk", hunk}, "\n")
	}
	return header
}

func mutationDiffLines(output map[string]any) []string {
	raw, ok := output["diff_hunks"]
	if !ok || raw == nil {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var hunks []struct {
		Header string   `json:"header"`
		Lines  []string `json:"lines"`
	}
	if err := json.Unmarshal(data, &hunks); err != nil {
		return nil
	}
	lines := make([]string, 0, len(hunks)*4)
	for _, hunk := range hunks {
		if header := strings.TrimSpace(hunk.Header); header != "" {
			lines = append(lines, header)
		}
		lines = append(lines, hunk.Lines...)
	}
	if len(lines) == 0 {
		return nil
	}
	if toolBool(output["diff_truncated"]) {
		lines = append(lines, "@@ diff truncated @@")
	}
	return lines
}

func terminalResultText(output map[string]any, status string, isErr bool) string {
	if !toolStatusFinal(status, isErr) {
		if text := firstNonEmpty(
			toolString(output["latest_output"]),
			toolString(output["output_preview"]),
		); text != "" {
			return text
		}
		return ""
	}
	if text := toolString(output["result"]); text != "" {
		return text
	}
	if errText := toolString(output["error"]); errText != "" {
		return errText
	}
	return "(no output)"
}

func spawnResultText(output map[string]any, status string, isErr bool) string {
	if isErr || strings.EqualFold(status, "failed") {
		if stderr := strings.TrimSpace(toolString(output["stderr"])); stderr != "" {
			return stderr
		}
		if errText := strings.TrimSpace(toolString(output["error"])); errText != "" {
			return errText
		}
	}
	if toolStatusFinal(status, isErr) {
		return displaypolicy.CleanSubagentFinalOutput(firstNonEmpty(
			spawnDisplayText(toolString(output["final_message"])),
			spawnDisplayText(toolString(output["finalMessage"])),
			spawnDisplayText(toolString(output["result"])),
			spawnDisplayText(toolString(output["output"])),
			spawnDisplayText(toolString(output["text"])),
		))
	}
	return firstNonEmpty(
		spawnStreamText(toolString(output["text"])),
		spawnStreamText(toolString(output["stdout"])),
		spawnStreamText(toolString(output["output_preview"])),
		spawnStreamText(toolString(output["stderr"])),
	)
}

func spawnDisplayText(text string) string {
	return displaypolicy.SpawnDisplayTextCandidate(text)
}

func spawnStreamText(text string) string {
	if text == "" {
		return ""
	}
	candidate := strings.TrimLeft(text, " \t\r\n")
	if !strings.HasPrefix(candidate, "{") {
		return text
	}
	decoded, remainder, ok := displaypolicy.SplitLeadingJSONObject(candidate)
	if !ok || !displaypolicy.IsSpawnDisplayJSONObject(decoded) {
		return text
	}
	if strings.TrimSpace(remainder) == "" {
		return ""
	}
	return strings.TrimLeft(remainder, "\r\n")
}

func genericResultText(output map[string]any, isErr bool) string {
	if len(output) == 0 {
		return ""
	}
	if isErr {
		return firstNonEmpty(toolString(output["stderr"]), toolString(output["error"]), toolString(output["summary"]))
	}
	return firstNonEmpty(toolString(output["summary"]), toolString(output["result"]), toolString(output["text"]))
}

func toolResultStatusText(status string, isErr bool) string {
	if isErr || strings.EqualFold(strings.TrimSpace(status), "failed") {
		return "failed"
	}
	if toolStatusFinal(status, isErr) {
		return "completed"
	}
	return ""
}

func toolStatusFinal(status string, isErr bool) bool {
	if isErr {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "interrupted", "cancelled", "canceled", "terminated":
		return true
	default:
		return false
	}
}

func toolResultTerminalID(call model.ToolCall, output map[string]any, meta map[string]any) string {
	return firstNonEmpty(
		toolString(output["terminal_id"]),
		stringFromNestedMap(meta, "caelis", "runtime", "task", "terminal_id"),
		strings.TrimSpace(call.ID),
	)
}

func runtimeToolMeta(meta map[string]any) map[string]any {
	caelis, _ := meta["caelis"].(map[string]any)
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	toolMeta, _ := runtimeMeta["tool"].(map[string]any)
	return toolMeta
}

func toolPath(values map[string]any) string {
	return firstNonEmpty(toolString(values["path"]), toolString(values["target"]), toolString(values["source"]))
}

func toolString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func toolInt(value any) int {
	if intValue, ok := intValue(value); ok {
		return intValue
	}
	if text := toolString(value); text != "" {
		if parsed, err := strconv.Atoi(text); err == nil {
			return parsed
		}
	}
	return -1
}

func toolBool(value any) bool {
	typed, _ := value.(bool)
	return typed
}

func pluralize(count int, unit string) string {
	if count == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(count) + " " + unit + "s"
}

func (a *Agent) lookupTool(name string) (tool.Tool, bool) {
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

func toolResultMessage(call model.ToolCall, result tool.Result) model.Message {
	message, _ := toolResultMessageWithMeta(call, result)
	return message
}

func canonicalToolResult(result tool.Result) (tool.Result, map[string]any) {
	canonical, info := tool.TruncateResultWithInfo(result, tool.DefaultTruncationPolicy())
	return canonical, toolTruncationEventMeta(info)
}

func toolResultMessageWithMeta(call model.ToolCall, result tool.Result) (model.Message, map[string]any) {
	result, truncationMeta := canonicalToolResult(result)
	return toolResultMessageFromCanonical(call, result), truncationMeta
}

func toolResultMessageFromCanonical(call model.ToolCall, result tool.Result) model.Message {
	parts := model.CloneParts(result.Content)
	if len(parts) == 0 {
		parts = []model.Part{model.NewJSONPart(mustJSON(map[string]any{}))}
	}
	message := model.Message{
		Role: model.RoleTool,
		Parts: []model.Part{{
			Kind: model.PartKindToolResult,
			ToolResult: &model.ToolResultPart{
				ToolUseID: strings.TrimSpace(firstNonEmpty(result.ID, call.ID)),
				Name:      strings.TrimSpace(firstNonEmpty(result.Name, call.Name)),
				Content:   parts,
				IsError:   result.IsError,
			},
		}},
	}
	return message
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

func toolCallTitle(call model.ToolCall) string {
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
		if title := displaypolicy.SummarizeToolCallTitle(name, args); strings.TrimSpace(title) != "" {
			return title
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

func toolCallStatus(result tool.Result, rawOutput map[string]any) string {
	if state, _ := rawOutput["state"].(string); strings.TrimSpace(state) != "" {
		switch strings.TrimSpace(state) {
		case "running", "waiting_input", "waiting_approval":
			return strings.TrimSpace(state)
		case "failed", "interrupted", "cancelled", "canceled", "terminated":
			return strings.TrimSpace(state)
		}
	}
	if exitCode, ok := intValue(rawOutput["exit_code"]); ok && exitCode != 0 {
		return "failed"
	}
	if result.IsError {
		return "failed"
	}
	return "completed"
}

func intValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int8:
		return int(typed), true
	case int16:
		return int(typed), true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case uint:
		return int(typed), true
	case uint8:
		return int(typed), true
	case uint16:
		return int(typed), true
	case uint32:
		return int(typed), true
	case uint64:
		return int(typed), true
	case float32:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
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

func responseMeta(resp *model.Response) map[string]any {
	if resp == nil {
		return nil
	}
	usage := map[string]any{
		"prompt_tokens":       resp.Usage.PromptTokens,
		"cached_input_tokens": resp.Usage.CachedInputTokens,
		"completion_tokens":   resp.Usage.CompletionTokens,
		"reasoning_tokens":    resp.Usage.ReasoningTokens,
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

func toolTruncationEventMeta(info tool.TruncationInfo) map[string]any {
	truncation := tool.TruncationMetadata(info)
	if len(truncation) == 0 {
		return nil
	}
	return map[string]any{
		"caelis": map[string]any{
			"version": 1,
			"runtime": map[string]any{
				"tool": map[string]any{
					"truncation": truncation,
				},
			},
		},
	}
}

func toolEventPayload(call model.ToolCall, status string, rawInput map[string]any, rawOutput map[string]any, content []session.EventToolContent) *session.EventTool {
	payload := &session.EventTool{
		ID:      strings.TrimSpace(call.ID),
		Name:    strings.TrimSpace(call.Name),
		Kind:    toolKindForName(call.Name),
		Title:   toolCallTitle(call),
		Status:  strings.TrimSpace(status),
		Input:   maps.Clone(rawInput),
		Output:  maps.Clone(rawOutput),
		Content: cloneEventToolContent(content),
	}
	return payload
}

func cloneEventToolContent(in []session.EventToolContent) []session.EventToolContent {
	if len(in) == 0 {
		return nil
	}
	out := make([]session.EventToolContent, 0, len(in))
	for _, item := range in {
		var oldText *string
		if item.OldText != nil {
			value := *item.OldText
			oldText = &value
		}
		out = append(out, session.EventToolContent{
			Type:       strings.TrimSpace(item.Type),
			Text:       item.Text,
			TerminalID: strings.TrimSpace(item.TerminalID),
			Path:       strings.TrimSpace(item.Path),
			OldText:    oldText,
			NewText:    item.NewText,
		})
	}
	return out
}

func messagesFromContext(ctx agent.Context) []model.Message {
	if ctx == nil {
		return nil
	}
	activeSession := ctx.Session()
	events := make([]*session.Event, 0, ctx.Events().Len())
	for event := range ctx.Events().All() {
		events = append(events, event)
	}
	out := make([]model.Message, 0, len(events))
	for i := 0; i < len(events); {
		event := events[i]
		if event == nil || !session.IsMainInvocationVisibleEvent(event) {
			i++
			continue
		}
		event = eventWithParticipantContextMeta(event, activeSession)
		if session.EventTypeOf(event) == session.EventTypeToolCall {
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

func eventWithParticipantContextMeta(event *session.Event, activeSession session.Session) *session.Event {
	if event == nil || event.Scope == nil {
		return event
	}
	participantID := strings.TrimSpace(event.Scope.Participant.ID)
	if participantID == "" {
		return event
	}
	binding, ok := chatParticipantBinding(activeSession, participantID)
	if !ok {
		return event
	}
	agent := strings.TrimSpace(binding.AgentName)
	label := strings.TrimSpace(binding.Label)
	if agent == "" && label == "" {
		return event
	}
	if stringFromFlatMap(event.Meta, "agent") != "" &&
		stringFromFlatMap(event.Meta, "mention") != "" &&
		stringFromFlatMap(event.Meta, "handle") != "" {
		return event
	}
	cloned := session.CloneEvent(event)
	if cloned.Meta == nil {
		cloned.Meta = map[string]any{}
	}
	if agent != "" && stringFromFlatMap(cloned.Meta, "agent") == "" {
		cloned.Meta["agent"] = agent
	}
	if label != "" {
		if stringFromFlatMap(cloned.Meta, "mention") == "" {
			cloned.Meta["mention"] = label
		}
		if stringFromFlatMap(cloned.Meta, "handle") == "" {
			cloned.Meta["handle"] = strings.TrimPrefix(label, "@")
		}
	}
	return cloned
}

func chatParticipantBinding(activeSession session.Session, participantID string) (session.ParticipantBinding, bool) {
	participantID = strings.TrimSpace(participantID)
	if participantID == "" {
		return session.ParticipantBinding{}, false
	}
	for _, item := range activeSession.Participants {
		if strings.TrimSpace(item.ID) == participantID {
			return session.CloneParticipantBinding(item), true
		}
	}
	return session.ParticipantBinding{}, false
}

func toolCallMessageFromEventRun(events []*session.Event, start int) (model.Message, int, bool) {
	if start < 0 || start >= len(events) {
		return model.Message{}, start + 1, false
	}
	if first := events[start]; first != nil && first.Message != nil && len(first.Message.ToolCalls()) > 0 {
		known := map[string]struct{}{}
		for _, call := range first.Message.ToolCalls() {
			if id := strings.TrimSpace(call.ID); id != "" {
				known[id] = struct{}{}
			}
		}
		next := start + 1
		for next < len(events) {
			event := events[next]
			if event == nil || !session.IsMainInvocationVisibleEvent(event) || session.EventTypeOf(event) != session.EventTypeToolCall {
				break
			}
			call, ok := toolCallFromEventTool(event)
			if !ok {
				break
			}
			if _, exists := known[strings.TrimSpace(call.ID)]; !exists {
				break
			}
			next++
		}
		return model.CloneMessage(*first.Message), next, true
	}
	calls := make([]model.ToolCall, 0, 1)
	text := ""
	next := start
	for next < len(events) {
		event := events[next]
		if event == nil || !session.IsMainInvocationVisibleEvent(event) || session.EventTypeOf(event) != session.EventTypeToolCall {
			break
		}
		if text == "" {
			text = strings.TrimSpace(session.EventText(event))
		}
		call, ok := toolCallFromEventTool(event)
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
		return model.Message{}, start + 1, false
	}
	return model.MessageFromToolCalls(model.RoleAssistant, calls, text), next, true
}

func toolCallFromEventTool(event *session.Event) (model.ToolCall, bool) {
	if event == nil || event.Tool == nil {
		return model.ToolCall{}, false
	}
	call := model.ToolCall{
		ID:   strings.TrimSpace(event.Tool.ID),
		Name: toolNameFromEvent(event),
		Args: string(mustJSON(event.Tool.Input)),
	}
	if call.ID == "" || call.Name == "" {
		return model.ToolCall{}, false
	}
	return call, true
}

func normalizeToolCallHistory(messages []model.Message) []model.Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]model.Message, 0, len(messages))
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
		run := []model.Message{messages[i]}
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

func messageFromInvocationEvent(event *session.Event) (model.Message, bool) {
	if event == nil || !session.IsMainInvocationVisibleEvent(event) {
		return model.Message{}, false
	}
	if event.Scope == nil || strings.TrimSpace(event.Scope.Participant.ID) == "" {
		if event.Message != nil {
			return model.CloneMessage(*event.Message), true
		}
		return messageFromDurableEvent(event)
	}
	text := strings.TrimSpace(session.EventText(event))
	if text == "" {
		return model.Message{}, false
	}
	label := participantInvocationLabel(*event)
	switch session.EventTypeOf(event) {
	case session.EventTypeUser:
		return model.NewTextMessage(model.RoleUser, fmt.Sprintf("User to %s: %s", label, text)), true
	case session.EventTypeAssistant:
		if prefix := participantAssistantContextPrefix(*event); prefix != "" {
			return model.NewTextMessage(model.RoleAssistant, prefix+text), true
		}
		return model.NewTextMessage(model.RoleAssistant, fmt.Sprintf("Assistant(%s): %s", label, text)), true
	default:
		if event.Message != nil {
			return model.CloneMessage(*event.Message), true
		}
		return messageFromDurableEvent(event)
	}
}

func messageFromDurableEvent(event *session.Event) (model.Message, bool) {
	switch session.EventTypeOf(event) {
	case session.EventTypeUser:
		if text := strings.TrimSpace(session.EventText(event)); text != "" {
			return model.NewTextMessage(model.RoleUser, text), true
		}
	case session.EventTypeAssistant:
		if text := strings.TrimSpace(session.EventText(event)); text != "" {
			return model.NewTextMessage(model.RoleAssistant, text), true
		}
	case session.EventTypeToolCall:
		if event == nil || event.Tool == nil {
			return model.Message{}, false
		}
		call := model.ToolCall{
			ID:   strings.TrimSpace(event.Tool.ID),
			Name: toolNameFromEvent(event),
			Args: string(mustJSON(event.Tool.Input)),
		}
		if call.ID == "" || call.Name == "" {
			return model.Message{}, false
		}
		return model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{call}, ""), true
	case session.EventTypeToolResult:
		if event == nil || event.Tool == nil {
			return model.Message{}, false
		}
		name := toolNameFromEvent(event)
		if event.Tool.ID == "" || name == "" {
			return model.Message{}, false
		}
		message := model.Message{
			Role: model.RoleTool,
			Parts: []model.Part{model.NewToolResultJSONPart(
				strings.TrimSpace(event.Tool.ID),
				name,
				truncatedToolOutputMap(toolResultContextPayload(event.Tool)),
				strings.EqualFold(strings.TrimSpace(event.Tool.Status), "failed"),
			)},
		}
		return message, true
	}
	return model.Message{}, false
}

func truncatedToolOutputMap(values map[string]any) map[string]any {
	out, _ := tool.TruncateMap(values, tool.DefaultTruncationPolicy())
	return out
}

func toolResultContextPayload(toolPayload *session.EventTool) map[string]any {
	if toolPayload == nil {
		return map[string]any{}
	}
	if len(toolPayload.Output) > 0 {
		return maps.Clone(toolPayload.Output)
	}
	if text := eventToolContentText(toolPayload.Content); text != "" {
		return map[string]any{"result": text}
	}
	return map[string]any{}
}

func eventToolContentText(content []session.EventToolContent) string {
	if len(content) == 0 {
		return ""
	}
	parts := make([]string, 0, len(content))
	for _, item := range content {
		switch strings.TrimSpace(item.Type) {
		case "content", "terminal":
			if item.Text != "" {
				parts = append(parts, item.Text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func toolNameFromEvent(event *session.Event) string {
	if event == nil {
		return ""
	}
	if name := strings.TrimSpace(stringFromNestedMap(event.Meta, "caelis", "runtime", "tool", "name")); name != "" {
		return name
	}
	if event.Tool != nil {
		if name := strings.TrimSpace(event.Tool.Name); name != "" {
			return name
		}
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

func participantInvocationLabel(event session.Event) string {
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
	if id := strings.TrimSpace(event.Actor.ID); id != "" && event.Actor.Kind == session.ActorKindParticipant {
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

func participantAssistantContextPrefix(event session.Event) string {
	if event.Scope == nil {
		return ""
	}
	participant := event.Scope.Participant
	if strings.TrimSpace(participant.ID) == "" || participant.Role != session.ParticipantRoleSidecar {
		return ""
	}
	agent := stableAgentContextValue(participantAgentType(event))
	if agent == "" {
		agent = stableAgentContextValue(string(participant.Kind))
	}
	if agent == "" {
		agent = "agent"
	}
	handle := stableAgentHandleValue(participantHandle(event))
	if handle != "" {
		return fmt.Sprintf("[agent_source agent=%s handle=%s]\n", agent, handle)
	}
	return fmt.Sprintf("[agent_source agent=%s]\n", agent)
}

func participantAgentType(event session.Event) string {
	if agent := strings.TrimSpace(stringFromFlatMap(event.Meta, "agent")); agent != "" {
		return agent
	}
	if source := strings.ToLower(strings.TrimSpace(event.Scope.Source)); strings.HasPrefix(source, "slash_") {
		return strings.TrimPrefix(source, "slash_")
	}
	if event.Scope.Participant.Kind != "" {
		return string(event.Scope.Participant.Kind)
	}
	return ""
}

func participantHandle(event session.Event) string {
	for _, value := range []string{
		stringFromFlatMap(event.Meta, "mention"),
		stringFromFlatMap(event.Meta, "handle"),
		event.Actor.Name,
	} {
		if text := strings.TrimSpace(value); text != "" && !strings.EqualFold(text, "user") {
			return text
		}
	}
	if event.Actor.Kind == session.ActorKindParticipant {
		if id := strings.TrimSpace(event.Actor.ID); id != "" {
			return id
		}
	}
	if event.Scope != nil {
		return strings.TrimSpace(event.Scope.Participant.ID)
	}
	return ""
}

func stableAgentContextValue(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return strings.Join(fields, "_")
}

func stableAgentHandleValue(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	value = strings.Join(fields, "_")
	value = strings.TrimPrefix(value, "@")
	if value == "" {
		return ""
	}
	return "@" + value
}

func stringFromFlatMap(values map[string]any, key string) string {
	if len(values) == 0 {
		return ""
	}
	text, _ := values[key].(string)
	return strings.TrimSpace(text)
}

func instructionsFromContext(_ agent.Context, systemPrompt string) []model.Part {
	out := make([]model.Part, 0, 1)
	if strings.TrimSpace(systemPrompt) != "" {
		out = append(out, model.NewTextPart(strings.TrimSpace(systemPrompt)))
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
