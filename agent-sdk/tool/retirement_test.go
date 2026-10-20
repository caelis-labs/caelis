package tool

import (
	"encoding/json"
	"reflect"
	"testing"
)

func retirementDefinition(name, version string) Definition {
	return Definition{Name: name, InputSchema: map[string]any{"type": "object"}, Metadata: map[string]any{MetadataToolKind: MetadataToolKindMCP, MetadataMCPServer: "server", MetadataMCPTool: name, MetadataMCPSourceFingerprint: version}}
}
func retirementVisibility(defs ...Definition) ToolVisibility {
	tools := []Tool{NamedTool{Def: Definition{Name: ToolSearchToolName, Metadata: map[string]any{MetadataToolKind: MetadataToolKindToolSearch}}}, NewRetirementTool()}
	for _, def := range defs {
		tools = append(tools, NamedTool{Def: def})
	}
	return NewToolVisibility(tools)
}
func retirementOutput(action string, bindings ...Binding) map[string]any {
	raw, _ := json.Marshal(retirementRequest{Action: action, Tools: bindings})
	var output map[string]any
	_ = json.Unmarshal(raw, &output)
	return output
}
func TestRetirementMarksPreservePrefixAndValidateWholeBindingSet(t *testing.T) {
	a, b := retirementDefinition("a", "1"), retirementDefinition("b", "1")
	v := retirementVisibility(a, b)
	if len(v.ModelSpecs()) != 1 {
		t.Fatal("manager exposed before discovery")
	}
	v.AdmitToolSearchResult(NewToolSearchResult([]Definition{a, b}))
	before := v.ModelSpecs()
	if len(before) != 4 {
		t.Fatalf("discovery schemas=%d", len(before))
	}
	if err := v.AdmitRetirement(retirementOutput("mark", BindingForDefinition(a))); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, v.ModelSpecs()) || len(v.Exposure().Pending) != 1 {
		t.Fatal("mark changed prefix or was lost")
	}
	stale := BindingForDefinition(b)
	stale.Version = "2"
	if err := v.AdmitRetirement(retirementOutput("mark", BindingForDefinition(b), stale)); err == nil || len(v.Exposure().Pending) != 1 {
		t.Fatal("non-atomic stale binding admission")
	}
	rediscovered := v.AdmitToolSearchResult(NewToolSearchResult([]Definition{a}))
	if len(rediscovered.Tools) != 1 || rediscovered.Tools[0].Binding == nil {
		t.Fatal("rediscovery omitted binding needed after compact")
	}
	if len(v.Exposure().Pending) != 0 {
		t.Fatal("rediscovery did not cancel mark")
	}
	_ = v.AdmitRetirement(retirementOutput("mark", BindingForDefinition(a)))
	_ = v.AdmitRetirement(retirementOutput("cancel", BindingForDefinition(a)))
	if len(v.Exposure().Pending) != 0 {
		t.Fatal("cancel failed")
	}
	_ = v.AdmitRetirement(retirementOutput("mark", BindingForDefinition(a)))
	v.KeepTool("a")
	if len(v.Exposure().Pending) != 0 {
		t.Fatal("use did not cancel mark")
	}
}
func TestRetirementRestoreRejectsNamesAndVersionsAndRemovesManager(t *testing.T) {
	a := retirementDefinition("same", "1")
	v := retirementVisibility(a)
	v.ApplyToolResult(ToolSearchToolName, map[string]any{"tools": []any{map[string]any{"name": "same"}}})
	if len(v.ModelSpecs()) != 1 {
		t.Fatal("legacy name-only result gained exposure")
	}
	v.AdmitToolSearchResult(NewToolSearchResult([]Definition{a}))
	state := v.Exposure()
	next := retirementVisibility(retirementDefinition("same", "2"))
	next.RestoreExposure(state)
	if len(next.ModelSpecs()) != 1 {
		t.Fatal("same-name replacement inherited exposure")
	}
	if _, ok := next.LookupTool("same"); ok {
		t.Fatal("hidden same-name tool callable")
	}
	next.AdmitToolSearchResult(NewToolSearchResult([]Definition{retirementDefinition("same", "2")}))
	if len(next.ModelSpecs()) != 3 {
		t.Fatal("new schema could not be rediscovered")
	}
	next.RestoreExposure(ExposureState{Version: 1})
	if len(next.ModelSpecs()) != 1 {
		t.Fatal("manager remained after all tools retired")
	}
	if _, ok := next.LookupTool(RetireToolName); ok {
		t.Fatal("hidden manager callable")
	}
	next.AdmitToolSearchResult(NewToolSearchResult([]Definition{retirementDefinition("same", "2")}))
	if len(next.ModelSpecs()) != 3 {
		t.Fatal("rediscovery after all unload failed")
	}
}

func TestRetirementExposureSurvivesUnreadyCatalog(t *testing.T) {
	def := retirementDefinition("late", "1")
	binding := BindingForDefinition(def)
	state := ExposureState{Version: 1, Active: []Binding{binding}, Pending: []Binding{binding}}
	v := retirementVisibility()
	v.RestoreExposure(state)
	v.KeepTool("LocalRead")
	if got := v.Exposure(); !reflect.DeepEqual(got, state) {
		t.Fatalf("unready catalog erased durable binding: got %+v want %+v", got, state)
	}
	v.RefreshDeferredTools([]Tool{NamedTool{Def: def}}, nil)
	v.RestoreExposure(v.Exposure())
	if len(v.ModelSpecs()) != 3 {
		t.Fatal("exact ready binding was not restored")
	}
}
