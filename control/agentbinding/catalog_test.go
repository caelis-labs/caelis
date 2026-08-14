package agentbinding

import (
	"reflect"
	"testing"
)

func TestDefinitionsAreTheSingleCanonicalHandleCatalog(t *testing.T) {
	t.Parallel()

	definitions := Definitions()
	wantHandles := []Handle{HandleSelf, HandleBreeze, HandleOrbit, HandleZenith, HandleGuardian, HandleReviewer}
	gotHandles := make([]Handle, 0, len(definitions))
	for _, definition := range definitions {
		gotHandles = append(gotHandles, definition.Handle)
		if definition.Name == "" || definition.Description == "" {
			t.Fatalf("definition %q is incomplete: %#v", definition.Handle, definition)
		}
	}
	if !reflect.DeepEqual(gotHandles, wantHandles) {
		t.Fatalf("Definitions handles = %#v, want %#v", gotHandles, wantHandles)
	}
	if got := DirectRunHandles(); !reflect.DeepEqual(got, []Handle{HandleBreeze, HandleOrbit, HandleZenith}) {
		t.Fatalf("DirectRunHandles() = %#v", got)
	}
	if got := definitionHandles(DelegationDefinitions()); !reflect.DeepEqual(got, []Handle{HandleSelf, HandleBreeze, HandleOrbit, HandleZenith}) {
		t.Fatalf("DelegationDefinitions() = %#v", got)
	}
	if got := definitionHandles(SystemDefinitions()); !reflect.DeepEqual(got, []Handle{HandleGuardian, HandleReviewer}) {
		t.Fatalf("SystemDefinitions() = %#v", got)
	}

	definitions[0].Handle = "mutated"
	if Definitions()[0].Handle != HandleSelf {
		t.Fatal("Definitions returned mutable package state")
	}
}

func TestHandleClassificationComesFromCatalog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		handle     Handle
		delegation bool
		system     bool
		direct     bool
	}{
		{handle: HandleSelf, delegation: true},
		{handle: HandleBreeze, delegation: true, direct: true},
		{handle: HandleOrbit, delegation: true, direct: true},
		{handle: HandleZenith, delegation: true, direct: true},
		{handle: HandleGuardian, system: true},
		{handle: HandleReviewer, system: true},
		{handle: "unknown"},
	}
	for _, test := range tests {
		if got := IsDelegation(test.handle); got != test.delegation {
			t.Errorf("IsDelegation(%q) = %v, want %v", test.handle, got, test.delegation)
		}
		if got := IsSystem(test.handle); got != test.system {
			t.Errorf("IsSystem(%q) = %v, want %v", test.handle, got, test.system)
		}
		if got := IsDirectRun(test.handle); got != test.direct {
			t.Errorf("IsDirectRun(%q) = %v, want %v", test.handle, got, test.direct)
		}
	}
}

func definitionHandles(definitions []Definition) []Handle {
	out := make([]Handle, 0, len(definitions))
	for _, definition := range definitions {
		out = append(out, definition.Handle)
	}
	return out
}
