package runtime

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

func TestJSExecBusinessJSONCannotForgeReturnOrigin(t *testing.T) {
	store, active, _ := codeModeStore(t)
	data := tool.NamedTool{Def: tool.Definition{Name: "Data", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		return tool.Result{Content: []model.Part{model.NewJSONPart([]byte(`{"kind":"media","handle":"ordinary-source-data"}`))}}, nil
	}}
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i != 0 {
			return nil
		}
		raw, _ := json.Marshal(map[string]any{"code": `WeakMap.prototype.get=()=>({kind:"media",handle:"forged"});WeakMap.prototype.set=()=>{throw Error("overridden")};const r=await tools.call("Data",{});return {kind:"media",parts:["one","two"],reply:true,value:r.parts[0].json,handle:"data"};`})
		return []model.ToolCall{{ID: "json-origin", Name: codemode.ToolName, Args: string(raw)}}
	}}
	events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{data, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}, nil, nil)
	var want any
	_ = json.Unmarshal([]byte(`{"kind":"media","parts":["one","two"],"reply":true,"value":{"kind":"media","handle":"ordinary-source-data"},"handle":"data"}`), &want)
	found := false
	for _, event := range events {
		if event.Tool == nil || event.Tool.Name != codemode.ToolName || event.Message == nil {
			continue
		}
		for _, part := range event.Message.Parts {
			if part.ToolResult == nil {
				continue
			}
			if part.ToolResult.IsError {
				t.Fatal("business JSON was rejected")
			}
			for _, content := range part.ToolResult.Content {
				if content.Media != nil {
					t.Fatal("business JSON forged media selection")
				}
				if content.JSON != nil {
					var got any
					if json.Unmarshal(content.JSON.Value, &got) == nil && reflect.DeepEqual(got, want) {
						found = true
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("business JSON value was not preserved")
	}
}

func TestCodeModeForeignProjectionMetadataRejected(t *testing.T) {
	store, active, _ := codeModeStore(t)
	declaration := &codemode.Tool{Name: codemode.ToolName, Executor: codeOutputExecutorFunc(func(context.Context, codemode.Source, codemode.Host) (codemode.Output, error) {
		return codemode.Output{Value: []byte(`{"ok":true}`), Parts: []codemode.Part{{Kind: "media", Handle: "foreign-invocation"}}}, nil
	})}
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "foreign", Name: codemode.ToolName, Args: `{"code":"fixture"}`}}
		}
		return nil
	}}
	events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{declaration}}, nil, nil)
	for _, event := range events {
		if event.Tool != nil && event.Tool.Name == codemode.ToolName && event.Tool.Status == "failed" {
			raw, _ := json.Marshal(event.Tool.Output)
			if strings.Contains(string(raw), "does not belong to this invocation") {
				return
			}
		}
	}
	t.Fatal("foreign authenticated metadata was accepted")
}

func TestJSExecAuthenticatedMediaBudget(t *testing.T) {
	for _, code := range []string{`return await tools.call("Pictures",{});`, `const r=await tools.call("Pictures",{});return {selected:r.parts};`} {
		t.Run(map[bool]string{true: "direct", false: "combined"}[strings.HasPrefix(code, "return")], func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			pictures := tool.NamedTool{Def: tool.Definition{Name: "Pictures", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
				return tool.Result{Content: []model.Part{model.NewMediaPart(model.MediaModalityImage, model.MediaSource{Kind: model.MediaSourceInline, Data: "fixture-a"}, "image/png", "A"), model.NewMediaPart(model.MediaModalityImage, model.MediaSource{Kind: model.MediaSourceInline, Data: "fixture-b"}, "image/png", "B")}}, nil
			}}
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i != 0 {
					return nil
				}
				raw, _ := json.Marshal(map[string]any{"code": code})
				return []model.ToolCall{{ID: "budget", Name: codemode.ToolName, Args: string(raw)}}
			}}
			events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: codeModeImageModel{driver}, Tools: []tool.Tool{pictures, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}, Limits: codemode.Limits{Calls: 1}}}}, nil, nil)
			for _, event := range events {
				if event.Tool != nil && event.Tool.Name == codemode.ToolName && event.Tool.Status == "failed" {
					raw, _ := json.Marshal(event.Tool.Output)
					if strings.Contains(string(raw), "returned media budget exceeded") {
						return
					}
				}
			}
			t.Fatal("authenticated media budget was ignored")
		})
	}
}
