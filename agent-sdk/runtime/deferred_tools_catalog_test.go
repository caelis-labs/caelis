package runtime

import (
	"context"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"testing"
)

type preferredCatalogSource struct {
	toolsCalls, searchCalls int
	ready                   []tool.Tool
}

func (s *preferredCatalogSource) Tools() []tool.Tool       { s.toolsCalls++; return nil }
func (s *preferredCatalogSource) SearchTools() []tool.Tool { s.searchCalls++; return s.ready }

func TestDeferredToolsUsesDispatchableCatalogWithoutDiscardedEnumeration(t *testing.T) {
	calls, wraps := 0, 0
	remote := tool.NamedTool{Def: fixtureMCPDefinition("catalog-v1"), Invoke: func(context.Context, tool.Call) (tool.Result, error) { calls++; return tool.Result{}, nil }}
	source := &preferredCatalogSource{ready: []tool.Tool{nil, tool.NamedTool{Def: tool.Definition{Name: "native_injection"}}, remote}}
	deferred := &deferredToolSource{source: source, wrap: func(items []tool.Tool) []tool.Tool {
		wraps++
		if len(items) != 1 {
			t.Fatalf("MCP filter bypass: %d", len(items))
		}
		return items
	}}
	result := deferred.Tools()
	if source.toolsCalls != 0 || source.searchCalls != 1 || wraps != 1 || len(result) != 1 {
		t.Fatalf("catalog selection source=%+v wraps=%d tools=%d", source, wraps, len(result))
	}
	if _, err := result[0].Call(t.Context(), tool.Call{}); err != nil || calls != 1 {
		t.Fatalf("real target changed: calls=%d err=%v", calls, err)
	}
	fallback := &runtimeDeferredSource{ready: []tool.Tool{remote}}
	deferred.source = fallback
	if len(deferred.Tools()) != 1 || wraps != 2 {
		t.Fatal("Tools-only source fallback lost")
	}
}
