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
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

// These counters observe existing source/selector interfaces. They do not
// replace Runtime discovery, binding, authorization, or its journal.
type h1Stats struct {
	LookupRequests            int   `json:"source_lookup_requests"`
	ToolsEnumerations         int   `json:"tools_enumerations"`
	SearchEnumerations        int   `json:"search_enumerations"`
	EnumeratedItems           int   `json:"enumerated_items"`
	DefinitionReads           int   `json:"definition_schema_materializations"`
	ExactRequests             int   `json:"exact_schema_requests"`
	QueryRequests             int   `json:"query_requests"`
	SelectorSchemaReads       int   `json:"selector_schema_reads"`
	ResultSchemaRevalidations int   `json:"result_schema_revalidations"`
	SelectorBatches           int   `json:"selector_batches_including_reduction"`
	SelectorCalls             int   `json:"selector_model_calls"`
	MaxCatalogBytes           int   `json:"max_selector_catalog_bytes"`
	SelectorCatalogBytes      int   `json:"selector_catalog_bytes_with_repeated_steps"`
	Effects                   int   `json:"actual_effects"`
	EnumerationNS             int64 `json:"enumeration_ns"`
	DefinitionNS              int64 `json:"definition_materialization_ns"`
	RankerNS                  int64 `json:"ranker_ns_inclusive"`
	SelectorNS                int64 `json:"selector_model_ns"`
}
type h1Source struct {
	mu    sync.Mutex
	items []tool.Tool
	stats h1Stats
	seen  map[string]bool
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
	s.seen = map[string]bool{}
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
	return tool.Result{Content: []model.Part{model.NewJSONPart([]byte(`{"value":42}`))}}, nil
}
func h1Directory(n int) *h1Source {
	s := &h1Source{seen: map[string]bool{}}
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
		def.Description = tool.ExternalCapabilityDescriptionPrefix + " " + strings.Repeat("Read a synthetic lookup value with an integer argument. ", 3)
		def.Metadata[tool.MetadataMCPSourceTrusted] = true
		def.InputSchema = map[string]any{"type": "object", "additionalProperties": false, "required": []string{"value"}, "properties": map[string]any{"value": map[string]any{"type": "integer"}}}
		s.items = append(s.items, h1Tool{s, def})
	}
	return s
}

type h1Ranker struct{ s *h1Source }

func (r h1Ranker) Rank(ctx context.Context, q string, defs []tool.Definition, limit int, selected toolsearch.SearchModel) ([]string, error) {
	start := time.Now()
	read := selected.ReadSchema
	selected.ReadSchema = func(ctx context.Context, name string) (tool.Definition, error) {
		r.s.mu.Lock()
		r.s.stats.SelectorSchemaReads++
		r.s.mu.Unlock()
		return read(ctx, name)
	}
	names, err := toolsearch.NewAgentRanker().Rank(ctx, q, defs, limit, selected)
	r.s.mu.Lock()
	r.s.stats.RankerNS += time.Since(start).Nanoseconds()
	r.s.mu.Unlock()
	return names, err
}

type h1Discovery struct {
	base tool.Tool
	s    *h1Source
}

