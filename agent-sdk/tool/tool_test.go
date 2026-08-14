package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

func TestCloneDefinitionIsolatesExecutionRequirements(t *testing.T) {
	t.Parallel()

	original := Definition{ExecutionRequirements: &ExecutionRequirements{
		Sandbox: sandbox.CapabilitySet{CommandExec: true, AsyncSessions: true},
	}}
	clone := CloneDefinition(original)
	clone.ExecutionRequirements.Sandbox.CommandExec = false
	clone.ExecutionRequirements.Sandbox.AsyncSessions = false
	if !original.ExecutionRequirements.Sandbox.CommandExec || !original.ExecutionRequirements.Sandbox.AsyncSessions {
		t.Fatalf("mutating clone changed original requirements: %+v", original.ExecutionRequirements)
	}
}

func TestDefinitionsAndModelSpecsCloneStableToolContracts(t *testing.T) {
	t.Parallel()

	tools := []Tool{
		NamedTool{
			Def: Definition{
				Name:        "inspect",
				Description: "Inspect session state",
				InputSchema: map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
					},
				},
			},
		},
	}

	defs := Definitions(tools)
	if got, want := len(defs), 1; got != want {
		t.Fatalf("len(defs) = %d, want %d", got, want)
	}
	if got := defs[0].Name; got != "inspect" {
		t.Fatalf("defs[0].Name = %q, want %q", got, "inspect")
	}
	specs := ModelSpecs(tools)
	if got, want := len(specs), 1; got != want {
		t.Fatalf("len(specs) = %d, want %d", got, want)
	}
	if specs[0].Function == nil {
		t.Fatal("specs[0].Function = nil, want function tool spec")
	}
	if got := specs[0].Function.Name; got != "inspect" {
		t.Fatalf("specs[0].Name = %q, want %q", got, "inspect")
	}
	if got := specs[0].Function.Description; got != "Inspect session state" {
		t.Fatalf("specs[0].Description = %q, want %q", got, "Inspect session state")
	}
	if got := specs[0].Kind; got != model.ToolSpecKindFunction {
		t.Fatalf("specs[0].Kind = %q, want %q", got, model.ToolSpecKindFunction)
	}
	if !specs[0].Function.Strict {
		t.Fatal("specs[0].Function.Strict = false, want strict inferred from closed schema")
	}

	defs[0].InputSchema["type"] = "array"
	specs[0].Function.Parameters["type"] = "array"

	clone := Definitions(tools)
	if got := clone[0].InputSchema["type"]; got != "object" {
		t.Fatalf("clone[0].InputSchema[type] = %v, want object", got)
	}
}

func TestModelSpecsDoesNotInferStrictForOpenSchema(t *testing.T) {
	t.Parallel()

	specs := ModelSpecs([]Tool{
		NamedTool{
			Def: Definition{
				Name:        "open",
				Description: "open schema",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
					},
				},
			},
		},
	})
	if len(specs) != 1 || specs[0].Function == nil {
		t.Fatalf("specs = %#v, want one function spec", specs)
	}
	if specs[0].Function.Strict {
		t.Fatal("Function.Strict = true, want false for open schema")
	}
}

func TestModelSpecsInfersStrictForClosedAnyOfSchema(t *testing.T) {
	t.Parallel()

	specs := ModelSpecs([]Tool{
		NamedTool{
			Def: Definition{
				Name:        "search",
				Description: "search schema",
				InputSchema: map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"include": map[string]any{
							"anyOf": []any{
								map[string]any{"type": "string"},
								map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
							},
						},
					},
				},
			},
		},
	})
	if len(specs) != 1 || specs[0].Function == nil {
		t.Fatalf("specs = %#v, want one function spec", specs)
	}
	if !specs[0].Function.Strict {
		t.Fatal("Function.Strict = false, want strict for closed anyOf schema")
	}
}

func TestModelSpecsDoesNotInferStrictForConditionalSchema(t *testing.T) {
	t.Parallel()

	specs := ModelSpecs([]Tool{
		NamedTool{
			Def: Definition{
				Name:        "conditional",
				Description: "conditional schema",
				InputSchema: map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"mode":   map[string]any{"type": "string"},
						"reason": map[string]any{"type": "string"},
					},
					"allOf": []any{map[string]any{
						"if":   map[string]any{"properties": map[string]any{"mode": map[string]any{"const": "guarded"}}},
						"then": map[string]any{"required": []string{"reason"}},
					}},
				},
			},
		},
	})
	if len(specs) != 1 || specs[0].Function == nil {
		t.Fatalf("specs = %#v, want one function spec", specs)
	}
	if specs[0].Function.Strict {
		t.Fatal("Function.Strict = true, want explicit downgrade for conditional schema")
	}
}

