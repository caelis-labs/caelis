package tool

import (
	"context"
	"encoding/json"
	"testing"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
)

func TestDefinitionsAndModelSpecsCloneStableToolContracts(t *testing.T) {
	t.Parallel()

	tools := []Tool{
		NamedTool{
			Def: Definition{
				Name:        "inspect",
				Description: "Inspect session state",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
					},
				},
			},
		},
	}

	defs := Definitions(tools)
	if got, want := len(defs), 1; got != want {
		t.Fatalf("len(defs) = %d, want %d", got, want)
	}
	if got := defs[0].Name; got != "inspect" {
		t.Fatalf("defs[0].Name = %q, want %q", got, "inspect")
	}
	specs := ModelSpecs(tools)
	if got, want := len(specs), 1; got != want {
		t.Fatalf("len(specs) = %d, want %d", got, want)
	}
	if specs[0].Function == nil {
		t.Fatal("specs[0].Function = nil, want function tool spec")
	}
	if got := specs[0].Function.Name; got != "inspect" {
		t.Fatalf("specs[0].Name = %q, want %q", got, "inspect")
	}
	if got := specs[0].Kind; got != sdkmodel.ToolSpecKindFunction {
		t.Fatalf("specs[0].Kind = %q, want %q", got, sdkmodel.ToolSpecKindFunction)
	}

	defs[0].InputSchema["type"] = "array"
	specs[0].Function.Parameters["type"] = "array"

	clone := Definitions(tools)
	if got := clone[0].InputSchema["type"]; got != "object" {
		t.Fatalf("clone[0].InputSchema[type] = %v, want object", got)
	}
}

func TestNamedToolClonesCallAndResult(t *testing.T) {
	t.Parallel()

	tool := NamedTool{
		Def: Definition{Name: "echo"},
		Invoke: func(_ context.Context, call Call) (Result, error) {
			if got := string(call.Input); got != `{"value":"ok"}` {
				t.Fatalf("call.Input = %s, want %s", got, `{"value":"ok"}`)
			}
			call.Metadata["mutated"] = true
			return Result{
				Name: "echo",
				Content: []sdkmodel.Part{
					sdkmodel.NewTextPart("ok"),
				},
				Meta: map[string]any{"source": "invoke"},
			}, nil
		},
	}

	call := Call{
		Name:     "echo",
		Input:    json.RawMessage(`{"value":"ok"}`),
		Metadata: map[string]any{"trace": "1"},
	}
	result, err := tool.Call(context.Background(), call)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if got := result.Name; got != "echo" {
		t.Fatalf("result.Name = %q, want %q", got, "echo")
	}
	if got := result.Content[0].Text.Text; got != "ok" {
		t.Fatalf("result.Content[0].Text.Text = %q, want %q", got, "ok")
	}
	if _, ok := call.Metadata["mutated"]; ok {
		t.Fatal("input call metadata should be cloned")
	}
}
