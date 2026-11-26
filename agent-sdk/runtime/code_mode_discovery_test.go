package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

// Complete-schema query budgets do not gate a direct call with known arguments.
func TestReviewSimpleSearchSchemaBudget(t *testing.T) {
	t.Parallel()
	for _, exact := range []bool{false, true} {
		t.Run(map[bool]string{false: "fuzzy", true: "exact_oversize"}[exact], func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			source := &jsExecSource{}
			effects := 0
			for _, name := range []string{"fixture__small", "fixture__huge"} {
				def := fixtureMCPDefinition("v1")
				def.Name = name
				def.Metadata[mcp.MetadataTool] = strings.TrimPrefix(name, "fixture__")
				def.Description = "fixture schema budget lookup"
				def.InputSchema = map[string]any{"type": "object"}
				if strings.HasSuffix(name, "huge") {
					def.InputSchema["description"] = strings.Repeat("complete schema data ", 10000)
				}
				source.items = append(source.items, tool.NamedTool{Def: def, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
					effects++
					return tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: []byte(`{"ok":true}`)}}}, nil
				}})
			}
			code := `const s=await tools.search({query:"fixture"});if(s.tools.length!==2||s.tools.some(t=>"input_schema" in t))throw Error("directory lost summaries");return s;`
			if exact {
				code = `let rejected=false;try{await tools.search({name:"fixture__huge"});}catch(e){rejected=true;}if(!rejected)throw Error("oversized schema accepted");await tools.call("fixture__huge",{});return {done:true};`
			}
			m := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					raw, _ := json.Marshal(map[string]any{"code": code})
					return []model.ToolCall{{ID: "schema-budget", Name: codemode.ToolName, Args: string(raw)}}
				}
				return nil
			}}
			events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: m, Tools: []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}, DeferredTools: source}, nil, nil)
			failed := false
			for _, e := range events {
				failed = failed || e.Tool != nil && e.Tool.Name == codemode.ToolName && e.Tool.Status == "failed"
				if e.Tool != nil && e.Tool.Status == "failed" {
					t.Logf("failed %s: %+v", e.Tool.Name, e.Tool.Output)
				}
			}
			if exact && (!failed || effects != 1) || !exact && (failed || effects > 1) {
				t.Fatalf("exact=%v failed=%v effects=%d", exact, failed, effects)
			}
		})
	}
}

// A derived name index in the fixture's existing source owner. The base source
// remains the current Tool snapshot; replacing an item is visible immediately.
type h1NamedSource struct {
	*h1Source
	positions map[string]int
}

func h1Indexed(s *h1Source) *h1NamedSource {
	out := &h1NamedSource{h1Source: s, positions: map[string]int{}}
	for i, one := range s.items {
		out.positions[one.Definition().Name] = i
	}
	return out
}
func (s *h1NamedSource) Lookup(ctx context.Context, name string) (tool.Tool, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.LookupRequests++
	i, ok := s.positions[name]
	if !ok {
		return nil, false, nil
	}
	return s.items[i], true, nil
}

func TestJSExecLookupStillFiltersAndWrapsSourceTarget(t *testing.T) {
	t.Parallel()
	s := h1Directory(2)
	named := h1Indexed(s)
	wraps := 0
	wrapped := &deferredToolSource{source: named, wrap: func(items []tool.Tool) []tool.Tool { wraps++; return items }}
	if one, found, err := wrapped.Lookup(t.Context(), "a__target"); err != nil || !found || one == nil || wraps != 1 {
		t.Fatalf("current target not wrapped: %v %v %d", found, err, wraps)
	}
	s.mu.Lock()
	s.items[0] = tool.NamedTool{Def: tool.Definition{Name: "a__target"}}
	s.mu.Unlock()
	if _, found, err := wrapped.Lookup(t.Context(), "a__target"); err != nil || found || wraps != 1 {
		t.Fatalf("native injection reached wrapper: %v %v %d", found, err, wraps)
	}
	if _, found, err := wrapped.Lookup(t.Context(), "replay_alias"); err != nil || found {
		t.Fatal("missing canonical name gained alias")
	}
}

type managerAdmissionSource struct {
	*mcp.Manager
	checks      atomic.Int32
	beforeFinal func(context.Context) error
}

func (s *managerAdmissionSource) CheckToolBinding(ctx context.Context, binding tool.Binding) error {
	if s.checks.Add(1) == 2 {
		if err := s.beforeFinal(ctx); err != nil {
			return err
		}
	}
	return s.Manager.CheckToolBinding(ctx, binding)
}

func TestJSExecManagerLookupFinalAdmissionRejectsSourceChange(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"revoke", "schema"} {
		t.Run(change, func(t *testing.T) {
			var effects atomic.Int32
			server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "manager-admission", Version: "1"}, nil)
			publish := func(kind string) {
				server.AddTool(&mcpsdk.Tool{Name: "read", InputSchema: map[string]any{"type": "object", "required": []string{"value"}, "properties": map[string]any{"value": map[string]any{"type": kind}}}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
					effects.Add(1)
					return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "effect"}}}, nil
				})
			}
			publish("integer")
			endpoint := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil))
			defer endpoint.Close()
			manager, err := mcp.NewManager(t.Context(), []mcp.ServerSpec{{PluginID: "fixture", Name: "live", Transport: mcp.TransportStreamableHTTP, URL: endpoint.URL}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			<-manager.Initialized()
			source := &managerAdmissionSource{Manager: manager, beforeFinal: func(ctx context.Context) error {
				if change == "revoke" {
					return manager.RevokeServer("fixture", "live")
				}
				publish("string")
				return manager.RefreshTools(ctx, "fixture", "live")
			}}
			store, active, _ := codeModeStore(t)
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				switch i {
				case 0:
					return h1Script(`return await tools.search({name:"live__read"});`)
				case 1:
					return h1Script(`await tools.call("live__read",{value:1});`)
				}
				return nil
			}}
			events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}, DeferredTools: source}, nil, nil)
			if effects.Load() != 0 || source.checks.Load() != 2 {
				t.Fatalf("final boundary bypass: effects=%d checks=%d", effects.Load(), source.checks.Load())
			}
			failed := false
			for _, event := range events {
				if event.Tool != nil && event.Tool.Name == codemode.ToolName && event.Tool.Status == "failed" {
					failed = true
				}
			}
			if !failed {
				t.Fatal("final admission error hidden")
			}
			if change == "schema" {
				raw, _ := json.Marshal(driver.requests[len(driver.requests)-1].Messages)
				if !strings.Contains(string(raw), "tools.search") || !strings.Contains(string(raw), "schema changed") {
					t.Fatalf("next request lacks Code Mode stale guidance: %s", raw)
				}
			}

		})
	}
}