func TestToolVisibilityDefersMCPToolsBehindToolSearch(t *testing.T) {
	t.Parallel()

	inspect := NamedTool{Def: Definition{Name: "inspect", Description: "Inspect", InputSchema: map[string]any{"type": "object"}}}
	search := NamedTool{Def: Definition{
		Name:        ToolSearchToolName,
		Description: "Search deferred tools",
		InputSchema: map[string]any{"type": "object"},
		Metadata:    map[string]any{MetadataToolKind: MetadataToolKindToolSearch},
	}}
	mcp := NamedTool{Def: Definition{
		Name:        "mcp__plugin__server__read",
		Description: "Read external data",
		InputSchema: map[string]any{"type": "object"},
		Metadata: map[string]any{
			MetadataToolKind:  MetadataToolKindMCP,
			MetadataPluginID:  "plugin",
			MetadataMCPServer: "server",
		},
	}}
	tools := []Tool{inspect, search, mcp}

	specs := ModelSpecs(tools)
	if got, want := toolSpecNames(specs), []string{"inspect", ToolSearchToolName}; !equalStrings(got, want) {
		t.Fatalf("ModelSpecs names = %v, want %v", got, want)
	}

	visibility := NewToolVisibility(tools)
	visibility.Reveal("mcp__plugin__server__read")
	specs = visibility.ModelSpecs()
	if got, want := toolSpecNames(specs), []string{"inspect", ToolSearchToolName, "mcp__plugin__server__read"}; !equalStrings(got, want) {
		t.Fatalf("ToolVisibility.ModelSpecs names = %v, want %v", got, want)
	}

	allSpecs := AllModelSpecs(tools)
	if got, want := toolSpecNames(allSpecs), []string{"inspect", ToolSearchToolName, "mcp__plugin__server__read"}; !equalStrings(got, want) {
		t.Fatalf("AllModelSpecs names = %v, want %v", got, want)
	}
}

func TestToolVisibilityAdmitsBoundedToolSearchResults(t *testing.T) {
	t.Parallel()

	search := NamedTool{Def: Definition{
		Name:     ToolSearchToolName,
		Metadata: map[string]any{MetadataToolKind: MetadataToolKindToolSearch},
	}}
	tools := []Tool{search}
	definitions := make([]Definition, 0, MaxDeferredToolsPerRun+6)
	for i := 0; i < MaxDeferredToolsPerRun+6; i++ {
		def := Definition{
			Name:        fmt.Sprintf("mcp__plugin__server__tool_%03d", i),
			Description: "Deferred external tool",
			InputSchema: map[string]any{"type": "object"},
			Metadata:    map[string]any{MetadataToolKind: MetadataToolKindMCP},
		}
		definitions = append(definitions, def)
		tools = append(tools, NamedTool{Def: def})
	}
	visibility := NewToolVisibility(tools)
	admitted := visibility.AdmitToolSearchResult(NewToolSearchResult(definitions))
	if got, want := len(admitted.Tools), MaxDeferredToolsPerRun; got != want {
		t.Fatalf("admitted tools = %d, want %d", got, want)
	}
	if !admitted.Truncated || admitted.OmittedCount != 6 {
		t.Fatalf("admitted result = %#v, want truncated with 6 omitted", admitted)
	}
	if got, want := len(visibility.ModelSpecs()), 1+MaxDeferredToolsPerRun; got != want {
		t.Fatalf("visible specs = %d, want %d", got, want)
	}

	repeated := visibility.AdmitToolSearchResult(NewToolSearchResult(definitions))
	if len(repeated.Tools) != 0 || repeated.AlreadyVisibleCount != MaxDeferredToolsPerRun {
		t.Fatalf("repeated admission = %#v, want no duplicate schemas", repeated)
	}
}

