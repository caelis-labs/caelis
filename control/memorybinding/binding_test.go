package memorybinding

import (
	"reflect"
	"strings"
	"testing"

	memoryv1alpha1 "github.com/caelis-labs/memory/api/memory/v1alpha1"
)

func TestResolveSelectsOneOpaqueBindingIntoDetachedRuntimeSnapshot(t *testing.T) {
	configuration := testConfiguration()
	selected, enabled, err := Resolve(configuration, RuntimeSelection{BindingRef: " shared "})
	if err != nil || !enabled {
		t.Fatalf("Resolve(shared) = %#v, %v, %v", selected, enabled, err)
	}
	if selected.BindingRef != "shared" || selected.RuntimeActorRef != "actor-a" ||
		selected.PrincipalRef != "principal:a" || selected.ViewRef != "view-a-shared" ||
		selected.GrantRef != "grant-a-shared" || selected.Audience != OutputAudienceShared ||
		selected.BindingVersion != 7 {
		t.Fatalf("shared snapshot = %#v", selected)
	}
	if strings.Contains(selected.ViewRef, "private") || strings.Contains(selected.GrantRef, "private") {
		t.Fatalf("snapshot retained another binding: %#v", selected)
	}

	defaulted, enabled, err := Resolve(configuration, RuntimeSelection{})
	if err != nil || !enabled {
		t.Fatalf("Resolve(default) = %#v, %v, %v", defaulted, enabled, err)
	}
	if defaulted.BindingRef != "private" || defaulted.Audience != OutputAudiencePrivate || defaulted.ViewRef != "view-a-private" {
		t.Fatalf("default snapshot = %#v", defaulted)
	}
}

func TestValidationRejectsDuplicateOrIncompleteAuthorityReferences(t *testing.T) {
	for name, mutate := range map[string]func(*Configuration){
		"duplicate binding": func(configuration *Configuration) {
			duplicate := configuration.Bindings[0]
			duplicate.RuntimeActorRef = "actor-b"
			configuration.Bindings = append(configuration.Bindings, duplicate)
		},
		"missing default": func(configuration *Configuration) {
			configuration.DefaultBindingRef = ""
		},
		"unknown default": func(configuration *Configuration) {
			configuration.DefaultBindingRef = "missing"
		},
		"missing Grant": func(configuration *Configuration) {
			configuration.Bindings[0].GrantRef = ""
		},
		"missing issuer credential reference": func(configuration *Configuration) {
			configuration.Bindings[0].IssuerCredentialRef = ""
		},
		"invalid audience": func(configuration *Configuration) {
			configuration.Bindings[0].Audience = "public"
		},
	} {
		t.Run(name, func(t *testing.T) {
			configuration := testConfiguration()
			mutate(&configuration)
			if err := Validate(configuration); err == nil {
				t.Fatalf("Validate(%s) succeeded: %#v", name, configuration)
			}
		})
	}
}

func TestResolveFailsClosedForUnknownBinding(t *testing.T) {
	configuration := testConfiguration()
	if _, enabled, err := Resolve(configuration, RuntimeSelection{BindingRef: "missing"}); err == nil || enabled {
		t.Fatalf("Resolve(unknown) = enabled:%v error:%v", enabled, err)
	}
}

func testConfiguration() Configuration {
	return Configuration{
		DefaultBindingRef: "private",
		Bindings: []AccessBinding{
			{
				BindingRef: "private", RuntimeActorRef: "actor-a", PrincipalRef: "principal:a",
				IssuerCredentialRef: "memory-issuer:a", ViewRef: "view-a-private", GrantRef: "grant-a-private",
				Audience: OutputAudiencePrivate, BindingVersion: 7,
			},
			{
				BindingRef: "shared", RuntimeActorRef: "actor-a", PrincipalRef: "principal:a",
				IssuerCredentialRef: "memory-issuer:a", ViewRef: "view-a-shared", GrantRef: "grant-a-shared",
				Audience: OutputAudienceShared, BindingVersion: 7,
			},
		},
	}
}

func TestZeroConfigurationWaitsForHostProvisioning(t *testing.T) {
	t.Parallel()

	configuration := Configuration{}
	if IsConfigured(configuration) {
		t.Fatal("zero configuration is unexpectedly configured")
	}
	if snapshot, active, err := Resolve(configuration, RuntimeSelection{}); err != nil || active || !reflect.DeepEqual(snapshot, RuntimeMemoryBindingSnapshot{}) {
		t.Fatalf("Resolve(zero) = %#v, %v, %v", snapshot, active, err)
	}
}

func TestBindRuntimeLabelsCanonicalizesAndDetaches(t *testing.T) {
	binding, active, err := Resolve(testConfiguration(), RuntimeSelection{})
	if err != nil || !active {
		t.Fatalf("Resolve() = %#v, %v, %v", binding, active, err)
	}
	input := memoryv1alpha1.LabelSet{"workspace:z", "identity:a"}
	labeled, err := BindRuntimeLabels(binding, input)
	if err != nil {
		t.Fatal(err)
	}
	input[0] = "workspace:changed"
	if !reflect.DeepEqual(labeled.Labels, memoryv1alpha1.LabelSet{"identity:a", "workspace:z"}) {
		t.Fatalf("canonical labels = %#v", labeled.Labels)
	}
	if len(binding.Labels) != 0 {
		t.Fatalf("source binding was mutated: %#v", binding.Labels)
	}
	if _, err := BindRuntimeLabels(binding, memoryv1alpha1.LabelSet{"duplicate", "duplicate"}); err == nil {
		t.Fatal("BindRuntimeLabels accepted duplicate labels")
	}
}