// These counters observe existing source/selector interfaces. They do not
// replace Runtime discovery, binding, authorization, or its journal.
type h1Stats struct {
	LookupRequests     int `json:"source_lookup_requests"`
	ToolsEnumerations  int `json:"tools_enumerations"`
	SearchEnumerations int `json:"search_enumerations"`
	EnumeratedItems    int `json:"enumerated_items"`
	DefinitionReads    int `json:"definition_schema_materializations"`

	Effects       int   `json:"actual_effects"`
	EnumerationNS int64 `json:"enumeration_ns"`
	DefinitionNS  int64 `json:"definition_materialization_ns"`
}
type h1Source struct {
	mu    sync.Mutex
	items []tool.Tool
	stats h1Stats
}

func (s *h1Source) Tools() []tool.Tool       { return s.enumerate(false) }
func (s *h1Source) SearchTools() []tool.Tool { return s.enumerate(true) }
func (s *h1Source) enumerate(search bool) []tool.Tool {
	start := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if search {
		s.stats.SearchEnumerations++
	} else {
		s.stats.ToolsEnumerations++
	}
	s.stats.EnumeratedItems += len(s.items)
	out := append([]tool.Tool(nil), s.items...)
	s.stats.EnumerationNS += time.Since(start).Nanoseconds()
	return out
}
func (s *h1Source) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats = h1Stats{}

}
func (s *h1Source) snapshot() h1Stats { s.mu.Lock(); defer s.mu.Unlock(); return s.stats }

type h1Tool struct {
	source *h1Source
	def    tool.Definition
}

func (t h1Tool) Definition() tool.Definition {
	start := time.Now()
	def := tool.CloneDefinition(t.def)
	t.source.mu.Lock()
	t.source.stats.DefinitionReads++
	t.source.stats.DefinitionNS += time.Since(start).Nanoseconds()
	t.source.mu.Unlock()
	return def
}
func (t h1Tool) Call(context.Context, tool.Call) (tool.Result, error) {
	t.source.mu.Lock()
	t.source.stats.Effects++
	t.source.mu.Unlock()
	return tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: []byte(`{"value":42}`)}}}, nil
}
func h1Directory(n int) *h1Source {
	s := &h1Source{}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("m_%04d__lookup", i)
		if i == 0 {
			name = "a__target"
		}
		if i == n-1 {
			name = "z__target"
		}
		def := fixtureMCPDefinition("h1-v1")
		def.Name = name
		def.Description = strings.Repeat("Read a synthetic lookup value with an integer argument. ", 3)
		def.InputSchema = map[string]any{"type": "object", "additionalProperties": false, "required": []string{"value"}, "properties": map[string]any{"value": map[string]any{"type": "integer"}}}
		s.items = append(s.items, h1Tool{s, def})
	}
	return s
}

type h1Model struct {
	s         *h1Source
	mainCalls int
	main      func(int, *model.Request) []model.ToolCall
	prefixes  [][]byte
}

func (*h1Model) Name() string                     { return "h1-offline-selector-fixture" }
func (*h1Model) Capabilities() model.Capabilities { return runtimeTestModelCapabilities() }
func (m *h1Model) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		prefix, _ := json.Marshal(struct {
			Instructions []model.Part     `json:"instructions"`
			Tools        []model.ToolSpec `json:"tools"`
		}{req.Instructions, req.Tools})
		m.prefixes = append(m.prefixes, prefix)
		i := m.mainCalls
		m.mainCalls++
		var calls []model.ToolCall
		if m.main != nil {
			calls = m.main(i, req)
		}
		msg := model.NewTextMessage(model.RoleAssistant, "done")
		if len(calls) > 0 {
			msg = model.MessageFromToolCalls(model.RoleAssistant, calls, "")
		}
		yield(model.StreamEventFromResponse(&model.Response{Message: msg, TurnComplete: true, Status: model.ResponseStatusCompleted}), nil)
	}
}
func h1Script(code string) []model.ToolCall {
	raw, _ := json.Marshal(map[string]any{"code": code})
	return []model.ToolCall{{ID: "h1", Name: codemode.ToolName, Args: string(raw)}}
}
func h1Spec(s *h1Source, m *h1Model) agent.AgentSpec {
	return agent.AgentSpec{Model: m, Tools: []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}, DeferredTools: s, Metadata: map[string]any{codemode.MetadataSelectedTools: []string{"a__target", "z__target"}}}
}
func h1Run(t *testing.T, rt *Runtime, active session.Session, spec agent.AgentSpec) []*session.Event {
	t.Helper()
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "offline H1 fixture", AgentSpec: spec})
	if err != nil {
		t.Fatal(err)
	}
	if err = run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	events, err := rt.sessions.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	return events
}
func TestJSExecH1SessionReuseAndPermissionRevocation(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"permission", "schema"} {
		t.Run(change, func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			s := h1Directory(2)
			deny := false
			registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "mcp-test", Decide: func(_ context.Context, in policy.ToolContext) (policy.Decision, error) {
				action := policy.ActionAllow
				if deny && in.Tool.Name == "a__target" {
					action = policy.ActionDeny
				}
				return policy.Decision{Action: action}, nil
			}}}
			m := &h1Model{s: s, main: func(i int, _ *model.Request) []model.ToolCall {
				switch i {
				case 0:
					return h1Script(`globalThis.local=1;return [await tools.search({name:"a__target"}),await tools.search({name:"z__target"})];`)
				case 1:
					return h1Script(`if(local!==1)throw Error("VM lost Session state");await tools.call("a__target",{value:1});await tools.call("z__target",{value:1});`)
				}
				return nil
			}}
			_, rt := runCodeMode(t, store, active, h1Spec(s, m), registry, nil)
			if got := s.snapshot(); got.Effects != 2 {
				t.Fatalf("initial exact path=%+v", got)
			}
			s.reset()
			m.mainCalls = 0
			m.main = func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return h1Script(`await tools.call("a__target",{value:2});await tools.call("z__target",{value:2});`)
				}
				return nil
			}
			h1Run(t, rt, active, h1Spec(s, m))
			if got := s.snapshot(); got.Effects != 2 {
				t.Fatalf("new Turn rediscovered or missed effects: %+v", got)
			}
			if change == "permission" {
				deny = true
			} else {
				s.mu.Lock()
				item := s.items[0].(h1Tool)
				item.def.InputSchema = map[string]any{"type": "object", "properties": map[string]any{"changed": map[string]any{"type": "string"}}}
				item.def.Metadata[mcp.MetadataSourceFingerprint] = "h1-v2"
				s.items[0] = item
				s.mu.Unlock()
			}
			s.reset()
			m.mainCalls = 0
			m.main = func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return h1Script(`await tools.call("a__target",{value:3});`)
				}
				return nil
			}
			events := h1Run(t, rt, active, h1Spec(s, m))
			if s.snapshot().Effects != 0 {
				t.Fatal("revoked/stale target executed")
			}
			failed := false
			for _, e := range events {
				if e.Tool != nil && e.Tool.Name == codemode.ToolName && e.Tool.Status == "failed" {
					failed = true
				}
			}
			if !failed {
				t.Fatal("revocation/stale was not visible")
			}
			if directory := os.Getenv("CAELIS_H1_EVIDENCE"); directory != "" {
				if err := os.MkdirAll(directory, 0700); err != nil {
					t.Fatal(err)
				}
				raw, err := json.MarshalIndent(map[string]any{"case": change, "classification": "offline real Runtime/Goja, synthetic wrapped target", "after_change": s.snapshot(), "original_session_events": events}, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, "revocation-"+change+".json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}

		})
	}
}

