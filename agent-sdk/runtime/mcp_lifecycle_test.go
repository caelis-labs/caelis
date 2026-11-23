package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/mcpdiscovery"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

func TestLegacyMCPSearchReceiptAfterStoreReopen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		marked bool
	}{
		{"marked-stale", true},
		{"unmarked-undiscovered", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			source := &changingBindingSource{}
			source.publish(tool.NamedTool{Def: fixtureMCPDefinition("v1")})
			first := &mcpScriptModel{steps: func(step int, _ *model.Request) []model.ToolCall {
				if step == 0 {
					return h1Script(`return await tools.search({name:"docs__read"});`)
				}
				return nil
			}}
			_, rt := runCodeMode(t, store, active, agent.AgentSpec{
				Model: first, DeferredTools: source,
				Tools: []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}},
			}, nil, nil)
			if err := rt.ReleaseCodeModeEnvironments(); err != nil {
				t.Fatal(err)
			}
			if len(first.requests) != 2 {
				t.Fatalf("search turn made %d model requests, want 2", len(first.requests))
			}
			storeRoot := filepath.Join(root, "sessions")
			writeLegacyMCPSearchReceipt(t, storeRoot, tc.marked)
			reopened := file.NewStore(file.Config{RootDir: storeRoot})
			if !tc.marked {
				events, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
				if err != nil {
					t.Fatal(err)
				}
				if got := codeModeBindings(events); len(got) != 0 {
					t.Fatalf("unmarked receipt restored a binding: %v", got)
				}
			}

			var effects atomic.Int32
			source.publish(tool.NamedTool{Def: fixtureMCPDefinition("v2"), Invoke: func(context.Context, tool.Call) (tool.Result, error) {
				effects.Add(1)
				return tool.Result{}, nil
			}})
			resumed := &mcpScriptModel{steps: func(step int, _ *model.Request) []model.ToolCall {
				if tc.marked && step == 0 {
					return []model.ToolCall{{ID: "after-upgrade", Name: codemode.ToolName, Args: `{"code":"return await tools.call('docs__read', {});"}`}}
				}
				return nil
			}}
			runCodeMode(t, reopened, active, agent.AgentSpec{
				Model: resumed, DeferredTools: source,
				Tools: []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}},
			}, nil, nil)
			want := append(model.CloneMessages(first.requests[1].Messages), model.NewTextMessage(model.RoleAssistant, "done"), model.NewTextMessage(model.RoleUser, "synthetic Code Mode task"))
			if len(resumed.requests) == 0 || !reflect.DeepEqual(resumed.requests[0].Messages, want) {
				t.Fatal("reopened history differs from the original model context")
			}
			if effects.Load() != 0 {
				t.Fatalf("reopened discovery dispatched %d effects against the replacement", effects.Load())
			}
			if tc.marked {
				result := errorDeliveryModelResult(t, resumed.requests[len(resumed.requests)-1], "after-upgrade")
				parts := errorDeliveryJSON(t, result.Content)
				if !result.IsError || !bytes.Contains(parts, []byte(`"code":"stale_revision"`)) {
					t.Fatalf("resumed model received %s, want stale_revision", parts)
				}
			}
		})
	}
}

func writeLegacyMCPSearchReceipt(t *testing.T, root string, marked bool) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(root, "*", "*", "*", "*", "*.events.jsonl"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("finding fixture event log: %v, %v", paths, err)
	}
	raw, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(raw, []byte{'\n'})
	count := 0
	for i, line := range lines {
		var event session.Event
		if len(line) == 0 {
			continue
		}
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type != session.EventTypeToolResult || event.Tool == nil || event.Tool.Name != mcp.SearchToolName {
			continue
		}
		if event.Tool.Result == nil || !event.Tool.Discovery || event.Tool.Outcome != session.ToolOutcomeSucceeded {
			t.Fatal("fixture did not produce a successful discovery receipt")
		}
		// v0.83.1 wrote plain model parts with the trusted marker in result.metadata.
		// Replace only this wire record so the resumed Store must decode that shape.
		legacy := struct {
			Content  []model.Part    `json:"content"`
			Metadata map[string]bool `json:"metadata,omitempty"`
		}{Content: tool.ModelParts(event.Tool.Result.Content)}
		if marked {
			legacy.Metadata = map[string]bool{mcpdiscovery.MetadataKey: true}
		}
		row := struct {
			*session.Event
			Tool struct {
				*session.EventTool
				Result any `json:"result"`
			} `json:"tool"`
		}{Event: &event}
		event.Tool.Discovery = false
		row.Tool.EventTool, row.Tool.Result = event.Tool, legacy
		lines[i], err = json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != 1 {
		t.Fatalf("found %d discovery receipts, want 1", count)
	}
	if err := os.WriteFile(paths[0], bytes.Join(lines, []byte{'\n'}), 0o600); err != nil {
		t.Fatal(err)
	}
}
