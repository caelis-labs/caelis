package agentbinding

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestValidateRolesRejectsProjectionOverflow(t *testing.T) {
	t.Parallel()

	roles := make([]Role, 0, maxCustomRoles+1)
	for i := 0; i < maxCustomRoles+1; i++ {
		roles = append(roles, Role{Handle: Handle(fmt.Sprintf("role-%02d", i)), Description: "bounded role"})
	}
	if err := ValidateRoles(roles); err == nil {
		t.Fatal("ValidateRoles(over count) succeeded")
	}
	if err := ValidateRoles([]Role{{Handle: "research", Description: strings.Repeat("界", maxCustomRoleDescriptionRunes+1)}}); err == nil {
		t.Fatal("ValidateRoles(overlong description) succeeded")
	}
}

func TestCreateRoleAddsBoundCustomDelegationDefinition(t *testing.T) {
	profiles := testProfiles()
	got, err := CreateRole(Configuration{}, Role{
		Handle:      " Research ",
		Description: " Investigate unfamiliar systems. ",
	}, Binding{ProfileID: "acp:claude:opus", Effort: "xhigh"}, profiles)
	if err != nil {
		t.Fatal(err)
	}
	role, ok := LookupRole(got, "research")
	if !ok || role.Description != "Investigate unfamiliar systems." {
		t.Fatalf("LookupRole(research) = %#v, %v", role, ok)
	}
	binding, ok := Lookup(got, "research")
	if !ok || binding.ProfileID != "acp:claude:opus" || binding.Effort != "xhigh" {
		t.Fatalf("Lookup(research) = %#v, %v", binding, ok)
	}
	catalog := CatalogFor(got)
	definition, ok := catalog.Lookup("research")
	if !ok || !definition.Custom || !catalog.IsDirectRun("research") || !catalog.IsDelegation("research") {
		t.Fatalf("custom definition = %#v, %v", definition, ok)
	}
	if got := catalog.DirectRunHandles(); !reflect.DeepEqual(got, []Handle{HandleBreeze, HandleOrbit, HandleZenith, "research"}) {
		t.Fatalf("Catalog.DirectRunHandles() = %#v", got)
	}
}

func TestValidateCustomHandleRejectsReservedProductCommands(t *testing.T) {
	for _, handle := range []Handle{
		"help", "review", "model", "status", "doctor", "new", "resume",
		"compact", "connect", "disconnect", "subagent", "plugin", "exit", "quit", "lead", "sandbox",
	} {
		if err := ValidateCustomHandle(handle); err == nil {
			t.Errorf("ValidateCustomHandle(%q) succeeded, want reserved-name rejection", handle)
		}
	}
}

func TestBindingSetSaveApplyAndUnavailableStatusAreAtomic(t *testing.T) {
	profiles := testProfiles()
	current, err := CreateRole(Configuration{}, Role{
		Handle: "research", Description: "Investigate unfamiliar systems.",
	}, Binding{ProfileID: "acp:claude:opus", Effort: "xhigh"}, profiles)
	if err != nil {
		t.Fatal(err)
	}
	current, err = SaveBindingSet(current, "deep-work")
	if err != nil {
		t.Fatal(err)
	}
	current, err = Bind(current, Binding{
		Handle: HandleOrbit, ProfileID: "provider:model", Effort: "low",
	}, profiles)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyBindingSet(current, "deep-work", profiles)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := Lookup(applied, HandleOrbit); ok {
		t.Fatalf("ApplyBindingSet retained later Orbit binding: %#v", applied.Bindings)
	}
	if binding, ok := Lookup(applied, "research"); !ok || binding.ProfileID != "acp:claude:opus" {
		t.Fatalf("ApplyBindingSet research binding = %#v, %v", binding, ok)
	}
	statuses := BindingSetStatuses(applied, profiles)
	if len(statuses) != 1 || !statuses[0].Active || !statuses[0].Available {
		t.Fatalf("BindingSetStatuses(active) = %#v", statuses)
	}

	staleProfiles := profiles
	staleProfiles.Profiles = staleProfiles.Profiles[:1]
	statuses = BindingSetStatuses(applied, staleProfiles)
	if len(statuses) != 1 || statuses[0].Available || !strings.Contains(statuses[0].Problem, "unknown profile") {
		t.Fatalf("BindingSetStatuses(stale) = %#v", statuses)
	}
	if _, err := ApplyBindingSet(applied, "deep-work", staleProfiles); err == nil {
		t.Fatal("ApplyBindingSet(stale) succeeded")
	}
	if binding, ok := Lookup(applied, "research"); !ok || binding.ProfileID != "acp:claude:opus" {
		t.Fatalf("failed apply mutated source configuration: %#v, %v", binding, ok)
	}
}

func TestDeleteRoleCleansActiveAndSavedBindings(t *testing.T) {
	profiles := testProfiles()
	current, err := CreateRole(Configuration{}, Role{
		Handle: "research", Description: "Investigate unfamiliar systems.",
	}, Binding{ProfileID: "provider:model", Effort: "high"}, profiles)
	if err != nil {
		t.Fatal(err)
	}
	current, err = SaveBindingSet(current, "with-research")
	if err != nil {
		t.Fatal(err)
	}
	got, err := DeleteRole(current, "research")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := LookupRole(got, "research"); ok {
		t.Fatalf("deleted role survived: %#v", got.Roles)
	}
	if _, ok := Lookup(got, "research"); ok {
		t.Fatalf("deleted role binding survived: %#v", got.Bindings)
	}
	set, ok := LookupBindingSet(got, "with-research")
	if !ok || len(set.Bindings) != 0 {
		t.Fatalf("saved role binding survived: %#v, %v", set, ok)
	}
}