func TestJSExecH1HintPreservesDescriptionsAndStoredSnapshots(t *testing.T) {
	t.Parallel()
	store, active, _ := codeModeStore(t)
	_, err := store.UpdateState(t.Context(), session.UpdateStateRequest{SessionRef: active.SessionRef, Update: func(state map[string]any) (map[string]any, error) {
		if state == nil {
			state = map[string]any{}
		}
		state["code_mode_catalog_v2"] = "legacy guidance requiring parallel_safe"
		state["code_mode_catalog_v3"] = "legacy guidance without service continuation"
		return state, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	s := h1Directory(2)
	text, err := rt.codeModeCatalog(t.Context(), active.SessionRef, s, map[string]any{codemode.MetadataSelectedTools: []string{"a__target", "z__target"}})
	if err != nil {
		t.Fatal(err)
	}
	description := (&codemode.Tool{Name: codemode.ToolName}).Definition().Description
	if !strings.HasPrefix(text, codemode.Guidance+"\n\nPartial tool hint snapshot; use tools.search for other capabilities.\n") ||
		!strings.Contains(text, "Read a synthetic lookup") || strings.Contains(text, "input_schema") || strings.Contains(text, "parallel_safe") {
		t.Fatalf("H1 hint semantics lost or schemas leaked into guidance: %s", text)
	}
	if !strings.Contains(text, "Search for unknown tools or arguments") || strings.Contains(text, "tools.search(") || strings.Contains(text, "tools.describe") || strings.Contains(text, "output.") {
		t.Fatal("guidance should contain preferences without duplicating API details")
	}
	for _, contract := range []string{"tools.search(", "{name}", "tools.search()", "{query?,after?}", "input_schema", "output_schema", "call_path", "error.message, error.code and error.result when present", "stale_revision"} {
		if !strings.Contains(description, contract) {
			t.Fatalf("js_exec description lost API contract %q", contract)
		}
	}

	state, _ := store.SnapshotState(t.Context(), active.SessionRef)
	if state["code_mode_catalog_v2"] != "legacy guidance requiring parallel_safe" || state["code_mode_catalog_v3"] != "legacy guidance without service continuation" {
		t.Fatal("changed a historical API snapshot")
	}
	again, err := rt.codeModeCatalog(t.Context(), active.SessionRef, nil, nil)
	if err != nil || again != state[codemode.MetadataCatalog] {
		t.Fatal("rewrote existing Session guidance")
	}
	fresh, another, _ := codeModeStore(t)
	other, err := New(Config{Sessions: fresh, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	many := h1Directory(8)
	var names []string
	for i, one := range many.items {
		item := one.(h1Tool)
		item.def.Description = strings.Repeat("读取语义", 50)
		many.items[i] = item
		names = append(names, item.def.Name)
	}
	capped, err := other.codeModeCatalog(t.Context(), another.SessionRef, many, map[string]any{codemode.MetadataSelectedTools: names})
	if err != nil {
		t.Fatal(err)
	}
	var hints []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(capped[strings.LastIndex(capped, "\n")+1:]), &hints); err != nil {
		t.Fatal(err)
	}
	if len(hints) != 6 {
		t.Fatalf("hint cap changed: %d", len(hints))
	}
	for _, hint := range hints {
		if len(hint.Description) > 128 || !utf8.ValidString(hint.Description) || !strings.HasPrefix(hint.Description, "读取语义") {
			t.Fatalf("hint bound/semantics lost: %+v", hint)
		}
	}

}

func TestJSExecSearchRejectsUnsupportedObject(t *testing.T) {
	t.Parallel()
	store, active, _ := codeModeStore(t)
	s := h1Directory(0)
	m := &h1Model{s: s, main: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return h1Script(`return await tools.search({name:"unknown",query:"invalid selector combination"});`)
		}
		return nil
	}}
	events, _ := runCodeMode(t, store, active, h1Spec(s, m), nil, nil)

	for _, e := range events {
		if e.Tool != nil && e.Tool.Name == codemode.ToolName && e.Tool.Status == "failed" {
			return
		}
	}
	t.Fatal("invalid query was hidden")
}

// A static MCP Tool is legal assembled input. Discovering it must not duplicate
// that same registered target when the next isolated VM restores eligibility.
func TestReviewStaticMCPDiscoveryAcrossVMs(t *testing.T) {
	t.Parallel()
	store, active, _ := codeModeStore(t)
	s := h1Directory(2)
	m := &h1Model{s: s, main: func(i int, _ *model.Request) []model.ToolCall {
		switch i {
		case 0:
			return h1Script(`return await tools.search({name:"a__target"});`)
		case 1:
			return h1Script(`return await tools.call("a__target",{value:1});`)
		}
		return nil
	}}
	spec := agent.AgentSpec{Model: m, Tools: []tool.Tool{s.items[0], &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}
	events, _ := runCodeMode(t, store, active, spec, nil, nil)
	if s.snapshot().Effects != 1 {
		for _, e := range events {
			if e.Tool != nil && e.Tool.Name == codemode.ToolName && e.Tool.Status == "failed" {
				t.Logf("failed parent output: %+v", e.Tool.Output)
			}
		}
		t.Fatalf("static discovered target must execute once in next VM; stats=%+v", s.snapshot())
	}
}

func TestJSExecStaticDiscoveredUnionStillRejectsDuplicateRegistrations(t *testing.T) {
	t.Parallel()
	store, active, _ := codeModeStore(t)
	s := h1Directory(2)
	driver := &h1Model{s: s, main: func(i int, _ *model.Request) []model.ToolCall {
		switch i {
		case 0:
			return h1Script(`return await tools.search({name:"a__target"});`)
		case 1:
			return h1Script(`await tools.call("a__target",{value:1});`)
		}
		return nil
	}}
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "duplicate registration", AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{s.items[0], s.items[0], &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}})
	if err == nil {
		err = run.Handle.WaitCompletion(t.Context())
	}
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("true duplicate not rejected at assembly: %v", err)
	}
	if s.snapshot().Effects != 0 {
		t.Fatal("duplicate static registrations executed")
	}
}

