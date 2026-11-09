package runtime

import (
	"encoding/json"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

func TestIncrementalReviewJSExecParallelSafeDiscovery(t *testing.T) {
	store, active, _ := codeModeStore(t)
	safe := tool.NamedTool{Def: tool.Definition{Name: "SafeRead", Capabilities: tool.Capabilities{ParallelSafe: true}, InputSchema: map[string]any{"type": "object"}}}
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "discover", Name: codemode.ToolName, Args: `{"code":"return await tools.search('SafeRead');"}`}}
		}
		return nil
	}}
	events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{safe, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}, nil, nil)
	found := false
	for _, ev := range events {
		if ev.Message == nil {
			continue
		}
		for _, part := range ev.Message.Parts {
			if part.ToolResult == nil || part.ToolResult.Name != codemode.ToolName {
				continue
			}
			for _, output := range part.ToolResult.Content {
				if output.JSON == nil {
					continue
				}
				t.Logf("actual js_exec model result: %s", output.JSON.Value)
				var result struct {
					Tools []map[string]json.RawMessage `json:"tools"`
				}
				if err := json.Unmarshal(output.JSON.Value, &result); err != nil || len(result.Tools) != 1 {
					continue
				}
				found = true
				value, present := result.Tools[0]["parallel_safe"]
				if !present || string(value) != "true" {
					t.Errorf("actual js_exec discovery dropped ParallelSafe=true: %s", output.JSON.Value)
				}
			}
		}
	}
	if !found {
		t.Fatal("no model-facing search result")
	}
}
