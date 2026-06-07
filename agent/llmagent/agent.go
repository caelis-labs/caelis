package llmagent

import (
	"fmt"
	"iter"
	"strings"
	"sync"

	"github.com/OnslaughtSnail/caelis/agent"
	"github.com/OnslaughtSnail/caelis/model"
	"github.com/OnslaughtSnail/caelis/session"
	"github.com/OnslaughtSnail/caelis/tool"
)

// Config holds configuration for creating an LLM agent.
type Config struct {
	Name        string
	Description string
	ModelRef    model.Ref
	Tools       []string // tool names to include
	SubAgents   []agent.Agent
	RunConfig   *agent.RunConfig
}

// Agent implements agent.Agent backed by an LLM.
type Agent struct {
	cfg      Config
	modelRef model.Ref
	llm      model.LLM
	tools    []tool.Tool
	catalog  tool.Registry
	executor tool.Executor
	toolCtx  tool.Context
}

// New creates a new LLM agent.
func New(cfg Config) *Agent {
	return &Agent{
		cfg:      cfg,
		modelRef: cfg.ModelRef,
	}
}

// Name returns the agent name.
func (a *Agent) Name() string { return a.cfg.Name }

// Description returns the agent description.
func (a *Agent) Description() string { return a.cfg.Description }

// SubAgents returns child agents.
func (a *Agent) SubAgents() []agent.Agent { return a.cfg.SubAgents }

// FindAgent returns a child agent by name.
func (a *Agent) FindAgent(name string) agent.Agent {
	for _, sa := range a.cfg.SubAgents {
		if sa.Name() == name {
			return sa
		}
	}
	return nil
}

// ModelRef returns the model reference for this agent.
func (a *Agent) ModelRef() model.Ref { return a.modelRef }

// ToolNames returns the configured tool names.
func (a *Agent) ToolNames() []string { return a.cfg.Tools }

// Prepare implements agent.Prepareable. Returns a new agent instance
// with the given dependencies wired. The original agent is not mutated.
func (a *Agent) Prepare(req agent.PrepareRequest) agent.Agent {
	cp := &Agent{
		cfg:      a.cfg,
		modelRef: a.modelRef,
		llm:      a.llm,
		tools:    a.tools,
		catalog:  a.catalog,
		executor: a.executor,
		toolCtx:  a.toolCtx,
	}
	if req.LLM != nil {
		cp.llm = req.LLM
	}
	if req.Tools != nil {
		cp.tools = req.Tools
	}
	if req.ToolCatalog != nil {
		cp.catalog = req.ToolCatalog
	}
	if req.ToolExecutor != nil {
		cp.executor = req.ToolExecutor
	}
	if req.ToolContext != nil {
		cp.toolCtx = req.ToolContext
	}
	return cp
}

