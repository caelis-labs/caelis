package registry

import (
	"context"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestMemoryRegistryRegistersAndLooksUpTools(t *testing.T) {
	t.Parallel()

	echo := tool.NamedTool{
		Def: tool.Definition{Name: "echo"},
	}
	reg, err := NewMemory(echo)
	if err != nil {
		t.Fatalf("NewMemory() error = %v", err)
	}
	list, err := reg.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got, want := len(list), 1; got != want {
		t.Fatalf("len(list) = %d, want %d", got, want)
	}
	item, ok, err := reg.Lookup(context.Background(), "ECHO")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if !ok || item == nil {
		t.Fatal("Lookup() = nil, want tool")
	}
	if got := item.Definition().Name; got != "echo" {
		t.Fatalf("tool name = %q, want %q", got, "echo")
	}
}
