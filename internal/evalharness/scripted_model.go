package evalharness

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// ScriptedModel consumes one scripted step for each provider request.
type ScriptedModel struct {
	name     string
	mu       sync.Mutex
	steps    []ScriptStep
	requests int
}

// ScriptStep describes the events and optional terminal error for one request.
type ScriptStep struct {
	Events []*model.StreamEvent
	Err    error
}

// NewScriptedModel snapshots the steps for a deterministic model client.
func NewScriptedModel(name string, steps ...ScriptStep) *ScriptedModel {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "scripted"
	}
	return &ScriptedModel{name: name, steps: cloneScriptSteps(steps)}
}

// TextStep returns a completed assistant text response.
func TextStep(text string) ScriptStep {
	return ScriptStep{Events: []*model.StreamEvent{model.StreamEventFromResponse(&model.Response{
		Message:      model.NewTextMessage(model.RoleAssistant, text),
		StepComplete: true,
		TurnComplete: true,
		Status:       model.ResponseStatusCompleted,
		FinishReason: model.FinishReasonStop,
	})}}
}

// Name returns the scripted model's configured name.
func (m *ScriptedModel) Name() string { return m.name }

// Generate yields a snapshot of the next configured step.
func (m *ScriptedModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.mu.Lock()
	if req != nil {
		m.requests++
	}
	index := m.requests - 1
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
		out.PartDelta = &part
	}
	if in.Response != nil {
		response := *in.Response
		response.Message = model.CloneMessage(in.Message)
		out.Response = &response
	}
	return &out
}
