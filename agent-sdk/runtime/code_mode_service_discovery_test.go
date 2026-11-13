package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/mcpdiscovery"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

func TestCodeModeConcurrentSearchFailuresRetainEachAttempt(t *testing.T) {
	entered, release := make(chan struct{}, 2), make(chan struct{})
	host := &codeModeHost{ctx: t.Context(), owner: runtimeCodeModeTool{lookup: func(context.Context, string) (tool.Tool, bool, error) {
		entered <- struct{}{}
		<-release
		return nil, false, tool.NewError(tool.ErrorCodeNotFound, "directory unavailable")
	}}}
	done := make(chan error, 2)
	for _, query := range []string{"first", "second"} {
		go func() {
			_, err := host.Search(t.Context(), codemode.SearchRequest{Query: query})
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

func TestCodeModeServiceDiscoveryReportsOmissions(t *testing.T) {
	store, active, _ := codeModeStore(t)
	source := h1Directory(20)
	m := &h1Model{s: source, main: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return h1Script(`const s=await tools.search({server:"docs"});if(!s.truncated||s.count<1||s.count>16||s.count+s.omitted_count!==20)throw Error("missing omissions "+JSON.stringify(s));return s;`)
		}
		return nil
	}}
	spec := agent.AgentSpec{Model: m, Tools: []tool.Tool{toolsearch.NewSource(source), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}, DeferredTools: source}
	runCodeMode(t, store, active, spec, nil, nil)
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	searches := 0
	for _, event := range events {
		if event.Tool != nil && event.Tool.Status == "failed" {
			t.Fatalf("failed %s: %+v", event.Tool.Name, event.Tool.Output)
		}
		if selected, found := serviceDiscoveryReceipt(t, event); found {
			if !selected.Truncated || selected.Count+selected.OmittedCount != 20 {
				t.Fatalf("canonical omissions = %+v", selected)
			}
			searches++
		}
	}
	if searches != 1 {
		t.Fatalf("canonical searches=%d, want 1", searches)
	}
}

func TestCodeModeServiceDiscoveryCanonicalReceiptAcrossVMs(t *testing.T) {
	store, active, _ := codeModeStore(t)
	source := h1Directory(3)
	other := source.items[2].(h1Tool)
	other.def.Metadata[tool.MetadataPluginID] = "other"
	source.items[2] = other
	m := &h1Model{s: source, main: func(i int, _ *model.Request) []model.ToolCall {
		switch i {
		case 0:
			return h1Script(`const s=await tools.search({server:"docs",plugin:"fixture"});if(s.count!==2||s.truncated||"omitted_count" in s)throw Error("service result "+JSON.stringify(s));if(s.tools[0].name!=="a__target"||s.tools[1].name!=="m_0001__lookup")throw Error("wrong metadata filter/order");for(const t of s.tools){if(t.input_schema.required[0]!=="value"||"parallel_safe" in t)throw Error("schema contract");}return await tools.call(s.tools[0].name,{value:1});`)
		case 1:
			return h1Script(`return await tools.call("m_0001__lookup",{value:2});`)
		}
		return nil
	}}
	// The default model selector would call this fixture's main model. Service
	// discovery must succeed without a selector conversation or lexical ranker.
	spec := agent.AgentSpec{Model: m, Tools: []tool.Tool{toolsearch.NewSource(source), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}, DeferredTools: source}
	runCodeMode(t, store, active, spec, nil, nil)
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	receipts := 0
	for _, event := range events {
		if event.Tool != nil && event.Tool.Status == "failed" {
			t.Fatalf("failed %s: %+v", event.Tool.Name, event.Tool.Output)
		}
		if selected, found := serviceDiscoveryReceipt(t, event); found {
			if selected.Count != 2 || selected.Truncated || selected.OmittedCount != 0 {
				t.Fatalf("canonical discovery receipt = %+v", selected)
			}
			for _, entry := range selected.Tools {
				if entry.Binding == nil || entry.Binding.Identity == "" || entry.Binding.Version == "" {
					t.Fatalf("receipt lacks binding: %+v", entry)
				}
			}
			receipts++
		}
	}
	if receipts != 1 || source.snapshot().Effects != 2 {
		t.Fatalf("receipts=%d stats=%+v; want one canonical search and two effects across fresh VMs", receipts, source.snapshot())
	}
}

func serviceDiscoveryReceipt(t *testing.T, event *session.Event) (tool.ToolSearchResult, bool) {
	t.Helper()
	if event.Journal == nil || event.Journal.ToolExecution == nil {
		return tool.ToolSearchResult{}, false
	}
	record := event.Journal.ToolExecution
	if record.ToolName != tool.ToolSearchToolName || record.Status != session.ToolExecutionSucceeded || record.Parent == nil || record.Parent.Kind != "code_mode" {
		return tool.ToolSearchResult{}, false
	}
	var result tool.Result
	if err := json.Unmarshal(record.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.IsError || result.Metadata[mcpdiscovery.MetadataKey] != true {
		t.Fatalf("search receipt lacks successful admission: %+v", result)
	}
	for _, part := range result.Content {
		if part.JSON != nil {
			var selected tool.ToolSearchResult
			if err := json.Unmarshal(part.JSON.Value, &selected); err != nil {
				t.Fatal(err)
			}
			return selected, true
		}
	}
	t.Fatal("search receipt lacks JSON result")
	return tool.ToolSearchResult{}, false
}

func TestCodeModeServicePaginationCanonicalBindingsAcrossVMs(t *testing.T) {
	for _, heavy := range []bool{false, true} {
		t.Run(map[bool]string{false: "limit", true: "schema budget"}[heavy], func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			source := h1Directory(37)
			if heavy {
				for i, item := range source.items {
					one := item.(h1Tool)
					one.def.InputSchema["description"] = strings.Repeat("complete schema ", 400)
					source.items[i] = one
				}
			}
			m := &h1Model{s: source, main: func(i int, _ *model.Request) []model.ToolCall {
				switch i {
				case 0:
					return h1Script(`const seen=new Set();let after;let pages=0;do{const q={server:"docs",plugin:"fixture"};if(after)q.after=after;const s=await tools.search(q);if(s.count===0||s.count>16)throw Error("empty/large page");for(const t of s.tools){if(seen.has(t.name))throw Error("repeated tool");seen.add(t.name);}if(s.next&&s.next!==s.tools[s.count-1].name)throw Error("bad anchor");after=s.next;if(++pages>37)throw Error("loop");}while(after);if(seen.size!==37||pages<3)throw Error("missing tools");return seen.size;`)
				case 1:
					return h1Script(`return await tools.call("z__target",{value:2});`)
				}
				return nil
			}}
			spec := agent.AgentSpec{Model: m, Tools: []tool.Tool{toolsearch.NewSource(source), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}, DeferredTools: source}
			runCodeMode(t, store, active, spec, nil, nil)
			events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			for _, event := range events {
				if event.Tool != nil && event.Tool.Status == "failed" {
					t.Fatalf("failed %s: %+v", event.Tool.Name, event.Tool.Output)
				}
				if page, found := serviceDiscoveryReceipt(t, event); found {
					if page.Next != "" && page.Next != page.Tools[len(page.Tools)-1].Name {
						t.Fatal("canonical anchor differs")
					}
					for _, entry := range page.Tools {
						if entry.Binding == nil {
							t.Fatal("missing canonical binding")
						}
						seen[entry.Name] = true
					}
				}
			}
			if len(seen) != 37 || source.snapshot().Effects != 1 {
				t.Fatalf("bindings=%d stats=%+v", len(seen), source.snapshot())
			}
		})
	}
}

func TestCodeModeServiceFiltersEligibilityBeforeLimit(t *testing.T) {
	store, active, _ := codeModeStore(t)
	source := h1Directory(20)
	for i := 0; i < 16; i++ {
		one := source.items[i].(h1Tool)
		if i < 8 {
			one.def.RequiredModelCapabilities = model.Capabilities{ImageInput: true}
		} else {
			one.def.ExecutionRequirements = &tool.ExecutionRequirements{Sandbox: sandbox.CapabilitySet{CommandExec: true}}
		}
		source.items[i] = one
	}
	m := &h1Model{s: source, main: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return h1Script(`const s=await tools.search({server:"docs"});if(s.count!==4||s.truncated||s.next||s.omitted_count)throw Error("eligibility after limit: "+JSON.stringify(s));return await tools.call("z__target",{value:1});`)
		}
		return nil
	}}
	runCodeMode(t, store, active, agent.AgentSpec{Model: m, Tools: []tool.Tool{toolsearch.NewSource(source), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}, DeferredTools: source}, nil, nil)
	if source.snapshot().Effects != 1 {
		t.Fatalf("later eligible tools were hidden: %+v", source.snapshot())
	}
}

func TestCodeModeServiceFiltersCurrentRuntimeSourceBeforeLimit(t *testing.T) {
	store, active, _ := codeModeStore(t)
	source := h1Directory(20)
	// Discovery's catalog is broader than the assembled execution source. The
	// current Runtime lookup, rather than this catalog, owns source eligibility.
	search := toolsearch.NewWithRanker(append([]tool.Tool(nil), source.items...), nil)
	source.items = source.items[16:]
	m := &h1Model{s: source, main: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return h1Script(`const s=await tools.search({server:"docs"});if(s.count!==4||s.truncated||s.next)throw Error("source scope applied after limit: "+JSON.stringify(s));return await tools.call("z__target",{value:1});`)
		}
		return nil
	}}
	runCodeMode(t, store, active, agent.AgentSpec{Model: m, Tools: []tool.Tool{search, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}, DeferredTools: source}, nil, nil)
	if source.snapshot().Effects != 1 {
		t.Fatalf("current source tools were hidden: %+v", source.snapshot())
	}
}