// Run executes the agent loop: model call → tool execution → model call.
func (a *Agent) Run(ctx agent.InvocationContext) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) {
		if a.llm == nil {
			yield(session.Event{}, fmt.Errorf("llmagent: LLM not set"))
			return
		}

		runCfg := ctx.RunConfig()
		if runCfg == nil {
			runCfg = agent.DefaultRunConfig()
		}

		// Build initial messages from prior replay + current user input.
		// This is the critical fix: prior messages from durable replay
		// are prepended before the current user message.
		messages := ctx.PriorMessages()
		messages = append(messages, model.Message{
			Role:    model.RoleUser,
			Content: []model.Part{{Text: ctx.UserMessage().TextContent()}},
		})

		// Build tool specs from resolved tools.
		var toolSpecs []model.ToolSpec
		specTools := a.tools
		if a.catalog != nil {
			listed, err := a.catalog.List(ctx)
			if err != nil {
				yield(session.Event{}, fmt.Errorf("llmagent: list tool catalog: %w", err))
				return
			}
			specTools = listed
		}
		for _, t := range specTools {
			def := t.Definition()
			toolSpecs = append(toolSpecs, model.ToolSpec{
				Name:        def.Name,
				Description: def.Description,
				Schema:      toModelSchema(def.Schema),
			})
		}

		modelCalls := 0
		toolCalls := 0
		for {
			if ctx.Ended() {
				return
			}
			modelCalls++
			if runCfg.MaxModelCalls > 0 && modelCalls > runCfg.MaxModelCalls {
				yield(session.Event{}, fmt.Errorf("llmagent: max model calls (%d) exceeded", runCfg.MaxModelCalls))
				return
			}

			req := model.Request{
				Messages: messages,
				Tools:    toolSpecs,
			}

			var (
				assistantText string
				reasoningText string
				pendingCalls  []pendingCall
				responseErr   error
			)

			for event, err := range a.llm.Generate(ctx, req) {
				if err != nil {
					responseErr = err
					break
				}
				if event.TextDelta != "" {
					assistantText += event.TextDelta
					if !yield(session.Event{
						Kind:       session.EventKindAssistant,
						Visibility: session.VisibilityUIOnly,
						AssistantPayload: &session.AssistantPayload{Parts: []session.EventPart{{
							Kind: session.PartKindText,
							Text: event.TextDelta,
						}}},
					}, nil) {
						return
					}
				}
				if event.ReasoningDelta != "" {
					reasoningText += event.ReasoningDelta
					if !yield(session.Event{
						Kind:       session.EventKindAssistant,
						Visibility: session.VisibilityUIOnly,
						AssistantPayload: &session.AssistantPayload{Parts: []session.EventPart{{
							Kind: session.PartKindReasoning,
							Text: event.ReasoningDelta,
						}}},
					}, nil) {
						return
					}
				}
				if event.ToolCall != nil {
					pendingCalls = append(pendingCalls, pendingCall{
						callID: event.ToolCall.CallID,
						name:   event.ToolCall.Name,
						args:   event.ToolCall.Args,
					})
				}
			}

			if responseErr != nil {
				yield(session.Event{}, fmt.Errorf("llmagent: model error: %w", responseErr))
				return
			}
			pendingCalls = canonicalizePendingCalls(pendingCalls)

			// Emit assistant text event if present.
			if assistantText != "" || reasoningText != "" {
				var parts []session.EventPart
				if reasoningText != "" {
					parts = append(parts, session.EventPart{
						Kind: session.PartKindReasoning,
						Text: reasoningText,
					})
				}
				if assistantText != "" {
					parts = append(parts, session.EventPart{
						Kind: session.PartKindText,
						Text: assistantText,
					})
				}
				evt := session.Event{
					Kind:             session.EventKindAssistant,
					Visibility:       session.VisibilityCanonical,
					AssistantPayload: &session.AssistantPayload{Parts: parts},
				}
				if !yield(evt, nil) {
					return
				}
				var modelParts []model.Part
				if reasoningText != "" {
					modelParts = append(modelParts, model.Part{
						Reasoning: &model.Reasoning{
							Text:       reasoningText,
							Visibility: model.ReasoningVisibilityVisible,
						},
					})
				}
				if assistantText != "" {
					modelParts = append(modelParts, model.Part{Text: assistantText})
				}
				messages = append(messages, model.Message{
					Role:    model.RoleAssistant,
					Content: modelParts,
				})
			}

			// Emit tool_call events.
			for _, tc := range pendingCalls {
				tcEvt := session.Event{
					Kind:       session.EventKindToolCall,
					Visibility: session.VisibilityCanonical,
					ToolCallPayload: &session.ToolCallPayload{
						CallID: tc.callID,
						Name:   tc.name,
						Status: "pending",
						Args:   tc.args,
					},
				}
				if !yield(tcEvt, nil) {
					return
				}
				messages = append(messages, model.Message{
					Role: model.RoleAssistant,
					Content: []model.Part{
						{
							ToolUse: &model.ToolUse{
								CallID: tc.callID,
								Name:   tc.name,
								Args:   tc.args,
							},
						},
					},
				})
			}

			if len(pendingCalls) == 0 {
				return
			}

			executableCalls := pendingCalls
			toolLimitExceeded := false
			if runCfg.MaxToolCalls > 0 {
				remaining := runCfg.MaxToolCalls - toolCalls
				if remaining <= 0 {
					yield(session.Event{}, fmt.Errorf("llmagent: max tool calls (%d) exceeded", runCfg.MaxToolCalls))
					return
				}
				if len(executableCalls) > remaining {
					executableCalls = executableCalls[:remaining]
					toolLimitExceeded = true
				}
			}
			toolCalls += len(executableCalls)

			// Execute tool calls concurrently, then emit results in the model's
			// original tool-call order so durable replay stays deterministic.
			results := make([]tool.Result, len(executableCalls))
			var wg sync.WaitGroup
			for i, tc := range executableCalls {
				if ctx.Ended() {
					return
				}
				wg.Add(1)
				go func(i int, tc pendingCall) {
					defer wg.Done()
					results[i] = a.executeTool(ctx, tc)
				}(i, tc)
			}
			wg.Wait()

			for i, tc := range executableCalls {
				result := results[i]
				content := []session.EventPart{
					{Kind: session.PartKindText, Text: result.Output},
				}
				resultEvt := session.Event{
					Kind:       session.EventKindToolResult,
					Visibility: session.VisibilityCanonical,
					ToolResultPayload: &session.ToolResultPayload{
						CallID:     tc.callID,
						Name:       tc.name,
						Status:     "completed",
						IsError:    result.IsError,
						Content:    content,
						Truncation: truncMeta(result),
					},
				}
				if !yield(resultEvt, nil) {
					return
				}
				messages = append(messages, model.Message{
					Role: model.RoleTool,
					Content: []model.Part{
						{
							ToolResult: &model.ToolResult{
								CallID:  tc.callID,
								Content: result.Output,
								IsError: result.IsError,
							},
						},
					},
				})
			}
			if toolLimitExceeded {
				yield(session.Event{}, fmt.Errorf("llmagent: max tool calls (%d) exceeded", runCfg.MaxToolCalls))
				return
			}
		}
	}
}

