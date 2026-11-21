package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

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
