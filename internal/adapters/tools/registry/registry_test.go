package registry

import (
	"context"
	"testing"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/tool"
)

func TestRegistryListsAndLooksUpTools(t *testing.T) {
	echo := tool.NamedTool{
		Def: tool.Definition{Name: "echo", Description: "echo input"},
		Invoke: func(context.Context, tool.Call) (tool.Result, error) {
			return tool.Result{Content: []model.Part{model.NewTextPart("ok")}}, nil
		},
	}
	read := tool.NamedTool{Def: tool.Definition{Name: "read"}}
	reg, err := New(echo, read)
	if err != nil {
		t.Fatal(err)
	}

	items, err := reg.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("listed tools = %d, want 2", len(items))
	}
	if items[0].Definition().Name != "echo" || items[1].Definition().Name != "read" {
		t.Fatalf("tool order = %q, %q; want echo, read", items[0].Definition().Name, items[1].Definition().Name)
	}

	found, ok, err := reg.Lookup(context.Background(), "ECHO")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || found.Definition().Name != "echo" {
		t.Fatalf("lookup ECHO ok=%v name=%q, want echo", ok, found.Definition().Name)
	}
}

func TestRegistryRejectsInvalidTools(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("New(nil) error = nil, want error")
	}

	reg, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(tool.NamedTool{}); err == nil {
		t.Fatal("Register(empty tool) error = nil, want error")
	}
}
