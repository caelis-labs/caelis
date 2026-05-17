package chat

import (
	"context"
	"errors"
	"iter"
	"maps"
	"strings"

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
