package controlplane

import (
	"context"
	"errors"
	"iter"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestExecutionValidatorChecksModelCapabilities(t *testing.T) {
	t.Parallel()

	request := agent.RunRequest{
		Request: agent.ModelRequestOptions{
			Stream: boolPointer(true),
			Output: &model.OutputSpec{Mode: model.OutputModeSchema, JSONSchema: map[string]any{"type": "object"}},
		},
		AgentSpec: agent.AgentSpec{Tools: []tool.Tool{tool.NamedTool{Def: tool.Definition{Name: "probe"}}}},
	}
	validator := ExecutionValidator{}
	for _, tt := range []struct {
		name  string
		caps  model.Capabilities
		valid bool
	}{
		{name: "supported", caps: model.Capabilities{ToolCalls: true, StructuredOutput: true, Streaming: true}, valid: true},
		{name: "missing tools", caps: model.Capabilities{StructuredOutput: true, Streaming: true}},
		{name: "missing structured output", caps: model.Capabilities{ToolCalls: true, Streaming: true}},
		{name: "missing streaming", caps: model.Capabilities{ToolCalls: true, StructuredOutput: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := request
			req.AgentSpec.Model = executionModel{capabilities: tt.caps}
			err := validator.ValidateExecutionRequest(req)
			if tt.valid {
				if err != nil {
					t.Fatalf("ValidateExecutionRequest() error = %v", err)
				}
				return
			}
			var capabilityErr *model.CapabilityError
			if !errors.As(err, &capabilityErr) {
				t.Fatalf("ValidateExecutionRequest() error = %v, want model capability error", err)
			}
		})
	}
}

func TestExecutionValidatorRejectsMalformedActualToolSet(t *testing.T) {
	t.Parallel()

	validator := ExecutionValidator{}
	request := agent.RunRequest{AgentSpec: agent.AgentSpec{
		Model: executionModel{capabilities: model.Capabilities{ToolCalls: true}},
		Tools: []tool.Tool{
			tool.NamedTool{Def: tool.Definition{Name: "probe"}},
			tool.NamedTool{Def: tool.Definition{Name: " PROBE "}},
		},
	}}
	if err := validator.ValidateExecutionRequest(request); err == nil {
		t.Fatal("ValidateExecutionRequest() error = nil, want duplicate actual tool rejection")
	}
}

type executionModel struct {
	capabilities model.Capabilities
}

func (executionModel) Name() string { return "execution-model" }
func (executionModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(func(*model.StreamEvent, error) bool) {}
}
func (m executionModel) Capabilities() model.Capabilities { return m.capabilities }

func boolPointer(value bool) *bool { return &value }
