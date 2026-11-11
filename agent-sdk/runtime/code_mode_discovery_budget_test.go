package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

// Two candidates exercise schema admission, not directory performance.
func TestReviewSimpleSearchSchemaBudget(t *testing.T) {
	for _, exact := range []bool{false, true} {
		t.Run(map[bool]string{false: "fuzzy", true: "exact_oversize"}[exact], func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			source := &jsExecSource{}
			effects := 0
			for _, name := range []string{"fixture__small", "fixture__huge"} {
				def := fixtureMCPDefinition("v1")
				def.Name = name
				def.Metadata[tool.MetadataMCPTool] = strings.TrimPrefix(name, "fixture__")
				def.Description = "fixture schema budget lookup"
				def.Metadata[tool.MetadataMCPSourceTrusted] = true
				def.InputSchema = map[string]any{"type": "object"}
				if strings.HasSuffix(name, "huge") {
					def.InputSchema["description"] = strings.Repeat("complete schema data ", 10000)
				}
				source.items = append(source.items, tool.NamedTool{Def: def, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
					effects++
					return tool.Result{Content: []model.Part{model.NewJSONPart([]byte(`{"ok":true}`))}}, nil
				}})
			}
			code := `const s=await tools.search("fixture");if(s.count>1||!s.truncated)throw Error("budget result "+JSON.stringify(s));if(s.count){if(s.tools[0].name!=="fixture__small"||s.tools[0].input_schema.type!=="object")throw Error("partial schema");await tools.call("fixture__small",{});}return s;`
			if exact {
				code = `try{await tools.search("fixture__huge");}catch(e){}try{await tools.call("fixture__huge",{});}catch(e){}return {done:true};`
			}
			m := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					raw, _ := json.Marshal(map[string]any{"code": code})
					return []model.ToolCall{{ID: "schema-budget", Name: codemode.ToolName, Args: string(raw)}}
				}
				return nil
			}}
			events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: m, Tools: []tool.Tool{toolsearch.NewSource(source, toolsearch.NewLexicalRanker()), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}, DeferredTools: source}, nil, nil)
			failed := false
			for _, e := range events {
				failed = failed || e.Tool != nil && e.Tool.Name == codemode.ToolName && e.Tool.Status == "failed"
				if e.Tool != nil && e.Tool.Status == "failed" {
					t.Logf("failed %s: %+v", e.Tool.Name, e.Tool.Output)
				}
			}
			if exact && (!failed || effects != 0) || !exact && (failed || effects > 1) {
				t.Fatalf("exact=%v failed=%v effects=%d", exact, failed, effects)
			}
		})
	}
}
