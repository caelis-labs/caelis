package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type lookupProbe struct {
	manager                            *Manager
	enumerations, lookups, definitions int
}

func (p *lookupProbe) Tools() []tool.Tool { p.enumerations++; return p.manager.Tools() }
func (p *lookupProbe) Lookup(ctx context.Context, name string) (tool.Tool, bool, error) {
	p.lookups++
	one, found, err := p.manager.Lookup(ctx, name)
	if err != nil || !found {
		return one, found, err
	}
	return lookupProbeTool{one, p}, true, nil
}

type lookupProbeTool struct {
	tool.Tool
	probe *lookupProbe
}

func (t lookupProbeTool) Definition() tool.Definition {
	t.probe.definitions++
	return t.Tool.Definition()
}

func TestCatalogLookupRealManagerDirectoryScale(t *testing.T) {
	var results []map[string]any
	for _, n := range []int{2, 20, 200, 2000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "lookup", Version: "1"}, &mcpsdk.ServerOptions{PageSize: 2000})
			var effects atomic.Int32
			for i := range n {
				catalogTool(server, fmt.Sprintf("item%04d", i), "synthetic lookup", &effects)
			}
			manager := catalogManager(t, server, true)
			probe := &lookupProbe{manager: manager}
			search := toolsearch.NewSource(probe, toolsearch.NewLexicalRanker())
			start := time.Now()
			for _, i := range []int{0, n / 2, n - 1} {
				name := fmt.Sprintf("local__item%04d", i)
				raw, _ := json.Marshal(map[string]any{"name": name})
				result, err := search.Call(t.Context(), tool.Call{Input: raw})
				if err != nil || result.IsError {
					t.Fatalf("exact lookup %s: %+v %v", name, result, err)
				}
			}
			if probe.enumerations != 0 || probe.lookups != 6 || probe.definitions != 6 {
				t.Fatalf("exact path enumerated or materialized unrelated definitions: %+v", probe)
			}
			exactWall := time.Since(start)
			before := probe.definitions
			start = time.Now()
			var receipts []tool.Result
			for _, i := range []int{0, n - 1} {
				name := fmt.Sprintf("local__item%04d", i)
				one, found, err := probe.Lookup(t.Context(), name)
				if err != nil || !found {
					t.Fatalf("current target unavailable %s %v", name, err)
				}
				def := one.Definition()
				binding := tool.BindingForDefinition(def)
				if err := manager.CheckToolBinding(t.Context(), binding); err != nil {
					t.Fatal(err)
				}
				if aliases, ok := def.Metadata[tool.MetadataReplayAliases].([]string); ok && len(aliases) > 0 {
					if _, found, err := manager.Lookup(t.Context(), aliases[0]); err != nil || found {
						t.Fatal("replay alias became a canonical candidate")
					}
				}
				result, err := one.Call(t.Context(), tool.Call{ID: name, Name: name, Input: []byte(`{}`)})
				if err != nil || result.IsError || result.ID != name {
					t.Fatalf("real MCP call lost original result: %+v %v", result, err)
				}
				receipts = append(receipts, result)
			}
			if probe.enumerations != 0 || probe.definitions-before != 2 || effects.Load() != 2 {
				t.Fatalf("known path counts probe=%+v effects=%d", probe, effects.Load())
			}
			results = append(results, map[string]any{"n": n, "classification": "real loopback MCP Manager/ToolSearch/call; no provider model", "catalog_enumerations": probe.enumerations, "exact_lookups": 6, "exact_target_definition_reads": 6, "known_target_definition_reads": 2, "actual_effects": effects.Load(), "original_results": receipts, "exact_wall_ns": exactWall.Nanoseconds(), "known_wall_ns": time.Since(start).Nanoseconds(), "counting": "proxy observes source enumeration and returned target Definition; Manager internal path inspected to use current raw definition, not instrumented"})
		})
	}
	if directory := os.Getenv("CAELIS_LOOKUP_EVIDENCE"); directory != "" {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(filepath.Join(directory, "real-manager-scale.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