func TestJSExecStaticAndDeferredUnionKeepsBothBindings(t *testing.T) {
	t.Parallel()
	store, active, _ := codeModeStore(t)
	s := h1Directory(2)
	driver := &h1Model{s: s, main: func(i int, _ *model.Request) []model.ToolCall {
		switch i {
		case 0:
			return h1Script(`return [await tools.search({name:"a__target"}),await tools.search({name:"z__target"})];`)
		case 1:
			return h1Script(`await tools.call("a__target",{value:1});await tools.call("z__target",{value:1});`)
		}
		return nil
	}}
	spec := h1Spec(s, driver)
	spec.Tools = append([]tool.Tool{s.items[0]}, spec.Tools...)
	runCodeMode(t, store, active, spec, nil, nil)
	if stats := s.snapshot(); stats.Effects != 2 {
		t.Fatalf("static/deferred union lost binding: %+v", stats)
	}
}

func TestCodeModeConcurrentSearchFailuresRetainEachAttempt(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}, 2), make(chan struct{})
	host := &codeModeHost{ctx: t.Context(), owner: runtimeCodeModeTool{lookup: func(context.Context, string) (tool.Tool, bool, error) {
		entered <- struct{}{}
		<-release
		return nil, false, tool.NewError(tool.ErrorCodeNotFound, "directory unavailable")
	}}}
	done := make(chan error, 2)
	for _, query := range []string{"first", "second"} {
		go func() {
			_, err := host.Search(t.Context(), codemode.SearchRequest{Name: query})
			done <- err
		}()
	}
	<-entered
	<-entered
	close(release)
	for range 2 {
		if err := <-done; err == nil {
			t.Fatal("search failure became success")
		}
	}
	if len(host.failures) != 2 {
		t.Fatalf("concurrent discovery lost a failure: %+v", host.failures)
	}
}

func TestCodeModeServiceFiltersCurrentRuntimeSourceBeforeLimit(t *testing.T) {
	t.Parallel()
	store, active, _ := codeModeStore(t)
	source := h1Directory(20)
	// Discovery's catalog is broader than the assembled execution source. The
	// current Runtime lookup, rather than this catalog, owns source eligibility.
	source.items = source.items[16:]
	m := &h1Model{s: source, main: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return h1Script(`const s=await tools.search();if(s.tools.filter(t=>t.server).length!==4||s.truncated||s.next)throw Error("source scope applied after limit: "+JSON.stringify(s));return await tools.call("z__target",{value:1});`)
		}
		return nil
	}}
	runCodeMode(t, store, active, agent.AgentSpec{Model: m, Tools: []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}, DeferredTools: source}, nil, nil)
	if source.snapshot().Effects != 1 {
		t.Fatalf("current source tools were hidden: %+v", source.snapshot())
	}
}

func runSearchContractResult(t *testing.T, source tool.Source, extra []tool.Tool, code string) (*session.Event, []*session.Event) {
	t.Helper()
	store, active, _ := codeModeStore(t)
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return h1Script(code)
		}
		return nil
	}}
	tools := append([]tool.Tool(nil), extra...)
	tools = append(tools, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}})
	runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: tools, DeferredTools: source}, nil, nil)
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type != "tool_result" || event.Tool == nil || event.Tool.Name != codemode.ToolName || event.Message == nil {
			continue
		}
		return event, events
	}
	t.Fatal("contract script produced no terminal result")
	return nil, nil
}

func runSearchContract(t *testing.T, source tool.Source, extra []tool.Tool, code string) []*session.Event {
	t.Helper()
	event, events := runSearchContractResult(t, source, extra, code)
	if event.Meta[codemode.MetadataScriptCompletion] != "completed" {
		t.Fatalf("contract script did not complete: %+v", event.Tool.Output)
	}
	for _, part := range event.Message.Parts {
		if part.ToolResult == nil {
			continue
		}
		for _, content := range part.ToolResult.Content {
			if content.JSON == nil {
				continue
			}
			var result struct {
				Contract bool `json:"contract"`
			}
			if json.Unmarshal(content.JSON.Value, &result) == nil && result.Contract {
				return events
			}
		}
	}
	t.Fatalf("contract script lost its final result: %+v", event.Tool.Output)
	return nil
}

