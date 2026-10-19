package agentbinding

import (
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/control/modelprofile"
)

func TestRetiredMemoryBindingsCannotActivate(t *testing.T) {
	for _, handle := range []Handle{HandleSteward, HandleMemoryVerifier} {
		legacy := Configuration{Bindings: []Binding{{Handle: handle, ProfileID: "missing", Effort: "unknown"}}, Sets: []BindingSet{{Name: "old", Bindings: []Binding{{Handle: handle}}}}}
		profiles := modelprofile.Configuration{}
		if err := ValidateConfiguration(legacy, profiles); err != nil {
			t.Fatal(err)
		}
		if _, ok := Lookup(legacy, handle); ok {
			t.Fatal("retired binding resolved")
		}
		if _, ok := CatalogFor(legacy).Lookup(handle); ok {
			t.Fatal("retired catalog entry")
		}
		if ValidateCustomHandle(handle) == nil {
			t.Fatal("retired handle reusable as custom role")
		}
		if SupportsProfile(handle, modelprofile.ModelProfile{Backend: modelprofile.Backend{Provider: &modelprofile.ProviderBackend{ModelConfigID: "model"}}}) {
			t.Fatal("retired profile eligible")
		}
		if _, err := Bind(legacy, Binding{Handle: handle}, profiles); !errors.Is(err, ErrRetiredHandle) {
			t.Fatal(err)
		}
		if _, err := Reset(legacy, handle); !errors.Is(err, ErrRetiredHandle) {
			t.Fatal(err)
		}
		applied, err := ApplyBindingSet(legacy, "old", profiles)
		if err != nil || len(applied.Bindings) != 0 {
			t.Fatalf("legacy set activated Memory: %#v, %v", applied, err)
		}
	}
}