// pendingCall holds a tool call request collected from the model stream.
type pendingCall struct {
	callID        string
	name          string
	args          map[string]any
	invalidReason string
}

const invalidToolCallName = "INVALID_TOOL_CALL"

func canonicalizePendingCalls(calls []pendingCall) []pendingCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]pendingCall, len(calls))
	seen := make(map[string]bool, len(calls))
	for i, call := range calls {
		call.callID = strings.TrimSpace(call.callID)
		call.name = strings.TrimSpace(call.name)
		if call.callID == "" || seen[call.callID] {
			call.callID = nextToolCallID(seen, i+1)
		}
		seen[call.callID] = true
		if call.name == "" {
			call.name = invalidToolCallName
			call.invalidReason = "missing tool name"
			call.args = cloneArgs(call.args)
			if call.args == nil {
				call.args = map[string]any{}
			}
			call.args["error"] = call.invalidReason
		}
		out[i] = call
	}
	return out
}

func nextToolCallID(seen map[string]bool, start int) string {
	for i := start; ; i++ {
		id := fmt.Sprintf("tool-call-%d", i)
		if !seen[id] {
			return id
		}
	}
}

func cloneArgs(args map[string]any) map[string]any {
	if args == nil {
		return nil
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	return out
}

// executeTool finds and runs a tool by name.
func (a *Agent) executeTool(ctx agent.InvocationContext, tc pendingCall) tool.Result {
	if tc.invalidReason != "" {
		return tool.Result{Output: "invalid model tool call: " + tc.invalidReason, IsError: true}
	}
	if a.executor == nil {
		return tool.Result{Output: fmt.Sprintf("tool executor not configured: %s", tc.name), IsError: true}
	}
	call := tool.Call{
		CallID: tc.callID,
		Name:   tc.name,
		Args:   tc.args,
	}
	result, err := a.executor.Execute(ctx, call)
	if err != nil {
		return tool.Result{Output: err.Error(), IsError: true}
	}
	return result
}

func truncMeta(r tool.Result) *session.TruncationMeta {
	if !r.Truncated {
		return nil
	}
	return &session.TruncationMeta{Strategy: "unknown"}
}

// toModelSchema converts tool.Schema to model.Schema.
func toModelSchema(s tool.Schema) model.Schema {
	props := make(map[string]model.Schema, len(s.Properties))
	for k, v := range s.Properties {
		props[k] = toModelSchema(v)
	}
	var items *model.Schema
	if s.Items != nil {
		ms := toModelSchema(*s.Items)
		items = &ms
	}
	return model.Schema{
		Type:        s.Type,
		Properties:  props,
		Required:    s.Required,
		Items:       items,
		Enum:        s.Enum,
		Format:      s.Format,
		Description: s.Description,
	}
}

// Compile-time interface checks.
var (
	_ agent.Agent       = (*Agent)(nil)
	_ agent.Prepareable = (*Agent)(nil)
)
