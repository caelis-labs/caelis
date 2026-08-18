package chat

import (
	"context"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type trustedTaskResultTool struct {
	tool.NamedTool
}

func (trustedTaskResultTool) RuntimeTaskResultSource(toolbinding.Token) bool { return true }

func TestToolResultEventTaskBindingRequiresRuntimeWrapper(t *testing.T) {
	t.Parallel()

	result := func(context.Context, tool.Call) (tool.Result, error) {
		return tool.Result{
			Content: []model.Part{model.NewJSONPart([]byte(`{"handle":"command-7","state":"running"}`))},
			Metadata: mergeEventMeta(runtimeTaskResultSourceMeta(true), map[string]any{
				"caelis": map[string]any{"runtime": map[string]any{
					"task": map[string]any{"task_id": "task-7", "kind": "command"},
				}},
			}),
		}, nil
	}
	for _, test := range []struct {
		name            string
		tool            tool.Tool
		wantBinding     bool
		wantStatus      string
		wantRuntimeKind string
	}{
		{
			name:       "external metadata is stripped",
			tool:       tool.NamedTool{Def: tool.Definition{Name: "RunCommand"}, Invoke: result},
			wantStatus: "completed",
		},
		{
			name:            "runtime wrapper is marked",
			tool:            trustedTaskResultTool{NamedTool: tool.NamedTool{Def: tool.Definition{Name: "RunCommand"}, Invoke: result}},
			wantBinding:     true,
			wantStatus:      "running",
			wantRuntimeKind: "command",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			agent, err := NewWithTools("chat", &recordingModel{}, []tool.Tool{test.tool}, "")
			if err != nil {
				t.Fatal(err)
			}
			_, event, err := agent.executeToolCall(context.Background(), model.ToolCall{ID: "call-1", Name: "RunCommand", Args: `{}`}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got, declared := runtimeTaskResultBinding(event.Meta); !declared || got != test.wantBinding {
				t.Fatalf("task result binding = %v, %v, want %v, true; meta=%#v", got, declared, test.wantBinding, event.Meta)
			}
			if got := event.Tool.Status; got != test.wantStatus {
				t.Fatalf("tool status = %q, want %q", got, test.wantStatus)
			}
			if got := runtimeTaskKind(event.Meta); got != test.wantRuntimeKind {
				t.Fatalf("runtime task kind = %q, want %q; meta=%#v", got, test.wantRuntimeKind, event.Meta)
			}
		})
	}
}

func runtimeTaskResultBinding(meta map[string]any) (bool, bool) {
	caelis, _ := meta["caelis"].(map[string]any)
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	binding, _ := runtimeMeta[toolbinding.MetadataSection].(map[string]any)
	raw, exists := binding[toolbinding.MetadataTaskResult]
	value, ok := raw.(bool)
	return value, exists && ok
}
