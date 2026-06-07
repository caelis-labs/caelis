package llmagent

import (
	"fmt"
	"iter"

	"github.com/OnslaughtSnail/caelis/agent"
	"github.com/OnslaughtSnail/caelis/model"
	"github.com/OnslaughtSnail/caelis/session"
	"github.com/OnslaughtSnail/caelis/tool"
)

// Config holds configuration for creating an LLM agent.
type Config struct {
	Name         string
	Description  string
	ModelRef     model.Ref
	SystemPrompt string
	Tools        []string // tool names to include
	SubAgents    []agent.Agent
	RunConfig    *agent.RunConfig
}

// Agent implements agent.Agent backed by an LLM.
type Agent struct {
	cfg      Config
	modelRef model.Ref
	llm      model.LLM
	tools    []tool.Tool
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
		toolCtx:  a.toolCtx,
	}
	if req.LLM != nil {
		cp.llm = req.LLM
	}
	if req.Tools != nil {
		cp.tools = req.Tools
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
		for _, t := range a.tools {
			def := t.Definition()
			toolSpecs = append(toolSpecs, model.ToolSpec{
				Name:        def.Name,
				Description: def.Description,
				Schema:      toModelSchema(def.Schema),
			})
		}

		modelCalls := 0
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
				}
				if event.ReasoningDelta != "" {
					reasoningText += event.ReasoningDelta
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
					modelParts = append(modelParts, model.Part{Text: reasoningText})
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

			// Execute tool calls and emit results.
			for _, tc := range pendingCalls {
				if ctx.Ended() {
					return
				}
				result := a.executeTool(ctx, tc)
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
		}
	}
}

// pendingCall holds a tool call request collected from the model stream.
type pendingCall struct {
	callID string
	name   string
	args   map[string]any
}

// executeTool finds and runs a tool by name.
func (a *Agent) executeTool(_ agent.InvocationContext, tc pendingCall) tool.Result {
	for _, t := range a.tools {
		if t.Definition().Name == tc.name {
			call := tool.Call{
				CallID: tc.callID,
				Name:   tc.name,
				Args:   tc.args,
			}
			result, err := t.Run(a.toolCtx, call)
			if err != nil {
				return tool.Result{Output: err.Error(), IsError: true}
			}
			return result
		}
	}
	return tool.Result{Output: fmt.Sprintf("tool not found: %s", tc.name), IsError: true}
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
