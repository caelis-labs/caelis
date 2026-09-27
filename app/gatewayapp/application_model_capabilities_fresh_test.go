package gatewayapp

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelconfig"
)

func TestApplicationModelCapabilitiesReadCanonicalCatalog(t *testing.T) {
	ctx := t.Context()
	stack := newLocalStateTestHost(t, &runtimeMemoryHostStub{})
	owner := appserver.Principal{ID: stack.composition.authorities.userID}
	revision, err := stack.ControlStatus().ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	no, yes := false, true
	connected, err := stack.ConfigurationCommands().ConnectModel(ctx, owner, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "connect-screen", ExpectedRevision: &revision},
		Config:    appserver.ConnectConfig{Provider: "openai-compatible", Model: "screen-custom", BaseURL: "https://provider.invalid/v1", APIKey: "SYNTHETIC_ONLY", ImageInput: &no},
	})
	if err != nil || connected.Outcome != appserver.OutcomeCommitted {
		t.Fatal(connected, err)
	}
	connection, err := stack.Applications().Register(ctx, owner, application.Registration{OperationID: "enroll-screen", Name: "screen-test", Credential: "app-client-" + strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	principal := appserver.Principal{ID: owner.ID, ApplicationID: connection.ApplicationID, ConnectionID: connection.ConnectionID}
	profile := application.Profile{Version: "screen/1", Model: "openai-compatible/screen-custom", Execution: "tools-only", ToolsVersion: "empty/1"}
	created, err := stack.Applications().Create(ctx, principal, appserver.CreateApplicationSessionRequest{WriteBase: appserver.WriteBase{OperationID: "create-screen"}, Profile: profile})
	if err != nil || created.Outcome != appserver.OutcomeCommitted {
		t.Fatal(created, err)
	}
	before, err := stack.Applications().ApplicationModelCapabilities(ctx, principal, created.SessionID)
	if err != nil || before.ImageInput == nil || *before.ImageInput {
		t.Fatal(before, err)
	}
	// Simulate a committed catalog update whose process-local refresh was lost.
	// Keep both the application's selector/revision and the Host lookup unchanged.
	stale := stack.composition.lookup
	var resolutions atomic.Int32
	stale.resolveAPIKey = func(context.Context, string) (string, error) {
		resolutions.Add(1)
		return "", errors.New("capability read resolved credentials")
	}
	stale.resolveHTTPClient = func(context.Context, ModelConfig) (*http.Client, error) {
		resolutions.Add(1)
		return nil, errors.New("capability read built provider client")
	}
	external := newAppConfigStore(filepath.Dir(stack.composition.authorities.store.path))
	for _, capability := range []*bool{&yes, nil, &no, &yes} {
		doc, err := external.LoadContext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for i := range doc.Models.Configs {
			if doc.Models.Configs[i].Model == "screen-custom" {
				doc.Models.Configs[i].ImageInput = capability
				found = true
			}
		}
		if !found {
			t.Fatal("fixture model missing")
		}
		if _, err = external.CompareAndSave(ctx, doc.ConfigurationRevision, doc); err != nil {
			t.Fatal(err)
		}
		got, err := stack.Applications().ApplicationModelCapabilities(ctx, principal, created.SessionID)
		if err != nil || got.Model != before.Model || got.ConfigurationRevision != before.ConfigurationRevision || got.SessionID != before.SessionID {
			t.Fatal(got, err)
		}
		if (got.ImageInput == nil) != (capability == nil) || got.ImageInput != nil && *got.ImageInput != *capability {
			t.Fatalf("capability=%v, want=%v despite stale process catalog", got.ImageInput, capability)
		}
		cached, present, err := stale.ResolveConfigIfPresent(profile.Model)
		if err != nil || !present || stack.composition.lookup != stale || modelconfig.ModelImageInput(cached) == nil || *modelconfig.ModelImageInput(cached) {
			t.Fatal("fixture no longer has stale text-only lookup", err)
		}
	}
	// Failure must propagate rather than silently falling back to stale support.
	path := stack.composition.authorities.store.path
	stack.composition.authorities.store.path = t.TempDir() // A directory is not a configuration document.
	got, err := stack.Applications().ApplicationModelCapabilities(ctx, principal, created.SessionID)
	stack.composition.authorities.store.path = path
	if err == nil || got.ImageInput != nil {
		t.Fatal("unreadable catalog used stale lookup", got, err)
	}
	got, err = stack.Applications().ApplicationModelCapabilities(ctx, principal, created.SessionID)
	if err != nil || got.ImageInput == nil || !*got.ImageInput {
		t.Fatal("read did not recover", got, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = stack.composition.applicationModelImageInput(cancelled, profile); !errors.Is(err, context.Canceled) {
		t.Fatal("ignored cancellation", err)
	}
	if resolutions.Load() != 0 {
		t.Fatal("read resolved credentials or provider", resolutions.Load())
	}
}
