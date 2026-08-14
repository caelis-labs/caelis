package controlplane

import (
	"context"
	"errors"
	"iter"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestExecutionValidatorDerivesAndValidatesActualRequirements(t *testing.T) {
	t.Parallel()

	validator, err := NewExecutionValidator(ExecutionValidatorConfig{
		Sandbox: executionDescriptor{Descriptor: sandbox.Descriptor{
			Backend: sandbox.BackendHost,
			Capabilities: sandbox.CapabilitySet{
				FileSystem:    true,
				CommandExec:   true,
				AsyncSessions: true,
			},
		}},
	})
	if err != nil {
		t.Fatalf("NewExecutionValidator() error = %v", err)
	}
	request := agent.RunRequest{
		Request: agent.ModelRequestOptions{
			Stream: boolPointer(true),
			Output: &model.OutputSpec{Mode: model.OutputModeSchema, JSONSchema: map[string]any{"type": "object"}},
		},
		AgentSpec: agent.AgentSpec{
			Model: executionModel{capabilities: model.Capabilities{
				ToolCalls:        true,
				StructuredOutput: true,
				Streaming:        true,
			}},
			Tools: []tool.Tool{
				tool.NamedTool{Def: tool.Definition{
					Name: "READ",
					ExecutionRequirements: &tool.ExecutionRequirements{
						Sandbox: sandbox.CapabilitySet{FileSystem: true},
					},
				}},
				tool.NamedTool{Def: tool.Definition{
					Name: "RUN_COMMAND",
					ExecutionRequirements: &tool.ExecutionRequirements{
						Sandbox: sandbox.CapabilitySet{CommandExec: true, AsyncSessions: true},
					},
				}},
			},
		},
	}
	requirements, err := validator.ExecutionRequirements(request)
	if err != nil {
		t.Fatalf("ValidateExecutionRequest() error = %v", err)
	}
	if !requirements.Model.ToolCalls || !requirements.Model.StructuredOutput || !requirements.Model.Streaming {
		t.Fatalf("model requirements = %+v, want tool/structured/streaming", requirements.Model)
	}
	if !requirements.Sandbox.FileSystem || !requirements.Sandbox.CommandExec || !requirements.Sandbox.AsyncSessions {
		t.Fatalf("sandbox requirements = %+v, want filesystem, command exec, and async sessions", requirements.Sandbox)
	}
}

func TestExecutionValidatorFailsClosedBeforeUnsupportedExecution(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		model      model.Capabilities
		sandbox    sandbox.CapabilitySet
		want       any
		capability sandbox.Capability
	}{
		{
			name:    "model tool calls",
			sandbox: sandbox.CapabilitySet{CommandExec: true},
			want:    new(model.CapabilityError),
		},
		{
			name:       "sandbox command execution",
			model:      model.Capabilities{ToolCalls: true},
			want:       new(sandbox.CapabilityError),
			capability: sandbox.CapabilityCommandExec,
		},
		{
			name:       "sandbox async sessions",
			model:      model.Capabilities{ToolCalls: true},
			sandbox:    sandbox.CapabilitySet{CommandExec: true},
			want:       new(sandbox.CapabilityError),
			capability: sandbox.CapabilityAsyncSessions,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			validator, err := NewExecutionValidator(ExecutionValidatorConfig{
				Sandbox: executionDescriptor{Descriptor: sandbox.Descriptor{
					Backend:      sandbox.BackendHost,
					Capabilities: tt.sandbox,
				}},
			})
			if err != nil {
				t.Fatalf("NewExecutionValidator() error = %v", err)
			}
			err = validator.ValidateExecutionRequest(agent.RunRequest{AgentSpec: agent.AgentSpec{
				Model: executionModel{capabilities: tt.model},
				Tools: []tool.Tool{tool.NamedTool{Def: tool.Definition{
					Name: "RUN_COMMAND",
					ExecutionRequirements: &tool.ExecutionRequirements{
						Sandbox: sandbox.CapabilitySet{CommandExec: true, AsyncSessions: true},
					},
				}}},
			}})
			switch want := tt.want.(type) {
			case *model.CapabilityError:
				var got *model.CapabilityError
				if !errors.As(err, &got) {
					t.Fatalf("error = %v, want %T", err, want)
				}
			case *sandbox.CapabilityError:
				var got *sandbox.CapabilityError
				if !errors.As(err, &got) {
					t.Fatalf("error = %v, want %T", err, want)
				}
				if got.Capability != tt.capability {
					t.Fatalf("capability = %q, want %q", got.Capability, tt.capability)
				}
			}
		})
	}
}

func TestExecutionValidatorRejectsMalformedActualToolSet(t *testing.T) {
	t.Parallel()

	validator, err := NewExecutionValidator(ExecutionValidatorConfig{
		Sandbox: executionDescriptor{Descriptor: sandbox.Descriptor{Backend: sandbox.BackendHost}},
	})
	if err != nil {
		t.Fatalf("NewExecutionValidator() error = %v", err)
	}
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

type executionDescriptor struct {
	sandbox.Descriptor
}

func (d executionDescriptor) Describe() sandbox.Descriptor { return d.Descriptor }

type executionModel struct {
	capabilities model.Capabilities
}

func (executionModel) Name() string { return "execution-model" }
func (executionModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(func(*model.StreamEvent, error) bool) {}
}
func (m executionModel) Capabilities() model.Capabilities { return m.capabilities }

func boolPointer(value bool) *bool { return &value }
