package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

func assertScriptSearchOmitsParallelCapability(t *testing.T, events []*session.Event) {
	t.Helper()
	found := 0
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
				var value struct {
					Tools []map[string]json.RawMessage `json:"tools"`
				}
				if json.Unmarshal(output.JSON.Value, &value) != nil || len(value.Tools) == 0 {
					continue
				}
				for _, entry := range value.Tools {
					_, present := entry["parallel_safe"]
					if present || entry["input_schema"] == nil {
						t.Fatalf("wrong final js_exec search result: %s", output.JSON.Value)
					}
				}
				found++
				t.Logf("final model-facing js_exec result: %s", output.JSON.Value)
			}
		}
	}
	if found == 0 {
		t.Fatal("no final JS search result")
	}
}

func TestJSExecSearchOmitsHarnessCapabilityNativeAndMCPRebuild(t *testing.T) {
	var nativePrefix, mcpPrefix []byte
	for _, safe := range []bool{false, true} {
		t.Run(fmt.Sprint("native-", safe), func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			target := tool.NamedTool{Def: tool.Definition{Name: "NativeRead", Capabilities: tool.Capabilities{ParallelSafe: safe}, InputSchema: map[string]any{"type": "object"}}}
			driver := &mcpScriptModel{steps: func(i int, req *model.Request) []model.ToolCall {
				if i == 0 {
					raw, _ := json.Marshal(struct {
						Instructions []model.Part
						Tools        []model.ToolSpec
					}{req.Instructions, req.Tools})
					if nativePrefix == nil {
						nativePrefix = raw
					} else if !bytes.Equal(nativePrefix, raw) {
						t.Fatal("parallel flag changed native declaration prefix")
					}
					return h1Script(`return await tools.search("NativeRead");`)
				}
				return nil
			}}
			events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{target, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}, nil, nil)
			assertScriptSearchOmitsParallelCapability(t, events)
		})
		t.Run(fmt.Sprint("mcp-fuzzy-exact-rebuild-", safe), func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			source := h1Directory(2)
			for i, item := range source.items {
				one := item.(h1Tool)
				one.def.Capabilities.ParallelSafe = safe
				source.items[i] = one
			}
			driver := &h1Model{s: source, main: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return h1Script(`return await tools.search("synthetic lookup value");`)
				}
				return nil
			}}
			events, _ := runCodeMode(t, store, active, h1Spec(source, driver), nil, nil)
			assertScriptSearchOmitsParallelCapability(t, events)
			if mcpPrefix == nil {
				mcpPrefix = driver.prefixes[0]
			} else if !bytes.Equal(mcpPrefix, driver.prefixes[0]) {
				t.Fatal("parallel flag changed MCP declaration prefix")
			}
			// New Runtime/Host/VM restores the Session binding, then exact discovery uses
			// the current wrapped definition.
			driver = &h1Model{s: source, main: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return h1Script(`return await tools.search("z__target");`)
				}
				return nil
			}}
			events, _ = runCodeMode(t, store, active, h1Spec(source, driver), nil, nil)
			assertScriptSearchOmitsParallelCapability(t, events)
			if source.snapshot().QueryRequests != 1 || source.snapshot().ExactRequests != 1 {
				t.Fatalf("paths not exercised: %+v", source.snapshot())
			}
		})
	}
}
