package evalharness

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"maps"
	"strings"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// ScriptedModel is a deterministic model.Client for regression scenarios. Each
// Generate call consumes one scripted step and records the provider-visible
// request that reached the model boundary.
type ScriptedModel struct {
	name     string
	mu       sync.Mutex
	steps    []ScriptStep
	requests []model.Request
}

type ScriptStep struct {
	Events []*model.StreamEvent
	Err    error
}

func NewScriptedModel(name string, steps ...ScriptStep) *ScriptedModel {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "scripted"
	}
	return &ScriptedModel{name: name, steps: cloneScriptSteps(steps)}
}

func TextStep(text string) ScriptStep {
	return ResponseStep(&model.Response{
		Message:      model.NewTextMessage(model.RoleAssistant, text),
		StepComplete: true,
		TurnComplete: true,
		Status:       model.ResponseStatusCompleted,
		FinishReason: model.FinishReasonStop,
	})
}

func ToolCallStep(text string, calls ...model.ToolCall) ScriptStep {
	return ResponseStep(&model.Response{
		Message:      model.MessageFromToolCalls(model.RoleAssistant, calls, text),
		StepComplete: true,
		TurnComplete: true,
		Status:       model.ResponseStatusCompleted,
		FinishReason: model.FinishReasonToolCalls,
	})
}

func AssistantPartsStep(text string, reasoning string, calls ...model.ToolCall) ScriptStep {
	finish := model.FinishReasonStop
	if len(calls) > 0 {
		finish = model.FinishReasonToolCalls
	}
	return ResponseStep(&model.Response{
		Message:      model.MessageFromAssistantParts(text, reasoning, calls),
		StepComplete: true,
		TurnComplete: true,
		Status:       model.ResponseStatusCompleted,
		FinishReason: finish,
	})
}

func ResponseStep(resp *model.Response) ScriptStep {
	return ScriptStep{Events: []*model.StreamEvent{model.StreamEventFromResponse(resp)}}
}

func (m *ScriptedModel) Name() string { return m.name }

func (m *ScriptedModel) Requests() []model.Request {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneRequests(m.requests)
}

func (m *ScriptedModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.mu.Lock()
	if req != nil {
		m.requests = append(m.requests, *model.CloneRequest(req))
	}
	index := len(m.requests) - 1
	step := ScriptStep{Err: fmt.Errorf("evalharness: no scripted model step for request %d", index+1)}
	if index >= 0 && index < len(m.steps) {
		step = cloneScriptStep(m.steps[index])
	}
	m.mu.Unlock()

	return func(yield func(*model.StreamEvent, error) bool) {
		for _, event := range step.Events {
			if !yield(cloneStreamEvent(event), nil) {
				return
			}
		}
		if step.Err != nil {
			yield(nil, step.Err)
		}
	}
}

type ChatScenario struct {
	Name         string
	SessionID    string
	Prompt       string
	SystemPrompt string
	Model        *ScriptedModel
	Tools        []tool.Tool
	Events       []*session.Event
}

type ChatRun struct {
	Events   []*session.Event
	Requests []model.Request
}

func RunChatScenario(ctx context.Context, scenario ChatScenario) (ChatRun, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	scenario.Name = strings.TrimSpace(scenario.Name)
	if scenario.Name == "" {
		scenario.Name = "chat-regression"
	}
	if scenario.Model == nil {
		return ChatRun{}, fmt.Errorf("evalharness: scripted model is required")
	}
	agentImpl, err := chat.NewWithTools(scenario.Name, scenario.Model, scenario.Tools, scenario.SystemPrompt)
	if err != nil {
		return ChatRun{}, err
	}
	sessionID := strings.TrimSpace(scenario.SessionID)
	if sessionID == "" {
		sessionID = scenario.Name
	}
	events := append([]*session.Event(nil), scenario.Events...)
	if strings.TrimSpace(scenario.Prompt) != "" {
		events = append(events, &session.Event{
			Type:    session.EventTypeUser,
			Text:    strings.TrimSpace(scenario.Prompt),
			Message: ptrMessage(model.NewTextMessage(model.RoleUser, strings.TrimSpace(scenario.Prompt))),
		})
	}
	agentCtx := agent.NewContext(agent.ContextSpec{
		Context: ctx,
		Session: session.Session{SessionRef: session.SessionRef{SessionID: sessionID}},
		Events:  events,
	})
	var out []*session.Event
	for event, runErr := range agentImpl.Run(agentCtx) {
		if runErr != nil {
			return ChatRun{}, runErr
		}
		out = append(out, cloneSessionEvent(event))
	}
	return ChatRun{Events: out, Requests: scenario.Model.Requests()}, nil
}

func EchoTool(name string) tool.Tool {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "ECHO"
	}
	return tool.NamedTool{
		Def: tool.Definition{
			Name:        name,
			Description: "echo input value",
			InputSchema: map[string]any{"type": "object"},
		},
		Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			var payload map[string]any
			if len(call.Input) > 0 {
				if err := json.Unmarshal(call.Input, &payload); err != nil {
					return tool.Result{}, err
				}
			}
			return tool.Result{
				ID:   call.ID,
				Name: call.Name,
				Content: []model.Part{
					model.NewJSONPart(MustJSON(payload)),
				},
			}, nil
		},
	}
}

func MustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

func RequestMessagesJSON(req model.Request) string {
	raw, _ := json.Marshal(model.CloneMessages(req.Messages))
	return string(raw)
}

func StableJSON(value any) string {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func ptrMessage(message model.Message) *model.Message {
	cp := model.CloneMessage(message)
	return &cp
}

func cloneScriptSteps(in []ScriptStep) []ScriptStep {
	if len(in) == 0 {
		return nil
	}
	out := make([]ScriptStep, 0, len(in))
	for _, step := range in {
		out = append(out, cloneScriptStep(step))
	}
	return out
}

func cloneScriptStep(in ScriptStep) ScriptStep {
	out := ScriptStep{Err: in.Err}
	if len(in.Events) > 0 {
		out.Events = make([]*model.StreamEvent, 0, len(in.Events))
		for _, event := range in.Events {
			out.Events = append(out.Events, cloneStreamEvent(event))
		}
	}
	return out
}

func cloneStreamEvent(in *model.StreamEvent) *model.StreamEvent {
	if in == nil {
		return nil
	}
	out := *in
	if in.PartDelta != nil {
		part := *in.PartDelta
		part.ProviderDetails = maps.Clone(in.PartDelta.ProviderDetails)
		if in.PartDelta.Replay != nil {
			replay := *in.PartDelta.Replay
			part.Replay = &replay
		}
		out.PartDelta = &part
	}
	if in.Message != nil {
		message := model.CloneMessage(*in.Message)
		out.Message = &message
	}
	if in.Response != nil {
		response := *in.Response
		response.Message = model.CloneMessage(in.Response.Message)
		out.Response = &response
	}
	if len(in.RawProviderEvent) > 0 {
		out.RawProviderEvent = append([]byte(nil), in.RawProviderEvent...)
	}
	return &out
}

func cloneRequests(in []model.Request) []model.Request {
	if len(in) == 0 {
		return nil
	}
	out := make([]model.Request, 0, len(in))
	for i := range in {
		out = append(out, *model.CloneRequest(&in[i]))
	}
	return out
}
