package tool

import (
	"context"
	"testing"
)

type dummyTool struct{ name string }

func (t *dummyTool) Definition() Definition {
	return Definition{Name: t.name, Schema: Schema{Type: "object"}}
}
func (t *dummyTool) Run(_ Context, _ Call) (Result, error) {
	return Result{Output: "ok"}, nil
}

func TestMemoryRegistry_RegisterAndLookup(t *testing.T) {
	r := NewMemoryRegistry()
	r.Register(&dummyTool{"A"})
	r.Register(&dummyTool{"B"})

	if r.Count() != 2 {
		t.Fatalf("count: got %d, want 2", r.Count())
	}

	tool, ok, err := r.Lookup(context.Background(), "A")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !ok || tool == nil {
		t.Fatal("expected to find tool A")
	}

	_, ok, _ = r.Lookup(context.Background(), "missing")
	if ok {
		t.Error("expected not found for missing tool")
	}
}

func TestMemoryRegistry_RegisterAll(t *testing.T) {
	r := NewMemoryRegistry()
	r.RegisterAll([]Tool{&dummyTool{"X"}, &dummyTool{"Y"}, &dummyTool{"Z"}})

	if r.Count() != 3 {
		t.Errorf("count: got %d", r.Count())
	}

	names := r.Names()
	if len(names) != 3 {
		t.Errorf("names: got %d", len(names))
	}
}

func TestMemoryRegistry_List(t *testing.T) {
	r := NewMemoryRegistry()
	r.Register(&dummyTool{"A"})
	r.Register(&dummyTool{"B"})

	list, err := r.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("got %d, want 2", len(list))
	}
}

func TestMemoryRegistry_Replace(t *testing.T) {
	r := NewMemoryRegistry()
	r.Register(&dummyTool{"A"})
	r.Register(&dummyTool{"A"}) // replace

	if r.Count() != 1 {
		t.Errorf("count: got %d, want 1", r.Count())
	}
}