func TestToolVisibilityAdmitsBoundedCumulativePromptCost(t *testing.T) {
	t.Parallel()

	search := NamedTool{Def: Definition{
		Name:     ToolSearchToolName,
		Metadata: map[string]any{MetadataToolKind: MetadataToolKindToolSearch},
	}}
	tools := []Tool{search}
	definitions := make([]Definition, 0, MaxDeferredToolsPerRun)
	for i := 0; i < MaxDeferredToolsPerRun; i++ {
		def := Definition{
			Name:        fmt.Sprintf("mcp__plugin__server__heavy_%03d", i),
			Description: strings.Repeat("heavy metadata ", 300),
			InputSchema: map[string]any{"type": "object"},
			Metadata:    map[string]any{MetadataToolKind: MetadataToolKindMCP},
		}
		definitions = append(definitions, def)
		tools = append(tools, NamedTool{Def: def})
	}
	visibility := NewToolVisibility(tools)
	admitted := visibility.AdmitToolSearchResult(NewToolSearchResult(definitions))
	if !admitted.Truncated || len(admitted.Tools) >= MaxDeferredToolsPerRun {
		t.Fatalf("admitted result = %#v, want cumulative prompt-cost truncation", admitted)
	}
	if visibility.deferredPromptTokens > MaxDeferredToolPromptTokensPerRun {
		t.Fatalf("deferred prompt tokens = %d, want <= %d", visibility.deferredPromptTokens, MaxDeferredToolPromptTokensPerRun)
	}
}

func TestToolVisibilityCommitFailureReturnsExactFailClosedResult(t *testing.T) {
	t.Parallel()

	search := NamedTool{Def: Definition{
		Name:     ToolSearchToolName,
		Metadata: map[string]any{MetadataToolKind: MetadataToolKindToolSearch},
	}}
	visibility := NewToolVisibility([]Tool{search})
	planned := ToolSearchResult{
		Tools: []ToolSearchDiscoveredTool{
			{Name: "mcp__missing__one"},
			{Name: "mcp__missing__two"},
		},
		Count:               2,
		Truncated:           true,
		OmittedCount:        3,
		AlreadyVisibleCount: 4,
	}

	got := visibility.commitToolSearchAdmission(planned, []string{"mcp__missing__one", "mcp__missing__two"})
	if len(got.Tools) != 0 || got.Count != 0 || !got.Truncated || got.OmittedCount != 5 || got.AlreadyVisibleCount != 4 {
		t.Fatalf("commit failure result = %#v, want empty exact fail-closed result with 5 omitted and 4 already visible", got)
	}
	if gotSpecs := visibility.ModelSpecs(); len(gotSpecs) != 1 || gotSpecs[0].Function == nil || gotSpecs[0].Function.Name != ToolSearchToolName {
		t.Fatalf("visibility changed after failed atomic commit: %#v", gotSpecs)
	}
}

func TestToolSearchNameWithoutMetadataDoesNotEnableDeferral(t *testing.T) {
	t.Parallel()

	searchStub := NamedTool{Def: Definition{Name: ToolSearchToolName}}
	mcp := NamedTool{Def: Definition{
		Name:        "mcp__plugin__server__read",
		Description: "Read external data",
		InputSchema: map[string]any{"type": "object"},
		Metadata:    map[string]any{MetadataToolKind: MetadataToolKindMCP},
	}}
	specs := ModelSpecs([]Tool{searchStub, mcp})
	if got, want := toolSpecNames(specs), []string{ToolSearchToolName, "mcp__plugin__server__read"}; !equalStrings(got, want) {
		t.Fatalf("ModelSpecs names = %v, want %v", got, want)
	}
}

func TestParseToolSearchOutput(t *testing.T) {
	t.Parallel()

	result := ParseToolSearchOutput(map[string]any{
		"tools": []any{
			map[string]any{"name": "direct"},
			map[string]any{"function": map[string]any{"name": "nested"}},
			map[string]any{"name": "   "},
		},
	})
	if got, want := result.DiscoveredToolNames(), []string{"direct", "nested"}; !equalStrings(got, want) {
		t.Fatalf("DiscoveredToolNames = %v, want %v", got, want)
	}

	malformed := ParseToolSearchOutput(map[string]any{"tools": "not-an-array"})
	if got := malformed.DiscoveredToolNames(); len(got) != 0 {
		t.Fatalf("malformed DiscoveredToolNames = %v, want empty", got)
	}
}