func (t h1Discovery) Definition() tool.Definition { return t.base.Definition() }
func (t h1Discovery) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	var input struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(call.Input, &input)
	t.s.mu.Lock()
	if input.Name != "" {
		t.s.stats.ExactRequests++
	} else {
		t.s.stats.QueryRequests++
	}
	t.s.mu.Unlock()
	result, err := t.base.Call(ctx, call)
	// The existing ranker revalidates each selected name through readSchema.
	// This is a result-bound count, separate from actual selector callback reads.
	if input.Name == "" && err == nil && !result.IsError {
		for _, part := range result.Content {
			if part.JSON != nil {
				var found tool.ToolSearchResult
				_ = json.Unmarshal(part.JSON.Value, &found)
				t.s.mu.Lock()
				t.s.stats.ResultSchemaRevalidations += len(found.Tools)
				t.s.mu.Unlock()
			}
		}
	}
	return result, err
}
func h1Search(s *h1Source) tool.Tool { return h1Discovery{toolsearch.NewSource(s, h1Ranker{s}), s} }

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
		if len(req.Tools) == 1 && req.Tools[0].Function != nil && req.Tools[0].Function.Name == "InspectToolSchema" {
			start := time.Now()
			var catalog struct {
				Candidates []struct {
					Name string `json:"name"`
				} `json:"candidates"`
			}
			raw := req.Messages[0].TextContent()
			if err := json.Unmarshal([]byte(raw), &catalog); err != nil {
				yield(nil, err)
				return
			}
			if len(catalog.Candidates) == 0 {
				yield(nil, fmt.Errorf("empty fixture catalog"))
				return
			}
			chosen := catalog.Candidates[0].Name
			for _, candidate := range catalog.Candidates {
				if candidate.Name == "z__target" {
					chosen = candidate.Name
				}
			}
			m.s.mu.Lock()
			m.s.stats.SelectorCalls++
			m.s.stats.MaxCatalogBytes = max(m.s.stats.MaxCatalogBytes, len(raw))
			m.s.stats.SelectorCatalogBytes += len(raw)
			if len(req.Messages) == 1 {
				m.s.stats.SelectorBatches++
				for _, candidate := range catalog.Candidates {
					m.s.seen[candidate.Name] = true
				}
			}
			m.s.mu.Unlock()
			var message model.Message
			if len(req.Messages) == 1 {
				args, _ := json.Marshal(map[string]any{"name": chosen})
				message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "inspect", Name: "InspectToolSchema", Args: string(args)}}, "")
			} else {
				selection, _ := json.Marshal(map[string]any{"tools": []string{chosen}})
				message = model.NewTextMessage(model.RoleAssistant, string(selection))
			}
			yield(model.StreamEventFromResponse(&model.Response{Message: message, TurnComplete: true, Status: model.ResponseStatusCompleted}), nil)
			m.s.mu.Lock()
			m.s.stats.SelectorNS += time.Since(start).Nanoseconds()
			m.s.mu.Unlock()
			return
		}
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
	return agent.AgentSpec{Model: m, Tools: []tool.Tool{h1Search(s), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}, DeferredTools: s, Metadata: map[string]any{codemode.MetadataSelectedTools: []string{"a__target", "z__target"}}}
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
					return h1Script(`globalThis.local=1;return [await tools.search("a__target"),await tools.search("z__target")];`)
				case 1:
					return h1Script(`if(typeof local!=="undefined")throw Error("VM retained variable");await tools.call("a__target",{value:1});await tools.call("z__target",{value:1});`)
				}
				return nil
			}}
			_, rt := runCodeMode(t, store, active, h1Spec(s, m), registry, nil)
			if got := s.snapshot(); got.ExactRequests != 2 || got.QueryRequests != 0 || got.SelectorCalls != 0 || got.Effects != 2 {
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
			if got := s.snapshot(); got.ExactRequests != 0 || got.QueryRequests != 0 || got.SelectorCalls != 0 || got.Effects != 2 {
				t.Fatalf("new Turn/VM rediscovered or missed effects: %+v", got)
			}
			if change == "permission" {
				deny = true
			} else {
				s.mu.Lock()
				item := s.items[0].(h1Tool)
				item.def.InputSchema = map[string]any{"type": "object", "properties": map[string]any{"changed": map[string]any{"type": "string"}}}
				item.def.Metadata[tool.MetadataMCPSourceFingerprint] = "h1-v2"
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

func TestJSExecH1HintWarningOncePreservesCapability(t *testing.T) {
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
	if strings.Count(text, tool.ExternalCapabilityDescriptionPrefix) != 1 || !strings.Contains(text, "Read a synthetic lookup") || !strings.Contains(text, "{tools:[{name,description,input_schema}],count,truncated,omitted_count?,next?}") || strings.Contains(text, "parallel_safe") {
		t.Fatalf("H1 guidance/hint semantics lost: %s", text)
	}
	if !strings.Contains(text, "tools.call(name,args)") || !strings.Contains(text, "input_schema") || strings.Contains(text, "tools.describe") || strings.Contains(text, "output.") {
		t.Fatal("minimal search/call guidance absent")
	}
	state, _ := store.SnapshotState(t.Context(), active.SessionRef)
	if state["code_mode_catalog_v2"] != "legacy guidance requiring parallel_safe" || state["code_mode_catalog_v3"] != "legacy guidance without service continuation" {
		t.Fatal("changed a historical API snapshot")
	}
	again, err := rt.codeModeCatalog(t.Context(), active.SessionRef, nil, nil)
	if err != nil || again != state[codeModeCatalogKey] {
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
		item.def.Description = tool.ExternalCapabilityDescriptionPrefix + " " + strings.Repeat("读取语义", 50)
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
			return h1Script(`return await tools.search({name:"unknown"});`)
		}
		return nil
	}}
	events, _ := runCodeMode(t, store, active, h1Spec(s, m), nil, nil)
	if s.snapshot().QueryRequests != 0 {
		t.Fatal("invalid query reached discovery")
	}
	for _, e := range events {
		if e.Tool != nil && e.Tool.Name == codemode.ToolName && e.Tool.Status == "failed" {
			return
		}
	}
	t.Fatal("invalid query was hidden")
}
