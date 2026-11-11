package runtime

import (
	"context"
	"encoding/json"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

// Execution completion is an observation, independent of cumulative IsError.
func TestCodeModeScriptCompletionSurvivesCanonicalErrors(t *testing.T) {
	for _, tc := range []struct {
		name, code, want string
		failed           bool
	}{
		{"normal", `return 42;`, "completed", false},
		{"ordinary_json", `return {metadata:{code_mode_script_completion:"failed"}};`, "completed", false},
		{"caught", `try {await tools.call("Bad",{});}catch(e){}return {recovered:true};`, "completed", true},
		{"uncaught_tool", `await tools.call("Bad",{});`, "failed", true},
		{"uncaught_script", `throw Error("script failure");`, "failed", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			bad := tool.NamedTool{Def: tool.Definition{Name: "Bad", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
				return tool.Result{IsError: true, Content: []model.Part{model.NewTextPart("original tool failure")}}, nil
			}}
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i > 0 {
					return nil
				}
				raw, _ := json.Marshal(map[string]any{"code": tc.code})
				return []model.ToolCall{{ID: "completion", Name: codemode.ToolName, Args: string(raw)}}
			}}
			events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{bad, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}, nil, nil)
			seen := false
			for _, event := range events {
				if event.Type != "tool_result" || event.Tool == nil || event.Tool.ID != "completion" || event.Message == nil {
					continue
				}
				seen = true
				if event.Meta[codemode.MetadataScriptCompletion] != tc.want {
					t.Fatalf("completion=%v want %s; fixture meta=%#v", event.Meta[codemode.MetadataScriptCompletion], tc.want, event.Meta)
				}
				for _, part := range event.Message.Parts {
					if part.ToolResult != nil && part.ToolResult.IsError != tc.failed {
						t.Fatal("canonical IsError changed")
					}
				}
			}
			if !seen {
				t.Fatal("missing canonical terminal result")
			}
		})
	}
}

func TestCodeModeTaskResultPreservesScriptCompletion(t *testing.T) {
	for _, completion := range []string{"completed", "failed"} {
		raw, _ := json.Marshal(tool.Result{IsError: true, Metadata: map[string]any{codemode.MetadataScriptCompletion: completion}, Content: []model.Part{model.NewTextPart("original result")}})
		snapshot := task.Snapshot{Kind: task.KindCodeMode, State: task.StateFailed}
		result := taskSnapshotToolResultWithPayload(tool.Call{ID: "observe"}, tool.Definition{Name: "Task"}, snapshot, map[string]any{"tool_result_json": string(raw)})
		if !result.IsError || result.Metadata[codemode.MetadataScriptCompletion] != completion {
			t.Fatalf("lost independent Task observation: %+v", result)
		}
	}
}