func TestDiscoveredToolNamesMetadataRoundTrip(t *testing.T) {
	t.Parallel()

	value := DiscoveredToolNamesMetadataValue([]string{"mcp__a", "mcp__A", "", "mcp__b"})
	got := DiscoveredToolNamesFromMetadata(map[string]any{MetadataDiscoveredToolNames: value})
	if want := []string{"mcp__a", "mcp__b"}; !equalStrings(got, want) {
		t.Fatalf("DiscoveredToolNamesFromMetadata = %v, want %v", got, want)
	}

	got = DiscoveredToolNamesFromMetadata(map[string]any{
		MetadataDiscoveredToolNames: []any{"mcp__c", 42, " "},
	})
	if want := []string{"mcp__c"}; !equalStrings(got, want) {
		t.Fatalf("DiscoveredToolNamesFromMetadata([]any) = %v, want %v", got, want)
	}
}

func TestNamedToolClonesCallAndResult(t *testing.T) {
	t.Parallel()

	tool := NamedTool{
		Def: Definition{Name: "echo"},
		Invoke: func(_ context.Context, call Call) (Result, error) {
			if got := string(call.Input); got != `{"value":"ok"}` {
				t.Fatalf("call.Input = %s, want %s", got, `{"value":"ok"}`)
			}
			call.Metadata["mutated"] = true
			call.Metadata["nested"].(map[string]any)["value"] = "invoke-mutated"
			call.ModelStep.ID = "invoke-mutated"
			return Result{
				Name: "echo",
				Content: []model.Part{
					model.NewTextPart("ok"),
				},
				Meta: map[string]any{
					"source": "invoke",
					"nested": map[string]any{"value": "result"},
				},
			}, nil
		},
	}

	call := Call{
		Name:      "echo",
		Input:     json.RawMessage(`{"value":"ok"}`),
		ModelStep: &ModelStepRef{ID: "step-1", Index: 0, CallCount: 1},
		Metadata: map[string]any{
			"trace":  "1",
			"nested": map[string]any{"value": "caller"},
		},
	}
	result, err := tool.Call(context.Background(), call)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if got := result.Name; got != "echo" {
		t.Fatalf("result.Name = %q, want %q", got, "echo")
	}
	if got := result.Content[0].Text.Text; got != "ok" {
		t.Fatalf("result.Content[0].Text.Text = %q, want %q", got, "ok")
	}
	if _, ok := call.Metadata["mutated"]; ok {
		t.Fatal("input call metadata should be cloned")
	}
	if got := call.Metadata["nested"].(map[string]any)["value"]; got != "caller" {
		t.Fatalf("input call nested metadata leaked mutation: %v", got)
	}
	if got := call.ModelStep.ID; got != "step-1" {
		t.Fatalf("input call model step leaked mutation: %q", got)
	}
	result.Meta["nested"].(map[string]any)["value"] = "caller-mutated"
	if got := result.Meta["nested"].(map[string]any)["value"]; got != "caller-mutated" {
		t.Fatalf("result nested metadata mutation = %v", got)
	}
}

func TestConcurrentModelStepAdmissionSurvivesCallCloning(t *testing.T) {
	t.Parallel()

	refs := NewConcurrentModelStepRefs("step-1", 2)
	if len(refs) != 2 || refs[0].AdmissionDone() == nil || refs[0].AdmissionDone() != refs[1].AdmissionDone() {
		t.Fatalf("concurrent model-step refs = %#v, want one shared admission barrier", refs)
	}
	cloned := CloneCall(Call{ModelStep: refs[0]})
	cloned.ModelStep.MarkAdmissionComplete()
	select {
	case <-refs[0].AdmissionDone():
		t.Fatal("admission barrier closed before every call completed admission")
	default:
	}
	refs[1].MarkAdmissionComplete()
	select {
	case <-refs[0].AdmissionDone():
	default:
		t.Fatal("admission barrier remained open after every call completed admission")
	}
}

func TestEffectClassOfDefaultsFailSafe(t *testing.T) {
	t.Parallel()
	if got := EffectClassOf(Definition{}); got != EffectNonIdempotent {
		t.Fatalf("EffectClassOf(empty) = %q, want non_idempotent", got)
	}
	if got := EffectClassOf(Definition{Metadata: map[string]any{"annotations": map[string]any{"readOnlyHint": true}}}); got != EffectReadOnly {
		t.Fatalf("EffectClassOf(readonly) = %q, want read_only", got)
	}
	if got := EffectClassOf(Definition{EffectClass: EffectIdempotent}); got != EffectIdempotent {
		t.Fatalf("EffectClassOf(explicit) = %q, want idempotent", got)
	}
}

func toolSpecNames(specs []model.ToolSpec) []string {
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		if spec.Function != nil {
			out = append(out, spec.Function.Name)
		}
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
