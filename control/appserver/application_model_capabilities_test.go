package appserver

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/control/application"
)

func TestApplicationModelCapabilitiesAreScopedAndFollowDesiredProfile(t *testing.T) {
	store, _ := applicationTestStore(t)
	a := enrollTestApplication(t, store, "owner", "cap-a", "a")
	b := enrollTestApplication(t, store, "owner", "cap-b", "b")
	scope, _ := ApplicationScope(a)
	profile := applicationTestProfile()
	profile.Model = "vision"
	if err := store.PutBinding(t.Context(), application.Binding{Scope: scope, SessionID: "owned", Profile: profile, CreationDigest: "digest"}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	service := &ApplicationService{config: ApplicationServiceConfig{Store: store, ValidateProfile: func(context.Context, application.Profile) error { return nil }, ModelImageInput: func(_ context.Context, p application.Profile) (*bool, error) {
		calls++
		if p.Model == "unknown" {
			return nil, nil
		}
		v := p.Model == "vision"
		return &v, nil
	}}}
	if !slices.Contains(service.Capabilities(), application.CapabilityModelCapabilities) {
		t.Fatal("bound capability not advertised")
	}
	for _, foreign := range []Principal{b, {ID: "owner"}, {}} {
		if _, err := service.ApplicationModelCapabilities(t.Context(), foreign, "owned"); err == nil {
			t.Fatal("cross-scope capability read allowed")
		}
	}
	if calls != 0 {
		t.Fatal("unauthorized request reached model catalog")
	}
	revision := uint64(1)
	for i, name := range []string{"vision", "text", "unknown", "vision"} {
		if i > 0 {
			next, err := service.UpdateApplicationConfiguration(t.Context(), a, "owned", application.UpdateConfigurationRequest{OperationID: "model-" + name, ExpectedConfigurationRevision: revision, Patch: application.ConfigurationPatch{Model: &name}})
			if err != nil {
				t.Fatal(err)
			}
			revision = next.Revision
		}
		got, err := service.ApplicationModelCapabilities(t.Context(), a, "owned")
		if err != nil || got.SessionID != "owned" || got.Model != name || got.ConfigurationRevision != revision {
			t.Fatal(got, err)
		}
		if name == "unknown" {
			if got.ImageInput != nil {
				t.Fatal("unknown declared text-only")
			}
		} else if got.ImageInput == nil || *got.ImageInput != (name == "vision") {
			t.Fatal("wrong model capability", got)
		}
	}
	service.config.ModelImageInput = nil
	if slices.Contains(service.Capabilities(), application.CapabilityModelCapabilities) {
		t.Fatal("unbound capability advertised")
	}
	if _, err := service.ApplicationModelCapabilities(t.Context(), a, "owned"); !errors.Is(err, application.ErrUnsupported) {
		t.Fatal(err)
	}
}
