package chat

import (
	"context"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"slices"
	"testing"
)

type trustedTaskResultTool struct {
	tool.NamedTool
}

func (trustedTaskResultTool) RuntimeTaskResultSource(toolbinding.Token) bool { return true }

func (t trustedTaskResultTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	toolbinding.Observed(ctx).TaskEvent = map[string]any{"caelis": map[string]any{"runtime": map[string]any{"task": map[string]any{"task_id": "task-7", "kind": "command", "state": "running"}}}}
	return t.NamedTool.Call(ctx, call)
}
func TestToolResultEventTaskBindingRequiresRuntimeWrapper(t *testing.T) {
	t.Parallel()

	result := func(context.Context, tool.Call) (tool.Result, error) {
		return tool.Result{
			Content: []tool.ContentBlock{tool.StructuredContent{Value: []byte(`{"handle":"command-7","state":"in_progress"}`)}},
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
			name:       "external content cannot declare task state",
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
			_, event, err := agent.executeToolCallAdmitted(context.Background(), model.ToolCall{ID: "call-1", Name: "RunCommand", Args: `{}`}, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got, declared := runtimeTaskResultBinding(event.Meta); !declared || got != test.wantBinding {
				t.Fatalf("task result binding = %v, %v, want %v, true; meta=%#v", got, declared, test.wantBinding, event.Meta)
			}
			if got := event.Tool.Status; got != test.wantStatus {
				t.Fatalf("tool status = %q, want %q", got, test.wantStatus)
			}
			caelis, _ := event.Meta["caelis"].(map[string]any)
			runtimeMeta, _ := caelis["runtime"].(map[string]any)
			taskMeta, _ := runtimeMeta["task"].(map[string]any)
			if got, _ := taskMeta["kind"].(string); got != test.wantRuntimeKind {
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

func TestToolVisibilityKeepsOnlyDirectTools(t *testing.T) {
	t.Parallel()

	const searchName = mcp.SearchToolName
	search := tool.NamedTool{Def: tool.Definition{
		Name:     searchName,
		Metadata: map[string]any{mcp.MetadataKind: searchName},
	}}
	visible := newToolVisibility([]tool.Tool{
		tool.NamedTool{Def: tool.Definition{Name: "Read"}},
		nil,
		tool.NamedTool{},
		search,
		tool.NamedTool{Def: tool.Definition{Name: "calendar_create_event", Metadata: map[string]any{mcp.MetadataKind: mcp.KindTool}}},
	})
	var names []string
	for _, spec := range visible.modelSpecs() {
		names = append(names, spec.Function.Name)
	}
	if want := []string{"Read"}; !slices.Equal(names, want) {
		t.Fatalf("model tool names = %v, want %v", names, want)
	}
	for _, name := range names {
		if got, ok := visible.lookupTool(name); !ok || got.Definition().Name != name {
			t.Errorf("lookupTool(%q) = %v, %v; want matching tool", name, got, ok)
		}
	}
	for _, name := range []string{"", searchName, "calendar_create_event"} {
		if _, ok := visible.lookupTool(name); ok {
			t.Errorf("lookupTool(%q) succeeded for a hidden tool", name)
		}
	}

	search.Def.Metadata = nil
	visible = newToolVisibility([]tool.Tool{search})
	if got := visible.modelSpecs(); len(got) != 1 || got[0].Function.Name != searchName {
		t.Errorf("unmarked search model specs = %v, want one function named %q", got, searchName)
	}
	if _, ok := visible.lookupTool(searchName); !ok {
		t.Errorf("lookupTool rejected %q without search metadata", searchName)
	}
}
