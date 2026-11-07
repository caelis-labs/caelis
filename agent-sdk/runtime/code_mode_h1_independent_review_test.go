package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
)

type h1ReviewBatchModel struct {
	*h1Model
	batches []map[string]any
}

func (m *h1ReviewBatchModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	if len(req.Tools) == 1 && req.Tools[0].Function.Name == "InspectToolSchema" && len(req.Messages) == 1 {
		var catalog struct {
			Candidates []struct {
				Name string `json:"name"`
			} `json:"candidates"`
		}
		raw := req.Messages[0].TextContent()
		if err := json.Unmarshal([]byte(raw), &catalog); err != nil {
			panic(err)
		}
		m.batches = append(m.batches, map[string]any{"count": len(catalog.Candidates), "bytes": len(raw), "first": catalog.Candidates[0].Name, "last": catalog.Candidates[len(catalog.Candidates)-1].Name})
	}
	return m.h1Model.Generate(ctx, req)
}

func TestH1ReviewInitialAndReductionBatches(t *testing.T) {
	var results []map[string]any
	for _, route := range []string{"native", "js"} {
		s := h1Directory(2000)
		base := &h1Model{s: s, main: func(i int, _ *model.Request) []model.ToolCall {
			if i != 0 {
				return nil
			}
			if route == "js" {
				return h1Script(`output.text(await tools.search("find the synthetic last lookup"));`)
			}
			return []model.ToolCall{{ID: "query", Name: tool.ToolSearchToolName, Args: `{"query":"find the synthetic last lookup","limit":8}`}}
		}}
		m := &h1ReviewBatchModel{h1Model: base}
		spec := h1Spec(s, base)
		spec.Model = m
		if route == "native" {
			spec.Tools = []tool.Tool{h1Search(s)}
		}
		store, active, _ := codeModeStore(t)
		runCodeMode(t, store, active, spec, nil, nil)
		total := 0
		for _, batch := range m.batches {
			total += batch["count"].(int)
		}
		if total != 2000 || len(m.batches) != 7 || s.snapshot().SelectorCalls != 14 {
			t.Fatalf("batch candidates=%d batches=%v stats=%+v", total, m.batches, s.snapshot())
		}
		results = append(results, map[string]any{"route": route, "n": 2000, "batches": m.batches, "sum_candidates_over_all_batches": total, "stats": s.snapshot()})
	}
	if dir := os.Getenv("CAELIS_H1_REVIEW_EVIDENCE"); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, "initial-and-reduction-batches.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// This test-only observer preserves the original h1Tool clone and its counters.
// Aggregated caller stacks explain the existing counter; no production hook.
type h1ReviewCalls struct {
	mu     sync.Mutex
	stacks map[string]int
}
type h1ReviewTool struct {
	tool.Tool
	calls *h1ReviewCalls
}

func (t h1ReviewTool) Definition() tool.Definition {
	var pcs [18]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	var chain []string
	for {
		frame, more := frames.Next()
		if strings.Contains(frame.Function, "github.com/caelis-labs/caelis/") {
			chain = append(chain, strings.TrimPrefix(frame.Function, "github.com/caelis-labs/caelis/")+fmt.Sprintf(":%d", frame.Line))
		}
		if !more {
			break
		}
	}
	t.calls.mu.Lock()
	t.calls.stacks[strings.Join(chain, " <- ")]++
	t.calls.mu.Unlock()
	return t.Tool.Definition()
}
func (c *h1ReviewCalls) reset() { c.mu.Lock(); defer c.mu.Unlock(); c.stacks = map[string]int{} }
func (c *h1ReviewCalls) copy() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]int{}
	for k, v := range c.stacks {
		out[k] = v
	}
	return out
}

