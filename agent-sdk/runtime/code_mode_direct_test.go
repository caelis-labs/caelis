package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

func TestCodeModeDirectBindingSurvivesResumeAndRequiresExplicitRefresh(t *testing.T) {
	store, active, root := codeModeStore(t)
	source := &runtimeDeferredSource{}
	effects := 0
	publish := func(version, kind string) {
		def := fixtureMCPDefinition(version)
		def.InputSchema = map[string]any{"type": "object", "additionalProperties": false,
			"required": []string{"value"}, "properties": map[string]any{"value": map[string]any{"type": kind}}}
		source.publish(tool.NamedTool{Def: def, Invoke: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			effects++
			if parent := tool.ParentFromContext(ctx); call.Name != "docs__read" || toolbinding.Target(ctx) == nil || parent == nil || parent.Kind != codemode.ToolKind {
				t.Errorf("lost canonical identity: %+v", call)
			}
			return tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: call.Input}}}, nil
		}})
	}
	publish("direct-v1", "integer")
	run := func(sessions session.Service, scripts ...string) *mcpScriptModel {
		t.Helper()
		driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
			if i < len(scripts) {
				return h1Script(scripts[i])
			}
			return nil
		}}
		runCodeMode(t, sessions, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{
			&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}},
		}, DeferredTools: source}, nil, nil)
		return driver
	}
	first := run(store, `return await docs.read({value:1});`, `return await tools.call("docs__read",{value:2});`)
	if effects != 2 {
		t.Fatalf("direct calls without search: effects=%d", effects)
	}
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	if binding := codeModeBindings(events)["docs__read"]; binding != mcp.BindingForDefinition(source.Tools()[0].Definition()) {
		t.Fatalf("actual Journal did not retain direct binding: %+v", binding)
	}
	for _, event := range events {
		if event.Type == session.EventTypeToolResult && event.Tool != nil && event.Tool.Name == mcp.SearchToolName {
			t.Fatal("direct path performed discovery")
		}
	}
	// The Store is reopened and a new Runtime is constructed for every run.
	// Compare rebuilt model context with the actual prior request frontier.
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
	resumed := run(reopened, `return await tools.plugins.fixture.docs.read({value:3});`)
	live := first.requests[len(first.requests)-1].Messages
	var callIDs, replyIDs []string
	for _, msg := range live {
		for _, call := range msg.ToolCalls() {
			if call.Name != codemode.ToolName {
				t.Fatalf("model history exposed child tool %q", call.Name)
			}
			callIDs = append(callIDs, call.ID)
		}
		for _, reply := range msg.ToolResults() {
			if reply.Name != codemode.ToolName {
				t.Fatalf("model history exposed child result %q", reply.Name)
			}
			replyIDs = append(replyIDs, reply.ToolUseID)
		}
	}
	if len(callIDs) != 2 || !slices.Equal(callIDs, replyIDs) {
		t.Fatalf("model call IDs %v, reply IDs %v", callIDs, replyIDs)
	}
	want := append(model.CloneMessages(live), model.NewTextMessage(model.RoleAssistant, "done"), model.NewTextMessage(model.RoleUser, "synthetic Code Mode task"))
	if !reflect.DeepEqual(resumed.requests[0].Messages, want) {
		t.Fatal("reopening the Session changed the runtime-produced model context")
	}
	if effects != 3 {
		t.Fatalf("same binding did not resume: effects=%d", effects)
	}
	publish("direct-v2", "string")
	stale := run(reopened, `return await docs.read({value:4});`)
	if effects != 3 {
		t.Fatal("old arguments executed against replacement schema")
	}
	raw, _ := json.Marshal(stale.requests[len(stale.requests)-1].Messages)
	if !strings.Contains(string(raw), "schema changed") || !strings.Contains(string(raw), "tools.search") {
		t.Fatalf("missing explicit refresh diagnostic: %s", raw)
	}
	run(reopened, `const schema=await tools.search({name:"docs__read"});if(schema.name!=="docs__read")throw Error('schema unavailable');const args={value:"new"};return await eval(schema.call_path);`)
	if effects != 4 {
		t.Fatalf("explicit refresh did not admit new arguments: effects=%d", effects)
	}
	assertSubagentSagaModelRoundTrip(t, reopened, active.SessionRef)
}

func TestCodeModeNativeOwnerSupersedesHistoricalMCPBinding(t *testing.T) {
	prior := fixtureMCPDefinition("v1")
	binding := mcp.BindingForDefinition(prior)
	current := tool.NamedTool{Def: tool.Definition{Name: prior.Name, InputSchema: map[string]any{"type": "object"}}}
	host := &codeModeHost{ctx: t.Context(), limits: codemode.DefaultLimits(), targets: map[string]codeModeTarget{}, owner: runtimeCodeModeTool{
		availableTools: func() []tool.Tool { return []tool.Tool{current} },
		lookup:         func(context.Context, string) (tool.Tool, bool, error) { return current, true, nil },
		checkBinding: func(context.Context, tool.Binding) error {
			t.Error("native owner consulted obsolete MCP authority")
			return tool.NewError(tool.ErrorCodeStaleRevision, "MCP binding is no longer present")
		},
	}}
	events := []*session.Event{{Type: session.EventTypeToolCall, Visibility: session.VisibilityMirror, Tool: &session.EventTool{
		Name: prior.Name, Binding: &binding, Status: "pending", Parent: &session.EventParentTool{Kind: codemode.ToolKind},
	}}}
	if err := host.prepareTargets(t.Context(), events); err != nil {
		t.Fatal(err)
	}
	if _, err := host.resolveTarget(t.Context(), prior.Name, false); err != nil {
		t.Fatalf("current native owner blocked: %v", err)
	}
	if _, err := host.Search(t.Context(), codemode.SearchRequest{Name: prior.Name}); err != nil {
		t.Fatalf("native schema query: %v", err)
	}
	if _, err := host.resolveTarget(t.Context(), prior.Name, false); err != nil {
		t.Fatalf("native schema query restored obsolete MCP binding: %v", err)
	}
}
