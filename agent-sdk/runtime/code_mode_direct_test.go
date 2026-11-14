package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/mcpdiscovery"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

func TestCodeModeDirectBindingSurvivesResumeAndRequiresExplicitRefresh(t *testing.T) {
	store, active, root := codeModeStore(t)
	source := &runtimeDeferredSource{}
	effects := 0
	publish := func(version, kind string) {
		def := fixtureMCPDefinition(version)
		def.Metadata[tool.MetadataMCPSourceTrusted] = true
		def.InputSchema = map[string]any{"type": "object", "additionalProperties": false,
			"required": []string{"value"}, "properties": map[string]any{"value": map[string]any{"type": kind}}}
		source.publish(tool.NamedTool{Def: def, Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			effects++
			if call.Name != "docs__read" || call.TargetBinding == nil || call.Parent == nil || call.Parent.Kind != codemode.ToolKind {
				t.Errorf("lost canonical identity: %+v", call)
			}
			return tool.Result{Content: []model.Part{model.NewJSONPart(call.Input)}}, nil
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
			toolsearch.NewSource(source, exactMCPRanker{}), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}},
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
	if binding := codeModeBindings(events)["docs__read"]; binding != tool.BindingForDefinition(source.Tools()[0].Definition()) {
		t.Fatalf("actual Journal did not retain direct binding: %+v", binding)
	}
	for _, event := range events {
		if event.Journal != nil && event.Journal.ToolExecution != nil && event.Journal.ToolExecution.ToolName == tool.ToolSearchToolName {
			t.Fatal("direct path performed discovery")
		}
	}
	// The Store is reopened and a new Runtime is constructed for every run.
	// Compare rebuilt model context with the actual prior request frontier.
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
	resumed := run(reopened, `return await tools.plugins.fixture.docs.read({value:3});`)
	before, _ := json.Marshal(first.requests[len(first.requests)-1].Messages)
	after, _ := json.Marshal(resumed.requests[0].Messages)
	if !strings.Contains(string(after), string(before[1:len(before)-1])) {
		t.Fatal("reopened model context lost the Runtime-produced messages")
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
	if !strings.Contains(string(raw), "binding is stale") || !strings.Contains(string(raw), "tools.search(name)") {
		t.Fatalf("missing explicit refresh diagnostic: %s", raw)
	}
	run(reopened, `const schema=await tools.search("docs__read");if(schema.count!==1)throw Error('schema unavailable');return await docs.read({value:"new"});`)
	if effects != 4 {
		t.Fatalf("explicit refresh did not admit new arguments: effects=%d", effects)
	}
	assertSubagentSagaModelRoundTrip(t, reopened, active.SessionRef)
}

func TestCodeModeDirectBindingDoesNotGrantLegacyMCPDiscovery(t *testing.T) {
	def := fixtureMCPDefinition("direct-v1")
	binding := tool.BindingForDefinition(def)
	events := []*session.Event{{Visibility: session.VisibilityJournal, Journal: &session.ExecutionJournalEntry{ToolExecution: &session.ToolExecution{
		ToolName: def.Name, Binding: &binding, Status: session.ToolExecutionStarted,
		Parent: &tool.ParentCall{Kind: codemode.ToolKind},
	}}}}
	if codeModeBindings(events)[def.Name] != binding {
		t.Fatal("Code Mode failed to retain its actual binding")
	}
	if len(mcpdiscovery.FromEvents(events)) != 0 {
		t.Fatal("direct child Journal granted legacy discovery eligibility")
	}
}

func TestCodeModeNativeOwnerSupersedesHistoricalMCPBinding(t *testing.T) {
	prior := fixtureMCPDefinition("v1")
	binding := tool.BindingForDefinition(prior)
	current := tool.NamedTool{Def: tool.Definition{Name: prior.Name, InputSchema: map[string]any{"type": "object"}}}
	host := &codeModeHost{ctx: t.Context(), limits: codemode.DefaultLimits(), targets: map[string]codeModeTarget{}, owner: runtimeCodeModeTool{
		availableTools: func() []tool.Tool { return []tool.Tool{current} },
		lookup:         func(context.Context, string) (tool.Tool, bool, error) { return current, true, nil },
		checkBinding: func(context.Context, tool.Binding) error {
			t.Error("native owner consulted obsolete MCP authority")
			return tool.NewError(tool.ErrorCodeStaleRevision, "MCP binding is no longer present")
		},
	}}
	events := []*session.Event{{Visibility: session.VisibilityJournal, Journal: &session.ExecutionJournalEntry{ToolExecution: &session.ToolExecution{
		ToolName: prior.Name, Binding: &binding, Status: session.ToolExecutionStarted, Parent: &tool.ParentCall{Kind: codemode.ToolKind},
	}}}}
	if err := host.prepareTargets(t.Context(), events); err != nil {
		t.Fatal(err)
	}
	if _, err := host.resolveTarget(t.Context(), prior.Name, false); err != nil {
		t.Fatalf("current native owner blocked: %v", err)
	}
	if _, err := host.Search(t.Context(), codemode.SearchRequest{Query: prior.Name}); err != nil {
		t.Fatalf("native schema query: %v", err)
	}
	if _, err := host.resolveTarget(t.Context(), prior.Name, false); err != nil {
		t.Fatalf("native schema query restored obsolete MCP binding: %v", err)
	}
}