func TestCodeModeSearchContractCallPathsPreserveOriginalNames(t *testing.T) {
	t.Parallel()
	const canonical = "canonical__read\"quoted\\path"
	const server = "server\"with-punctuation."
	const remote = "read'quoted\"tool-name"
	const plugin = "plugin'quoted\"name"
	def := fixtureMCPDefinition("search-contract-v1")
	def.Name = canonical
	def.Metadata[mcp.MetadataServer] = server
	def.Metadata[mcp.MetadataTool] = remote
	def.Metadata[mcp.MetadataPluginID] = plugin
	def.InputSchema = map[string]any{"type": "object", "required": []string{"value"}, "properties": map[string]any{"value": map[string]any{"type": "integer"}}}
	var effects []string
	invoke := func(ctx context.Context, call tool.Call) (tool.Result, error) {
		effects = append(effects, call.Name)
		if parent := tool.ParentFromContext(ctx); parent == nil || parent.Kind != codemode.ToolKind {
			t.Errorf("call path lost Runtime parent: %+v", call)
		}
		if binding := toolbinding.Target(ctx); call.Name == canonical && (binding == nil || *binding != mcp.BindingForDefinition(def)) {
			t.Errorf("call path lost exact binding: %+v", call)
		}
		return tool.Result{
			Content: []tool.ContentBlock{tool.StructuredContent{Value: call.Input}},
		}, nil
	}
	source := &runtimeDeferredSource{}
	source.publish(tool.NamedTool{Def: def, Invoke: invoke})
	date := tool.NamedTool{Def: tool.Definition{Name: "Date", InputSchema: def.InputSchema}, Invoke: invoke}
	metadata, _ := json.Marshal(map[string]string{"name": canonical, "server": server, "tool": remote, "plugin": plugin})
	code := fmt.Sprintf(`
const expected=%s;
const page=await tools.search();
if(!page.tools.some(t=>t.name===expected.name)||!page.tools.some(t=>t.name==="Date")||page.next!==null)throw Error("directory omitted eligible tools");
if(typeof Date.now!=='function')throw Error('Date global overwritten');
const date=await tools.search({name:'Date'});
if(date.name!=='Date'||'server' in date||'tool' in date||'plugin' in date)throw Error('Date metadata');
if(date.call_path!=='tools.call("Date", args)')throw Error('intrinsic requires fallback');
let args={value:1};
const first=await eval(date.call_path);
if(first.data.value!==1)throw Error('Date template result');
const entry=await tools.search({name:expected.name});
for(const key of Object.keys(expected))if(entry[key]!==expected[key])throw Error('metadata fidelity '+key);
if(entry.input_schema.required[0]!=='value')throw Error('incomplete schema');
if(typeof tools.plugins[expected.plugin][expected.server][expected.tool]!=='function')throw Error('original object names lost');
args={value:2};
const second=await eval(entry.call_path);
if(second.data.value!==2)throw Error('quoted template result');
if(typeof Date.now!=='function')throw Error('Date global changed');
return {contract:true};`, metadata)
	runSearchContract(t, source, []tool.Tool{date}, code)
	if !reflect.DeepEqual(effects, []string{"Date", canonical}) {
		t.Fatalf("templates executed wrong canonical targets: %q", effects)
	}
}

func TestCodeModeSearchContractNewlyReadyToolUsesSameVMCallPath(t *testing.T) {
	t.Parallel()
	source := &runtimeDeferredSource{}
	def := fixtureMCPDefinition("search-contract-late-v1")
	def.Name = "late__read"
	def.Metadata[mcp.MetadataServer] = "late-server"
	effects := 0
	late := tool.NamedTool{Def: def, Invoke: func(ctx context.Context, call tool.Call) (tool.Result, error) {
		effects++
		if binding := toolbinding.Target(ctx); call.Name != def.Name || binding == nil || *binding != mcp.BindingForDefinition(def) {
			t.Errorf("newly ready call lost identity: %+v", call)
		}
		raw := json.RawMessage(`{"ready":true}`)
		return tool.Result{
			Content: []tool.ContentBlock{tool.StructuredContent{Value: raw}},
		}, nil
	}}
	publications := 0
	publish := tool.NamedTool{Def: tool.Definition{Name: "PublishReadyTool", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		publications++
		source.publish(late)
		return tool.Result{}, nil
	}}
	runSearchContract(t, source, []tool.Tool{publish}, `
if('late-server' in tools.mcp)throw Error('tool was ready before VM snapshot');
await PublishReadyTool({});
const page=await tools.search({query:'LATE read'});
if(page.tools.length!==1||page.next!==null||page.tools[0].server!=='late-server'||'input_schema' in page.tools[0])throw Error('unexpected directory');
const found=await tools.search({name:page.tools[0].name});
if(found.call_path!=='tools.call("late__read", args)')throw Error('late tool requires fallback');
if('late-server' in tools.mcp)throw Error('search mutated frozen namespace');
const args={};
const reply=await eval(found.call_path);
if(reply.data.ready!==true)throw Error('new tool template failed');
if('late-server' in tools.mcp)throw Error('call mutated frozen namespace');
return {contract:true};`)
	if publications != 1 || effects != 1 {
		t.Fatalf("same-VM publication=%d effects=%d, want one each", publications, effects)
	}
}

