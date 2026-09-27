package gatewayapp_test

import (
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
)

func TestApplicationModelCapabilitiesHTTP(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	provider := &applicationHTTPModel{}
	host := startApplicationHTTPHost(t, filepath.Join(root, "store"), root, provider)
	defer host.close(t)
	info, err := host.host.Initialize(ctx)
	if err != nil || !slices.Contains(info.Capabilities, application.CapabilityModelCapabilities) {
		t.Fatal("missing negotiation", info, err)
	}
	yes, no := true, false
	for _, m := range []struct {
		name  string
		image *bool
	}{{"screen-vision", &yes}, {"screen-text", &no}, {"screen-unknown", nil}} {
		status, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
		if err != nil {
			t.Fatal(err)
		}
		result, err := host.host.ConnectModel(ctx, appserver.ConnectModelRequest{WriteBase: appserver.WriteBase{OperationID: "connect-" + m.name, ExpectedRevision: &status.Configuration.Revision}, Config: appserver.ConnectConfig{Provider: "openai-compatible", Model: m.name, BaseURL: "https://provider.invalid/v1", APIKey: "SYNTHETIC_ONLY", ImageInput: m.image}})
		if err != nil || result.Outcome != appserver.OutcomeCommitted {
			t.Fatal(result, err)
		}
	}
	a, _ := registerApplicationHTTP(t, ctx, host, "cap-a", filepath.Join(root, "a.credential"))
	b, _ := registerApplicationHTTP(t, ctx, host, "cap-b", filepath.Join(root, "b.credential"))
	profile := applicationHTTPProfile()
	profile.Model = "openai-compatible/screen-text"
	created, err := a.CreateApplicationSession(ctx, appserver.CreateApplicationSessionRequest{WriteBase: appserver.WriteBase{OperationID: "create-capabilities"}, Profile: profile})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.ApplicationModelCapabilities(ctx, created.SessionID); err == nil {
		t.Fatal("cross-application capabilities exposed")
	}
	if _, err = host.host.ApplicationModelCapabilities(ctx, created.SessionID); err == nil {
		t.Fatal("Host principal substituted application scope")
	}
	for _, name := range []string{"screen-text", "screen-vision", "screen-unknown", "screen-vision"} {
		configuration, err := a.ApplicationConfiguration(ctx, created.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		selected := "openai-compatible/" + name
		updated, err := a.UpdateApplicationConfiguration(ctx, created.SessionID, application.UpdateConfigurationRequest{OperationID: "update-" + name + "-" + strconv.FormatUint(configuration.Revision, 10), ExpectedConfigurationRevision: configuration.Revision, Patch: application.ConfigurationPatch{Model: &selected}})
		if err != nil {
			t.Fatal(err)
		}
		got, err := a.ApplicationModelCapabilities(ctx, created.SessionID)
		if err != nil || got.Model != selected || got.SessionID != created.SessionID || got.ConfigurationRevision != updated.Revision {
			t.Fatal(got, err)
		}
		if name == "screen-unknown" {
			if got.ImageInput != nil {
				t.Fatal("invented capability for unknown model")
			}
		} else if got.ImageInput == nil || *got.ImageInput != (name == "screen-vision") {
			t.Fatal("selected model capability incorrect", got)
		}
	}
	if provider.requestCount() != 0 {
		t.Fatal("capability observation called model")
	}
}
