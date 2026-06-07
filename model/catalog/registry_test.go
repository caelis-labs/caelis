package catalog

import (
	"context"
	"testing"

	"github.com/OnslaughtSnail/caelis/model"
)

func TestRegistryList(t *testing.T) {
	r := New(Config{
		Models: []model.ModelInfo{
			{ModelID: "m1", DisplayName: "Model 1"},
			{ModelID: "m2", DisplayName: "Model 2"},
		},
	})
	list, err := r.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("got %d, want 2", len(list))
	}
}

func TestRegistryResolve(t *testing.T) {
	r := New(Config{
		Models: []model.ModelInfo{
			{ModelID: "m1", DisplayName: "Model 1", Aliases: []string{"alias1"}},
		},
	})
	_, info, err := r.Resolve(context.Background(), model.Ref{ModelID: "m1"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if info.ModelID != "m1" {
		t.Errorf("got %q, want %q", info.ModelID, "m1")
	}
}

func TestRegistryResolveAlias(t *testing.T) {
	r := New(Config{
		Models: []model.ModelInfo{
			{ModelID: "m1", Aliases: []string{"fast"}},
		},
	})
	_, info, err := r.Resolve(context.Background(), model.Ref{Alias: "fast"})
	if err != nil {
		t.Fatalf("Resolve by alias: %v", err)
	}
	if info.ModelID != "m1" {
		t.Errorf("got %q, want %q", info.ModelID, "m1")
	}
}

func TestRegistryResolveNotFound(t *testing.T) {
	r := New(Config{})
	_, _, err := r.Resolve(context.Background(), model.Ref{ModelID: "missing"})
	if err == nil {
		t.Error("expected error for missing model")
	}
}

func TestRegistryResolveWithFactory(t *testing.T) {
	r := New(Config{
		Models: []model.ModelInfo{
			{ModelID: "m1"},
		},
		Factory: func(ref model.Ref) (model.LLM, error) {
			return nil, nil // factory returns nil LLM for test
		},
	})
	llm, _, err := r.Resolve(context.Background(), model.Ref{ModelID: "m1"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if llm != nil {
		t.Error("expected nil LLM from test factory")
	}
}

func TestRegistryRegister(t *testing.T) {
	r := New(Config{})
	r.Register(model.ModelInfo{ModelID: "new"})
	list, _ := r.List(context.Background())
	if len(list) != 1 {
		t.Errorf("got %d, want 1", len(list))
	}
}