func TestCodeModeSearchRefreshesModelFunctionInCurrentScript(t *testing.T) {
	t.Parallel()
	source := &changingBindingSource{}
	makeRemote := func(version, field string) tool.NamedTool {
		def := fixtureMCPDefinition(version)
		def.Metadata[mcp.MetadataModelFunction] = true
		def.Description = "Read docs " + version
		def.InputSchema = map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{field},
			"properties": map[string]any{field: map[string]any{"type": "string"}},
		}
		return tool.NamedTool{Def: def, Invoke: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			if binding := toolbinding.Target(ctx); binding == nil || *binding != mcp.BindingForDefinition(def) {
				return tool.Result{}, fmt.Errorf("wrong canonical binding for %s: %+v", version, binding)
			}
			return tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: call.Input}}}, nil
		}}
	}
	old := makeRemote("v1", "oldValue")
	current := makeRemote("v2", "newValue")
	source.publish(old)
	publish := tool.NamedTool{Def: tool.Definition{Name: "PublishRefresh", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		source.publish(current)
		return tool.Result{}, nil
	}}
	event, events := runSearchContractResult(t, source, []tool.Tool{old, publish}, `
if((await docs__read({oldValue:"old"})).data.oldValue!=="old")throw Error("old binding unavailable");
await PublishRefresh({});
let stale;
try { await docs__read({oldValue:"stale"}); } catch (e) { stale=e.code; }
if(stale!=="stale_revision")throw Error("expected stale binding before search: "+stale);
const page=await tools.search({query:"docs read"});
if(page.tools.length!==1||page.tools[0].description!=="Read docs v2")throw Error("directory did not refresh");
const entry=await tools.search({name:"docs__read"});
if(entry.input_schema.required[0]!=="newValue")throw Error("schema did not refresh");
{
  const docs__read=0;
  const args={newValue:"current"};
  const result=await eval(entry.call_path);
  return {contract:result.data.newValue==="current"};
}`)
	result, _ := event.Tool.Output["result"].(map[string]any)
	if event.Meta[codemode.MetadataScriptCompletion] != "completed" || result["contract"] != true {
		t.Fatalf("script did not recover from stale binding: %+v", event.Tool.Output)
	}
	if binding := codeModeBindings(events)[old.Def.Name]; binding != mcp.BindingForDefinition(current.Def) {
		t.Fatalf("search did not retain the refreshed binding: %+v", binding)
	}
}

func TestCodeModeSearchContractPagination(t *testing.T) {
	t.Parallel()
	source := &runtimeDeferredSource{}
	for index := 0; index < 20; index++ {
		def := fixtureMCPDefinition("v1")
		def.Name = fmt.Sprintf("docs__%02d", index)
		source.ready = append(source.ready, tool.NamedTool{Def: def})
	}
	runSearchContract(t, source, nil, `
 const page=await tools.search({query:"docs"});
 if(page.tools.length!==16||page.next!==page.tools[15].name)throw Error("bad first page");
 const tail=await tools.search({query:"docs",after:page.next});
 if(tail.tools.length!==4||tail.next!==null||tail.tools[0].name!== "docs__16")throw Error("bad tail");
 const empty=await tools.search({query:"no_such_keyword"});
 return {contract:empty.tools.length===0&&empty.next===null};`)
}

func TestCodeModeExactNameDoesNotFallBackToFilter(t *testing.T) {
	t.Parallel()
	source := &runtimeDeferredSource{}
	def := fixtureMCPDefinition("v1")
	source.publish(tool.NamedTool{Def: def})
	event, _ := runSearchContractResult(t, source, nil, `return await tools.search({name:"docs"});`)
	if event.Tool.Status != "failed" || !strings.Contains(fmt.Sprint(event.Tool.Output), "not_found") {
		t.Fatalf("exact miss = %+v", event.Tool.Output)
	}
}

func TestCodeModeSearchRanksServicesAndMethodsAndBatchesExactSchemas(t *testing.T) {
	t.Parallel()
	source := &runtimeDeferredSource{}
	makeRemote := func(name, server, method, description string) tool.NamedTool {
		def := fixtureMCPDefinition("rank-v1")
		def.Name, def.Description = name, description
		def.Metadata[mcp.MetadataServer], def.Metadata[mcp.MetadataTool] = server, method
		def.InputSchema = map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "integer"}}}
		return tool.NamedTool{Def: def}
	}
	target := makeRemote("z__target", "RecordService", "getRecord", "Exact method.")
	source.ready = append(source.ready, target)
	for i := 0; i < 17; i++ {
		name := fmt.Sprintf("a__%02d", i)
		source.ready = append(source.ready, makeRemote(name, "a", name, "Get a record from this service. "+strings.Repeat("中文", 90)))
	}
	source.ready = append(source.ready, makeRemote("not_a_match", "elsewhere", "getOnly", "Get from elsewhere."))
	huge := makeRemote("huge__describe", "huge", "describe", "Oversized exact schema.")
	huge.Def.InputSchema["description"] = strings.Repeat("oversized schema ", 10000)
	source.ready = append(source.ready, huge)
	native := tool.NamedTool{Def: tool.Definition{Name: "GetRecord", InputSchema: target.Def.InputSchema}}
	events := runSearchContract(t, source, []tool.Tool{native}, `
const query="record service get record";
const start=await tools.search({query});
if(start.tools.length!==16 || start.tools[0].name!=="z__target" || start.tools[0].call_path!=="globalThis.RecordService.getRecord(args)")throw Error("service/method ranking: "+JSON.stringify(start));
const tail=await tools.search({query,after:start.next});
const joined=[...start.tools,...tail.tools];
if(joined.length!==18 || new Set(joined.map(t=>t.name)).size!==18 || tail.next!==null)throw Error("ranked pagination lost results");
if(joined.some(t=>"input_schema" in t || Array.from(t.description).length>161))throw Error("directory was not lightweight");
const native=await tools.search({query:"get record"});
if(native.tools[0].name!=="GetRecord" || native.tools[0].call_path!=="globalThis.GetRecord(args)")throw Error("prefix missing direct syntax");
const batch=await tools.search({names:["unknown","GetRecord","huge__describe","z__target"]});
const [missing, prefix, huge, remote]=batch.tools;
if(missing.name!=="unknown"||missing.error_code!=="not_found"||huge.error_code!=="resource_exhausted")throw Error("batch status mismatch");
if(prefix.entry.input_schema.properties.value.type!=="integer"||remote.entry.input_schema.properties.value.type!=="integer")throw Error("missing exact schema");
if(prefix.entry.call_path!=="globalThis.GetRecord(args)"||remote.entry.output_format!=="body")throw Error("schema contract mismatch");
return {contract:true};`)
	bindings := codeModeBindings(events)
	if len(bindings) != 1 || bindings[target.Def.Name] != mcp.BindingForDefinition(target.Def) {
		t.Fatalf("batch bound anything beyond the delivered MCP schema: %v", bindings)
	}
}