func TestH1ReviewNativeJSDefinitionCosts(t *testing.T) {
	var results []map[string]any
	constantCounts := map[string]int{}
	for _, n := range []int{2, 20, 200, 2000} {
		for _, route := range []string{"native", "js"} {
			t.Run(fmt.Sprintf("%s/%d", route, n), func(t *testing.T) {
				s := h1Directory(n)
				// The delivered fixture reuses one underlying MCP identity for all
				// names. This control verifies that it does not inflate call counts.
				if os.Getenv("CAELIS_H1_REVIEW_DISTINCT_IDENTITY") == "1" {
					for i, one := range s.items {
						item := one.(h1Tool)
						item.def.Metadata[tool.MetadataMCPTool] = item.def.Name
						s.items[i] = item
					}
				}
				trace := &h1ReviewCalls{stacks: map[string]int{}}
				for i, one := range s.items {
					s.items[i] = h1ReviewTool{one, trace}
				}
				var source tool.Source = s
				if os.Getenv("CAELIS_H1_REVIEW_LOOKUP") == "1" {
					source = h1Indexed(s)
				}
				store, active, _ := codeModeStore(t)
				m := &h1Model{s: s}
				spec := func() agent.AgentSpec {
					result := h1Spec(s, m)
					search := h1Discovery{s: s, base: toolsearch.NewSource(source, h1Ranker{s})}
					result.DeferredTools = source
					result.Tools[0] = search
					if route == "native" {
						result.Tools = []tool.Tool{search}
					}
					return result
				}
				m.main = func(i int, _ *model.Request) []model.ToolCall {
					if i != 0 {
						return nil
					}
					if route == "js" {
						return h1Script(`output.text(await tools.describe("z__target"));`)
					}
					return []model.ToolCall{{ID: "exact", Name: tool.ToolSearchToolName, Args: `{"name":"z__target"}`}}
				}
				var rt *Runtime
				trace.reset()
				exact := h1Measure(s, "exact_last", func() { _, rt = runCodeMode(t, store, active, spec(), nil, nil) })
				exactStacks := trace.copy()
				if exact.Stats.ExactRequests != 1 || exact.Stats.SelectorCalls != 0 || exact.Stats.Effects != 0 {
					t.Fatalf("exact=%+v", exact)
				}
				// Populate the other target identically before measuring continuation.
				m.mainCalls = 0
				m.main = func(i int, _ *model.Request) []model.ToolCall {
					if i != 0 {
						return nil
					}
					if route == "js" {
						return h1Script(`output.text(await tools.describe("a__target"));`)
					}
					return []model.ToolCall{{ID: "other", Name: tool.ToolSearchToolName, Args: `{"name":"a__target"}`}}
				}
				h1Run(t, rt, active, spec())
				trace.reset()
				continuation := h1Measure(s, "next_turn_direct_calls", func() {
					m.mainCalls = 0
					m.main = func(i int, _ *model.Request) []model.ToolCall {
						if i != 0 {
							return nil
						}
						if route == "js" {
							return h1Script(`await tools.call("a__target",{value:2});await tools.call("z__target",{value:2});`)
						}
						return []model.ToolCall{{ID: "a", Name: tool.MCPCallToolName, Args: `{"tool_name":"a__target","arguments":{"value":2}}`}, {ID: "z", Name: tool.MCPCallToolName, Args: `{"tool_name":"z__target","arguments":{"value":2}}`}}
					}
					h1Run(t, rt, active, spec())
				})
				if continuation.Stats.ExactRequests != 0 || continuation.Stats.QueryRequests != 0 || continuation.Stats.SelectorCalls != 0 || continuation.Stats.Effects != 2 {
					t.Fatalf("continuation=%+v", continuation)
				}
				for _, phase := range []struct {
					phase  h1Phase
					stacks map[string]int
				}{{exact, exactStacks}, {continuation, trace.copy()}} {
					total := 0
					for _, count := range phase.stacks {
						total += count
					}
					if total != phase.phase.Stats.DefinitionReads {
						t.Fatalf("stack total %d != Definition count %d", total, phase.phase.Stats.DefinitionReads)
					}
					if os.Getenv("CAELIS_H1_REVIEW_LOOKUP") == "1" {
						key := route + "/" + phase.phase.Name
						if previous, exists := constantCounts[key]; exists && previous != total {
							t.Fatalf("indexed known path grew with N: %s N=%d count=%d baseline=%d", key, n, total, previous)
						}
						constantCounts[key] = total
						if phase.phase.Stats.ToolsEnumerations != 0 || phase.phase.Stats.SearchEnumerations != 0 {
							t.Fatalf("indexed path enumerated directory: %+v", phase.phase.Stats)
						}
					}
					results = append(results, map[string]any{"n": n, "route": route, "phase": phase.phase, "definition_caller_stacks": phase.stacks})
				}
			})
		}
	}
	if dir := os.Getenv("CAELIS_H1_REVIEW_EVIDENCE"); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		raw, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "native-js-definition-costs.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
