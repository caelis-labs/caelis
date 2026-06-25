package controladapter

import (
	"context"
	"testing"
)

func TestRuntimeStackPluginDepsPreferGroupedField(t *testing.T) {
	t.Parallel()

	calledLegacy := false
	stack := &RuntimeStack{
		ListPluginsFn: func(context.Context) ([]PluginSnapshot, error) {
			calledLegacy = true
			return []PluginSnapshot{{ID: "legacy"}}, nil
		},
		Plugin: PluginRuntimeDeps{
			ListPluginsFn: func(context.Context) ([]PluginSnapshot, error) {
				return []PluginSnapshot{{ID: "grouped"}}, nil
			},
		},
	}

	plugins, err := stack.ListPlugins(context.Background())
	if err != nil {
		t.Fatalf("ListPlugins() error = %v", err)
	}
	if len(plugins) != 1 || plugins[0].ID != "grouped" {
		t.Fatalf("ListPlugins() = %#v, want grouped plugin", plugins)
	}
	if calledLegacy {
		t.Fatal("ListPlugins() used legacy flat dependency despite grouped dependency")
	}
}

func TestRuntimeStackPluginDepsFallbackToLegacyField(t *testing.T) {
	t.Parallel()

	stack := &RuntimeStack{
		ListPluginsFn: func(context.Context) ([]PluginSnapshot, error) {
			return []PluginSnapshot{{ID: "legacy"}}, nil
		},
	}

	plugins, err := stack.ListPlugins(context.Background())
	if err != nil {
		t.Fatalf("ListPlugins() error = %v", err)
	}
	if len(plugins) != 1 || plugins[0].ID != "legacy" {
		t.Fatalf("ListPlugins() = %#v, want legacy plugin", plugins)
	}
}
