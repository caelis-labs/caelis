package runtime

import (
	"context"
	"errors"
	"iter"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestAgentSpecCapabilityValidationRejectsUndeclaredRequestedFeatures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tools    []tool.Tool
		output   *model.OutputSpec
		stream   bool
		declared model.Capabilities
		want     model.Capability
	}{
		{name: "tool calls", tools: []tool.Tool{tool.NamedTool{Def: tool.Definition{Name: "probe"}}}, want: model.CapabilityToolCalls},
		{name: "structured output", output: &model.OutputSpec{Mode: model.OutputModeSchema}, want: model.CapabilityStructuredOutput},
		{name: "stream", stream: true, want: model.CapabilityStreaming},
		{name: "parallel tool", tools: []tool.Tool{tool.NamedTool{Def: tool.Definition{Name: "probe", Capabilities: tool.Capabilities{ParallelSafe: true}}}}, want: model.CapabilityToolCalls},
		{name: "reasoning continuation", declared: model.Capabilities{ReasoningContinuation: true}, want: model.CapabilityReasoningContinuation},
		{name: "hosted tool", declared: model.Capabilities{HostedTools: true}, want: model.CapabilityHostedTools},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateAgentSpecCapabilities(limitTestModelWithoutCapabilities{}, tt.tools, tt.output, tt.stream, tt.declared)
			var capabilityErr *model.CapabilityError
			if !errors.As(err, &capabilityErr) {
				t.Fatalf("error = %v, want *model.CapabilityError", err)
			}
			if capabilityErr.Capability != tt.want {
				t.Fatalf("capability = %q, want %q", capabilityErr.Capability, tt.want)
			}
		})
	}
}

func TestRuntimeRejectsMissingCapabilitiesBeforeDurableRunMutation(t *testing.T) {
	t.Parallel()

	sessions, active := newTestSessionService(t, "capability-preflight")
	runtime, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: active.SessionRef,
		Input:      "must not persist",
		Request:    agent.ModelRequestOptions{Stream: boolPtr(true)},
		AgentSpec:  agent.AgentSpec{Name: "undeclared", Model: limitTestModelWithoutCapabilities{}},
	})
	var capabilityErr *model.CapabilityError
	if !errors.As(err, &capabilityErr) || capabilityErr.Capability != model.CapabilityStreaming {
		t.Fatalf("Run() error = %v, want streaming CapabilityError", err)
	}
	events, err := sessions.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %#v, want no mutation after failed assembly", events)
	}
}

type limitTestModelWithoutCapabilities struct{}

func (limitTestModelWithoutCapabilities) Name() string { return "undeclared" }

func (limitTestModelWithoutCapabilities) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(func(*model.StreamEvent, error) bool) {}
}
