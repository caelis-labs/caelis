package appserver

import (
	"context"
	"database/sql"
	"errors"
	"github.com/caelis-labs/caelis/control/internal/operationreceipt"
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func TestApplicationModelCapabilitiesAreScopedAndFollowDesiredProfile(t *testing.T) {
	store, _ := applicationTestStore(t)
	a := Principal{ID: "owner"}
	b := Principal{ID: "other"}
	scope, _ := SessionScope(a)
	profile := applicationTestProfile()
	profile.Model = "vision"
	if err := store.PutBinding(t.Context(), sessionprofile.Binding{Scope: scope, SessionID: "owned", Profile: profile, CreationDigest: "digest"}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	service := &SessionProfileService{config: SessionProfileServiceConfig{Store: store, ValidateProfile: func(context.Context, sessionprofile.Profile) error { return nil }, ModelImageInput: func(_ context.Context, p sessionprofile.Profile) (*bool, error) {
		calls++
		if p.Model == "unknown" {
			return nil, nil
		}
		v := p.Model == "vision"
		return &v, nil
	}}}
	if !slices.Contains(service.Capabilities(), sessionprofile.CapabilityModelCapabilities) {
		t.Fatal("bound capability not advertised")
	}
	for _, foreign := range []Principal{b, {}} {
		if _, err := service.SessionModelCapabilities(t.Context(), foreign, "owned"); err == nil {
			t.Fatal("cross-scope capability read allowed")
		}
	}
	if calls != 0 {
		t.Fatal("unauthorized request reached model catalog")
	}
	revision := uint64(1)
	for i, name := range []string{"vision", "text", "unknown", "vision"} {
		if i > 0 {
			next, err := store.UpdateConfiguration(operationreceipt.WithTransaction(t.Context(), func(context.Context, *sql.Tx, []byte) error { return nil }), scope, "owned", sessionprofile.UpdateConfigurationRequest{OperationID: "model-" + name, ExpectedConfigurationRevision: revision, Patch: sessionprofile.ConfigurationPatch{Model: &name}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			revision = next.Revision
		}
		got, err := service.SessionModelCapabilities(t.Context(), a, "owned")
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
	if slices.Contains(service.Capabilities(), sessionprofile.CapabilityModelCapabilities) {
		t.Fatal("unbound capability advertised")
	}
	if _, err := service.SessionModelCapabilities(t.Context(), a, "owned"); !errors.Is(err, sessionprofile.ErrUnsupported) {
		t.Fatal(err)
	}
}