func TestCodeModeLargeCatalogStartup(t *testing.T) {
	t.Parallel()
	for _, count := range []int{1200, 2000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			source := &jsExecSource{}
			var calls []string
			const server = "catalog_service_with_original_names"
			const plugin = "catalog_plugin_with_original_names"
			var routes []codemode.ToolRoute
			for i := range count {
				remote := fmt.Sprintf("catalog_operation_%04d_with_original_name", i)
				def := fixtureMCPDefinition("large-catalog-v1")
				def.Name = server + "__" + remote
				def.Metadata[mcp.MetadataServer] = server
				def.Metadata[mcp.MetadataTool] = remote
				def.Metadata[mcp.MetadataPluginID] = plugin
				routes = append(routes, codeModeToolRoute(def))
				source.items = append(source.items, tool.NamedTool{Def: def, Invoke: func(ctx context.Context, call tool.Call) (tool.Result, error) {
					calls = append(calls, call.Name)
					if parent := tool.ParentFromContext(ctx); parent == nil || parent.Kind != codemode.ToolKind || toolbinding.Target(ctx) == nil {
						t.Errorf("lost Runtime admission identity: %+v", call)
					}
					return tool.Result{}, nil
				}})
			}
			raw, _ := json.Marshal(routes)
			t.Logf("catalog routes=%d actual JSON bytes=%d reply budget=%d", count, len(raw), codemode.DefaultLimits().ReplyBytes)
			first, last := routes[0], routes[len(routes)-1]
			code := fmt.Sprintf(`await %s.%s({});await tools.plugins.%s.%s.%s({});await tools.call(%q,{});return null;`, server, first.Tool, plugin, server, last.Tool, last.Name)
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return h1Script(code)
				}
				return nil
			}}
			var metrics []gojaexec.Metrics
			events, rt := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{Observe: func(m gojaexec.Metrics) { metrics = append(metrics, m) }}}}, DeferredTools: source}, nil, nil)
			for _, event := range events {
				if event.Tool != nil && event.Tool.Name == codemode.ToolName && event.Tool.Status == "failed" {
					t.Errorf("script failed: %+v", event.Tool.Output)
				}
			}
			if strings.Join(calls, ",") != strings.Join([]string{first.Name, last.Name, last.Name}, ",") {
				t.Errorf("canonical calls=%v", calls)
			}
			if err := rt.ReleaseCodeModeEnvironments(); err != nil {
				t.Fatal(err)
			}
			if len(metrics) != 1 || !metrics[0].Reaped {
				t.Errorf("worker metrics=%+v", metrics)
			}
		})
	}
}

func TestJSExecCatalogSelectionIsEmbedderOwnedAndBounded(t *testing.T) {
	t.Parallel()
	store, active, _ := codeModeStore(t)
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	def := fixtureMCPDefinition("v1")
	def.Name = "custom__entry"
	def.Metadata[mcp.MetadataTool] = "query-docs"
	def.Description = strings.Repeat("界", 70)
	candidates := []tool.Tool{tool.NamedTool{Def: def}}
	text, err := rt.codeModeCatalog(t.Context(), active.SessionRef, &jsExecSource{items: candidates}, nil)
	if err != nil || strings.Contains(text, def.Name) {
		t.Fatalf("SDK imposed product selection: %s %v", text, err)
	}
	selected := map[string]any{codemode.MetadataSelectedTools: []any{def.Name}}
	again, err := rt.codeModeCatalog(t.Context(), active.SessionRef, &jsExecSource{items: candidates}, selected)
	if err != nil || again != text {
		t.Fatalf("late selection rewrote early prefix: %v", err)
	}
	second, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: active.AppName, UserID: active.UserID})
	if err != nil {
		t.Fatal(err)
	}
	text, err = rt.codeModeCatalog(t.Context(), second.SessionRef, &jsExecSource{items: candidates}, selected)
	if err != nil {
		t.Fatal(err)
	}
	var hints []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	start := strings.LastIndex(text, "\n[")
	if start < 0 || json.Unmarshal([]byte(text[start+1:]), &hints) != nil || len(hints) != 1 || hints[0].Name != def.Name || len(hints[0].Description) > 128 || !utf8.ValidString(hints[0].Description) || strings.Contains(text[start+1:], "input_schema") {
		t.Fatalf("generic bounded name/description snapshot=%s", text)
	}
}

func TestCodeModePromptDeliveryStaticAndDynamicRequests(t *testing.T) {
	t.Parallel()
	for _, dynamic := range []bool{false, true} {
		t.Run(map[bool]string{false: "static", true: "dynamic"}[dynamic], func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			const legacyKey = "code_mode_catalog_v10"
			const legacyGuidance = "LEGACY_V10_GUIDANCE"
			_, err := store.UpdateState(t.Context(), session.UpdateStateRequest{SessionRef: active.SessionRef, Update: func(state map[string]any) (map[string]any, error) {
				if state == nil {
					state = map[string]any{}
				}
				state[legacyKey] = legacyGuidance
				return state, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			def := fixtureMCPDefinition("v1")
			def.Name = "hints__read"
			def.Description = "prompt hint sentinel"
			source := &jsExecSource{items: []tool.Tool{tool.NamedTool{Def: def}}}
			llm := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return h1Script(`return {ok:true};`)
				}
				return nil
			}}
			tools := []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}
			spec := agent.AgentSpec{Model: llm, Tools: tools, DeferredTools: source, Metadata: map[string]any{"system_prompt": "PROFILE_INSTRUCTIONS", codemode.MetadataSelectedTools: []string{def.Name}}}
			if dynamic {
				spec.ResolveModelRequest = func(context.Context) (agent.ModelRequestSnapshot, error) {
					return agent.ModelRequestSnapshot{Model: llm, Tools: tools, DeferredTools: source, Instructions: []model.Part{model.NewTextPart("PROFILE_INSTRUCTIONS")}, Validate: func(context.Context) (func(), error) { return func() {}, nil }}, nil
				}
			}
			runCodeMode(t, store, active, spec, nil, nil)
			state, err := store.SnapshotState(t.Context(), active.SessionRef)
			if err != nil {
				t.Fatal(err)
			}
			catalog, ok := state[codemode.MetadataCatalog].(string)
			if !ok || !strings.HasPrefix(catalog, codemode.Guidance) || state[legacyKey] != legacyGuidance {
				t.Fatal("current guidance must have its own snapshot and preserve historical state")
			}
			if len(llm.requests) != 2 {
				t.Fatalf("requests=%d", len(llm.requests))
			}
			var prefix, declarations string
			expectedDefinition, err := json.Marshal(tool.ModelSpecs(tools)[0])
			if err != nil {
				t.Fatal(err)
			}
			for _, req := range llm.requests {
				var text strings.Builder
				for _, p := range req.Instructions {
					if p.Text != nil {
						text.WriteString(p.Text.Text)
					}
				}
				got := text.String()
				if !strings.HasPrefix(got, "PROFILE_INSTRUCTIONS") || strings.Count(got, catalog) != 1 || !strings.Contains(got, "prompt hint sentinel") || strings.Contains(got, legacyGuidance) {
					t.Fatalf("guidance propagation=%q", got)
				}
				for _, message := range req.Messages {
					if strings.Contains(message.TextContent(), codemode.Guidance) {
						t.Fatal("SDK guidance belongs in Instructions, not conversational messages")
					}
				}
				entryCount := 0
				for _, spec := range req.Tools {
					if spec.Function != nil && spec.Function.Name == codemode.ToolName {
						entryCount++
						actual, err := json.Marshal(spec)
						if err != nil || string(actual) != string(expectedDefinition) {
							t.Fatalf("entry definition changed during request assembly: %s, %v", actual, err)
						}
					}
				}
				if entryCount != 1 {
					t.Fatalf("js_exec definitions=%d", entryCount)
				}
				raw, _ := json.Marshal(req.Instructions)
				if prefix != "" && prefix != string(raw) {
					t.Fatal("instruction prefix changed within Turn")
				}
				prefix = string(raw)
				declared, err := json.Marshal(req.Tools)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(got+string(declared), "parallel_safe") {
					t.Fatal("internal scheduling capability leaked to model instructions or definitions")
				}
				if declarations != "" && declarations != string(declared) {
					t.Fatal("tool definitions changed within Turn")
				}
				declarations = string(declared)
				t.Logf("model instructions=%s\ndefinitions=%s", raw, declared)
			}
		})
	}
}

