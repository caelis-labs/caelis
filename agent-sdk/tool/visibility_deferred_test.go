package tool

import (
	"context"
	"fmt"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestDeferredVisibilityRestoresPendingReplayAndPinsCallable(t *testing.T) {
	visibility := NewToolVisibility([]Tool{NamedTool{Def: Definition{Name: ToolSearchToolName, Metadata: map[string]any{MetadataToolKind: MetadataToolKindToolSearch}}}})
	visibility.ApplyDiscoveredToolNames([]string{"mcp__old__docs__read", "mcp__old__docs__read"})
	if len(visibility.pendingReplay) != 1 || len(visibility.ModelSpecs()) != 1 {
		t.Fatal("pending replay duplicated or exposed an unready tool")
	}
	def := Definition{Name: "docs__read", Description: "original", InputSchema: map[string]any{"type": "object"}, Metadata: map[string]any{MetadataToolKind: MetadataToolKindMCP, MetadataReplayAliases: []string{"mcp__old__docs__read"}}}
	called := ""
	original := NamedTool{Def: def, Invoke: func(context.Context, Call) (Result, error) {
		called = "original"
		return Result{Content: []model.Part{model.NewTextPart("ok")}}, nil
	}}
	visibility.RefreshDeferredTools([]Tool{original}, nil)
	if len(visibility.ModelSpecs()) != 2 || len(visibility.pendingReplay) != 0 {
		t.Fatal("ready tool did not restore durable discovery")
	}
	replacement := original
	replacement.Def.Description = "replacement"
	replacement.Invoke = func(context.Context, Call) (Result, error) { called = "replacement"; return Result{}, nil }
	visibility.RefreshDeferredTools([]Tool{replacement}, nil)
	item, ok := visibility.LookupTool(def.Name)
	if !ok {
		t.Fatal("ready callable missing")
	}
	if _, err := item.Call(t.Context(), Call{Name: def.Name}); err != nil {
		t.Fatal(err)
	}
	if called != "original" || visibility.ModelSpecs()[1].Function.Description != "original" {
		t.Fatal("published definition or callable changed during run")
	}
}

func TestDeferredVisibilityBoundsPendingReplay(t *testing.T) {
	visibility := NewToolVisibility(nil)
	for i := range 2 * MaxDeferredToolsPerRun {
		visibility.ApplyDiscoveredToolNames([]string{fmt.Sprintf("unknown_%d", i), "", " bad "})
	}
	if len(visibility.pendingReplay) != MaxDeferredToolsPerRun {
		t.Fatalf("pending discoveries=%d", len(visibility.pendingReplay))
	}
}

func TestDeferredVisibilityRestoresReplayAliasAddedAfterWinnerIsReady(t *testing.T) {
	visibility := NewToolVisibility(nil)
	definition := Definition{Name: "docs__read", Metadata: map[string]any{MetadataToolKind: MetadataToolKindMCP}}
	visibility.RefreshDeferredTools([]Tool{NamedTool{Def: definition}}, nil)
	visibility.ApplyDiscoveredToolNames([]string{"mcp__fallback__docs__read"})
	if len(visibility.ModelSpecs()) != 0 {
		t.Fatal("unresolved replay alias exposed a tool")
	}
	definition.Metadata[MetadataReplayAliases] = []string{"mcp__fallback__docs__read"}
	visibility.RefreshDeferredTools([]Tool{NamedTool{Def: definition}}, nil)
	if names := toolSpecNames(visibility.ModelSpecs()); len(names) != 1 || names[0] != "docs__read" {
		t.Fatalf("late replay alias not restored: %v", names)
	}
}
