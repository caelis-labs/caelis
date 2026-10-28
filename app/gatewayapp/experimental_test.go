package gatewayapp

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"strconv"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/experimental"
	"github.com/caelis-labs/caelis/control/sessionprofile"
	"github.com/caelis-labs/caelis/internal/testenv"
)

func TestExperimentalHTTPIndependentCombinationsAndActivation(t *testing.T) {
	stack, client := newExperimentalHTTPStack(t)
	info, err := client.Initialize(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	supported := false
	for _, c := range info.Capabilities {
		supported = supported || c == appserver.CapabilityExperimental
	}
	if !supported {
		t.Fatal("capability absent")
	}
	var previous string
	var previousValues experimental.Values
	for bits := 0; bits < 8; bits++ {
		snapshot, err := client.ExperimentalFeatures(t.Context(), previous)
		if err != nil {
			t.Fatal(err)
		}
		if previous != "" && !maps.Equal(snapshot.Effective, previousValues) {
			t.Fatalf("read activated wrong Runtime: %+v", snapshot)
		}
		revision, err := strconv.ParseUint(snapshot.Revision, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		values := experimental.Values{experimental.AsyncCompact: bits&1 != 0}
		requested := experimental.Values{experimental.AsyncCompact: bits&1 != 0, experimental.MCPRefresh: bits&2 != 0, experimental.ToolRetirement: bits&4 != 0}
		req := appserver.ExperimentalRequest{WriteBase: appserver.WriteBase{OperationID: fmt.Sprintf("experiments-%d", bits), ExpectedRevision: &revision}, Values: requested}
		result, err := client.SetExperimentalFeatures(t.Context(), req)
		if err != nil || result.Outcome != appserver.OutcomeCommitted {
			t.Fatalf("save=%+v %v", result, err)
		}
		replay, err := client.SetExperimentalFeatures(t.Context(), req)
		if err != nil || replay.Revision != result.Revision {
			t.Fatalf("original receipt replay=%+v %v", replay, err)
		}
		read, err := client.ExperimentalFeatures(t.Context(), previous)
		if err != nil || !maps.Equal(read.Desired, values) {
			t.Fatalf("desired=%+v %v", read, err)
		}
		if previous != "" && !maps.Equal(read.Effective, previousValues) {
			t.Fatal("save rewrote active Runtime")
		}
		doc, err := stack.composition.authorities.store.LoadContext(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(stack.composition.authorities.store.path)
		if err != nil {
			t.Fatal(err)
		}
		var persisted struct{ Experimental experimental.Values }
		if err := json.Unmarshal(raw, &persisted); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{experimental.MCPRefresh, experimental.ToolRetirement} {
			if _, ok := doc.Experimental[id]; ok {
				t.Fatalf("loaded legacy value %s", id)
			}
			if _, ok := persisted.Experimental[id]; ok {
				t.Fatalf("persisted legacy value %s", id)
			}
		}
		created, err := client.CreateSession(t.Context(), appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: fmt.Sprintf("create-experimental-%d", bits)}})
		if err != nil {
			t.Fatal(err)
		}
		runtime, release := activateHeldSessionRuntime(t, stack, created.SessionID)
		defer release()
		if !maps.Equal(runtime.instance.activeRuntime.Experimental, values) {
			t.Fatalf("activation sampled wrong values: %v", runtime.instance.activeRuntime.Experimental)
		}
		profile, err := client.CreateSession(t.Context(), appserver.CreateSessionRequest{
			WriteBase: appserver.WriteBase{OperationID: fmt.Sprintf("create-profile-%d", bits)},
			Profile:   &sessionprofile.Profile{Version: "experimental/1", Model: "ollama/compact-test", ToolsVersion: "tools/1", Execution: "tools-only"},
		})
		if err != nil {
			t.Fatal(err)
		}
		_, releaseProfile := activateHeldSessionRuntime(t, stack, profile.SessionID)
		defer releaseProfile()
		profileRead, err := client.ExperimentalFeatures(t.Context(), profile.SessionID)
		if err != nil || !maps.Equal(profileRead.Effective, values) {
			t.Fatalf("explicit profile activation=%+v %v", profileRead, err)
		}
		previous, previousValues = created.SessionID, values
	}
	snapshot, err := client.ExperimentalFeatures(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	revision, _ := strconv.ParseUint(snapshot.Revision, 10, 64)
	result, err := client.SetExperimentalFeatures(t.Context(), appserver.ExperimentalRequest{WriteBase: appserver.WriteBase{OperationID: "unknown-feature", ExpectedRevision: &revision}, Values: experimental.Values{"missing": false}})
	if err == nil || result.Outcome != appserver.OutcomeRejected {
		t.Fatalf("unknown feature accepted: %+v %v", result, err)
	}
	if _, err := client.ExperimentalFeatures(t.Context(), "missing-session"); err == nil {
		t.Fatal("unauthorized Session read succeeded")
	}
}

func newExperimentalHTTPStack(t *testing.T) (*Stack, *httpclient.Client) {
	t.Helper()
	provider := newGatewayAppCompactionOllamaServer(t)
	stack, err := newGatewayAppTestStack(t, Config{StoreDir: t.TempDir(), WorkspaceKey: "workspace", WorkspaceCWD: t.TempDir(), SkillDirs: []string{}, Sandbox: SandboxConfig{RequestedType: "host"},
		Model: ModelConfig{Provider: "ollama", API: providers.APIOllama, Model: "compact-test", BaseURL: provider.URL, HTTPClient: provider.Client()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	services := gatewayTestAppServerServices(stack.ControlClient(), gatewayTestStatusService{}, stack.TaskStreams())
	services.Experimental = stack.ExperimentalFeatures()
	const token = "0123456789abcdef0123456789abcdef"
	auth, err := controlserver.BearerTokenAuthenticator(token, appserver.Principal{ID: stack.composition.authorities.userID})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := controlserver.New(controlserver.HandlerConfig{Services: services, Authenticator: auth, AllowedHosts: []string{"127.0.0.1", "localhost", "::1"}})
	if err != nil {
		t.Fatal(err)
	}
	server := testenv.NewHTTPServer(t, handler.Handler())
	client, err := httpclient.New(httpclient.Config{BaseURL: server.URL, BearerToken: token, HTTPClient: server.Client(), Compatibility: appserver.CurrentCompatibility()})
	if err != nil {
		t.Fatal(err)
	}

	return stack, client
}

func TestExperimentalLegacyStoredValuesAreIgnoredAndRemovedOnSave(t *testing.T) {
	stack, client := newExperimentalHTTPStack(t)
	store := stack.composition.authorities.store
	doc, err := store.LoadContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	doc.Experimental = experimental.Values{experimental.AsyncCompact: true, experimental.MCPRefresh: true, experimental.ToolRetirement: false}
	if err := store.Save(doc); err != nil {
		t.Fatal(err)
	}
	snapshot, err := client.ExperimentalFeatures(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(snapshot.Desired, experimental.Values{experimental.AsyncCompact: true}) {
		t.Fatalf("desired = %v", snapshot.Desired)
	}
	revision, err := strconv.ParseUint(snapshot.Revision, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	request := appserver.ExperimentalRequest{WriteBase: appserver.WriteBase{OperationID: "remove-legacy", ExpectedRevision: &revision}, Values: experimental.Values{experimental.MCPRefresh: false, experimental.ToolRetirement: true}}
	result, err := client.SetExperimentalFeatures(t.Context(), request)
	if err != nil || result.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("save = %+v %v", result, err)
	}
	raw, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct{ Experimental experimental.Values }
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(saved.Experimental, experimental.Values{experimental.AsyncCompact: true}) {
		t.Fatalf("persisted = %v", saved.Experimental)
	}
	snapshot, err = client.ExperimentalFeatures(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	revision, err = strconv.ParseUint(snapshot.Revision, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	request.OperationID = "legacy-noop"
	result, err = client.SetExperimentalFeatures(t.Context(), request)
	if err != nil || result.Outcome != appserver.OutcomeCommitted || result.Revision != revision {
		t.Fatalf("legacy no-op = %+v %v", result, err)
	}
}
