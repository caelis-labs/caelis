package mcp

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCatalogLookupReturnsPublishedTool(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "lookup", Version: "1"}, &mcpsdk.ServerOptions{PageSize: 1})
	var effects atomic.Int32
	catalogTool(server, "item-a", "first receipt", &effects)
	catalogTool(server, "item-b", "second receipt", &effects)
	manager := catalogManager(t, server)
	for _, tc := range []struct{ name, receipt string }{
		{"local__item_a", "first receipt"},
		{"local__item_b", "second receipt"},
	} {
		one, found, err := manager.Lookup(t.Context(), tc.name)
		if err != nil || !found || one.Definition().Name != tc.name {
			t.Fatalf("exact lookup %q: found=%v, err=%v", tc.name, found, err)
		}
		manager.mu.Lock()
		published := manager.toolsByName[tc.name]
		manager.mu.Unlock()
		if one != published {
			t.Fatalf("lookup %q returned a different callable from the published entry", tc.name)
		}
		if err := manager.CheckToolBinding(t.Context(), BindingForDefinition(one.Definition())); err != nil {
			t.Fatal(err)
		}
		result, err := one.Call(t.Context(), tool.Call{ID: tc.name, Name: tc.name, Input: []byte(`{}`)})
		if err != nil || len(result.Content) != 1 || result.Content[0] != tool.Text(tc.receipt) {
			t.Fatalf("call %q returned %+v, %v", tc.name, result, err)
		}
	}
	if one, found, err := manager.Lookup(t.Context(), "local__item_missing"); one != nil || found || err != nil {
		t.Fatalf("missing lookup returned %v, %v, %v", one, found, err)
	}
	if got := effects.Load(); got != 2 {
		t.Fatalf("remote effects = %d, want 2", got)
	}
}

func BenchmarkCatalogLookupAndBinding(b *testing.B) {
	for _, size := range []int{2, 20, 200, 2000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "lookup", Version: "1"}, &mcpsdk.ServerOptions{PageSize: 2000})
			for i := range size {
				catalogTool(server, fmt.Sprintf("item%04d", i), "synthetic lookup", nil)
			}
			manager := catalogManager(b, server)
			name := fmt.Sprintf("local__item%04d", size-1)
			one, found, err := manager.Lookup(b.Context(), name)
			if err != nil || !found {
				b.Fatalf("lookup %q: found=%v, err=%v", name, found, err)
			}
			binding := BindingForDefinition(one.Definition())
			b.ReportAllocs()
			for b.Loop() {
				if _, found, err := manager.Lookup(b.Context(), name); err != nil || !found {
					b.Fatalf("lookup %q: found=%v, err=%v", name, found, err)
				}
				if err := manager.CheckToolBinding(b.Context(), binding); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