func TestCodeModeMinimalModelInterface(t *testing.T) {
	t.Parallel()
	store, active, _ := codeModeStore(t)
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	text, err := rt.codeModeCatalog(t.Context(), active.SessionRef, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	definition := (&codemode.Tool{Name: codemode.ToolName}).Definition()
	description := definition.Description
	guidance := codemode.Guidance
	for _, value := range []string{guidance, description} {
		for _, removed := range []string{"tools.describe", "output.text", "output.image", "batch_id", "records()", "native", "MCP", "approval", "authorization", "parallel_safe"} {
			if strings.Contains(value, removed) {
				t.Fatalf("model-facing text contains implementation detail %q", removed)
			}
		}
	}
	for _, preference := range []string{
		"Call known tools directly", "schemas from Skills", "Search for unknown tools or arguments",
		"Await dependent operations", "express independent work concurrently", "Runtime schedules execution",
		"Submit js_exec scripts sequentially", "observe the original Task to completion before the next script, or use reset:true to abort the old script",
		"Task controls work independently of the VM",
	} {
		if !strings.Contains(guidance, preference) {
			t.Fatalf("missing usage preference %q", preference)
		}
	}
	for _, protocol := range []string{"tools.search(", "input_schema", "call_path", "tools.unpack", "error.reply", "not_found", "too_many_matches", "stale_revision", "tools.call("} {
		if strings.Contains(guidance, protocol) {
			t.Fatalf("detailed API protocol belongs in the tool description, found %q in Guidance", protocol)
		}
	}
	for _, contract := range []string{
		"Top-level await", "Use return to select output (omitted: null)",
		"Top-level names persist in the active environment", "Later scripts can read, assign or redeclare them",
		"JavaScript scope and const rules still apply",
		"Reuse declared tool schemas", "(await Read(args)).data.content", "tools.call(name,args)",
		"await tools.search({query?,after?}) returns a plain page", "next:string|null", "Pass next as after",
		"await tools.search({name}) returns the exact entry and binds it", "input_schema", `output_format:"body"`, "output_schema describes .data", "call_path",
		"tools.search({names})", "{name,error_code}", "in input order",
		"result.data is explicit structured output", "checked against any output schema", "Missing data is undefined", "JSON null stays null",
		"result.text joins only text blocks", "Return the original Body to preserve raw JSON", "original result.media references", "Omit undefined",
		"error.result.text() joins text and raw JSON", "error.result.json() parses that string", "Return error.result whole or select its media references",
		"runtime.state()", "reset:true", "error.message, error.code and error.result",
		"stale_revision, search again and rebuild args", "Check child outcomes before retrying", "Never replay unknown outcomes",
	} {
		if !strings.Contains(description, contract) {
			t.Fatalf("missing js_exec contract %q", contract)
		}
	}
	successAPI := strings.SplitN(description, "Inspect error.message", 2)[0]
	for _, reader := range []string{".json()", ".text()"} {
		if strings.Contains(successAPI, reader) {
			t.Fatalf("diagnostic reader advertised as part of the success API: %s", reader)
		}
	}
	for _, legacy := range []string{"tools.unpack", "{parts:", "tools.describe", "output.text", "output.image"} {
		if strings.Contains(description, legacy) {
			t.Fatalf("legacy workflow still advertised: %s", legacy)
		}
	}
	for _, detail := range []string{"console.", "host Session", "resume", "release"} {
		if strings.Contains(description, detail) {
			t.Fatalf("js_exec description exposes unnecessary detail %q", detail)
		}
	}
	if strings.Index(description, "await Read(args)") > strings.Index(description, "tools.search(") {
		t.Fatal("direct entry must precede discovery")
	}
	properties := definition.InputSchema["properties"].(map[string]any)
	for name, contract := range map[string]string{
		"reset":         "without undoing its effects",
		"yield_time_ms": "not an execution timeout. After yield, use Task outside the VM with the original script handle, not a child command handle.",
	} {
		property := properties[name].(map[string]any)
		if !strings.Contains(property["description"].(string), contract) {
			t.Fatalf("missing js_exec %s contract %q", name, contract)
		}
	}

	again, err := rt.codeModeCatalog(t.Context(), active.SessionRef, nil, nil)
	if err != nil || again != text {
		t.Fatal("prefix changed")
	}
}
