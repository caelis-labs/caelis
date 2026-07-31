package gatewayapp

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/app/controlserver"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/control/client/httpclient"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelconfig/credentialstore"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/internal/testenv"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

const (
	realMimoE2EEnabledEnv     = "CAELIS_REAL_MIMO_E2E"
	realMimoE2ESourceStoreEnv = "CAELIS_REAL_MIMO_SOURCE_STORE"
	realMimoE2EProfileEnv     = "CAELIS_REAL_MIMO_PROFILE"
	defaultRealMimoProfile    = "provider:xiaomi@token-plan-cn/xiaomi/mimo-v2.5"
)

// TestControlHostRealMimoMultiWorkspaceParallel is an opt-in integration test.
// It copies only the selected Mimo configuration and credential into t.TempDir,
// then exercises the production HTTP/SSE client boundary against one live Host.
func TestControlHostRealMimoMultiWorkspaceParallel(t *testing.T) {
	if strings.TrimSpace(os.Getenv(realMimoE2EEnabledEnv)) != "1" {
		t.Skip("set CAELIS_REAL_MIMO_E2E=1 to run the real-provider app-server E2E")
	}
	sourceStore := strings.TrimSpace(os.Getenv(realMimoE2ESourceStoreEnv))
	if sourceStore == "" {
		t.Fatalf("%s is required", realMimoE2ESourceStoreEnv)
	}
	profileID := strings.TrimSpace(os.Getenv(realMimoE2EProfileEnv))
	if profileID == "" {
		profileID = defaultRealMimoProfile
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	e2eStore := t.TempDir()
	profile, effort := copyRealMimoConfiguration(t, ctx, sourceStore, e2eStore, profileID)
	workspaceA := newWorkspaceRuntimeTestDir(
		t,
		"real-mimo-workspace-a",
		"When the user includes CAELIS_REAL_APP_SERVER_E2E, reply with exactly E2E_WORKSPACE_ALPHA and no other text. Do not call tools.",
	)
	workspaceB := newWorkspaceRuntimeTestDir(
		t,
		"real-mimo-workspace-b",
		"When the user includes CAELIS_REAL_APP_SERVER_E2E, reply with exactly E2E_WORKSPACE_BETA and no other text. Do not call tools.",
	)

	stack, err := NewLocalStack(Config{
		StoreDir:           e2eStore,
		WorkspaceKey:       "real-mimo-workspace-a",
		WorkspaceCWD:       workspaceA,
		ApprovalMode:       "auto-review",
		ModelProfileID:     profile.ID,
		ModelProfileEffort: effort,
		SkillDirs:          []string{},
		Sandbox:            SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := stack.Close(); err != nil {
			t.Errorf("close real Mimo Host: %v", err)
		}
	}()

	const token = "real-mimo-e2e-control-token-0123456789"
	authenticator, err := controlserver.BearerTokenAuthenticator(
		token,
		controlclient.Principal{ID: "local-user"},
	)
	if err != nil {
		t.Fatal(err)
	}
	server, err := controlserver.New(controlserver.HandlerConfig{
		Service:       stack.ControlClient(),
		TaskStreams:   stack.TaskStreams(),
		Authenticator: authenticator,
		AllowedHosts:  []string{"127.0.0.1", "localhost", "::1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := testenv.NewHTTPServer(t, server.Handler())
	defer httpServer.Close()
	remote, err := httpclient.New(httpclient.Config{
		BaseURL:     httpServer.URL,
		BearerToken: token,
		EventBuffer: 256,
		HTTPClient:  httpServer.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if info, err := remote.Initialize(ctx); err != nil ||
		info.APIVersion != controlclient.HTTPAPIVersion ||
		info.EnvelopeVersion != controlclient.EnvelopeVersion {
		t.Fatalf("Initialize() = %#v, %v", info, err)
	}

	specs := []realMimoSessionSpec{
		{sessionID: "real-mimo-a1", workspaceKey: "real-mimo-workspace-a", cwd: workspaceA, marker: "E2E_WORKSPACE_ALPHA"},
		{sessionID: "real-mimo-a2", workspaceKey: "real-mimo-workspace-a", cwd: workspaceA, marker: "E2E_WORKSPACE_ALPHA"},
		{sessionID: "real-mimo-b1", workspaceKey: "real-mimo-workspace-b", cwd: workspaceB, marker: "E2E_WORKSPACE_BETA"},
		{sessionID: "real-mimo-b2", workspaceKey: "real-mimo-workspace-b", cwd: workspaceB, marker: "E2E_WORKSPACE_BETA"},
	}
	for index := range specs {
		spec := &specs[index]
		result, err := remote.CreateSession(ctx, controlclient.CreateSessionRequest{
			WriteBase:          controlclient.WriteBase{OperationID: "create-" + spec.sessionID},
			PreferredSessionID: spec.sessionID,
			WorkspaceKey:       spec.workspaceKey,
			CWD:                spec.cwd,
			Title:              spec.sessionID,
		})
		if err != nil || result.Outcome != controlclient.OutcomeCommitted {
			t.Fatalf("CreateSession(%q) = %#v, %v", spec.sessionID, result, err)
		}
		state, err := remote.InspectSession(ctx, controlclient.StateRequest{SessionID: spec.sessionID})
		if err != nil {
			t.Fatal(err)
		}
		if state.WorkspaceKey != spec.workspaceKey || state.CWD != spec.cwd || state.Run.Active {
			t.Fatalf("initial Session state for %q = %#v", spec.sessionID, state)
		}
	}
	listed, err := remote.ListSessions(ctx, controlclient.ListSessionsRequest{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Sessions) != len(specs) {
		t.Fatalf("ListSessions() returned %d Sessions, want %d", len(listed.Sessions), len(specs))
	}

	type observer struct {
		name         string
		sessionID    string
		marker       string
		subscription controlclient.FeedSubscription
	}
	observers := make([]observer, 0, len(specs)+1)
	for _, spec := range specs {
		reconnected, err := remote.Reconnect(ctx, controlclient.ReconnectRequest{SessionID: spec.sessionID})
		if err != nil {
			t.Fatalf("Reconnect(%q): %v", spec.sessionID, err)
		}
		observers = append(observers, observer{
			name:         spec.sessionID + "-primary",
			sessionID:    spec.sessionID,
			marker:       spec.marker,
			subscription: reconnected.Subscription,
		})
	}
	secondObserver, err := remote.Reconnect(ctx, controlclient.ReconnectRequest{SessionID: specs[0].sessionID})
	if err != nil {
		t.Fatal(err)
	}
	observers = append(observers, observer{
		name:         specs[0].sessionID + "-secondary",
		sessionID:    specs[0].sessionID,
		marker:       specs[0].marker,
		subscription: secondObserver.Subscription,
	})

	observed := make(chan realMimoObservation, len(observers))
	for _, item := range observers {
		go func() {
			observed <- observeRealMimoTurn(ctx, item.name, item.sessionID, item.marker, item.subscription)
		}()
	}

	start := make(chan struct{})
	prompted := make(chan realMimoPromptResult, len(specs))
	var promptWait sync.WaitGroup
	for _, spec := range specs {
		promptWait.Add(1)
		go func() {
			defer promptWait.Done()
			<-start
			result, promptErr := remote.Prompt(ctx, controlclient.PromptRequest{
				WriteBase: controlclient.WriteBase{
					OperationID: "prompt-" + spec.sessionID,
					SessionID:   spec.sessionID,
				},
				Input: "CAELIS_REAL_APP_SERVER_E2E. Follow the workspace-specific instruction exactly.",
			})
			prompted <- realMimoPromptResult{sessionID: spec.sessionID, result: result, err: promptErr}
		}()
	}
	startedAt := time.Now()
	close(start)
	promptWait.Wait()
	close(prompted)
	for prompt := range prompted {
		if prompt.err != nil ||
			prompt.result.Outcome != controlclient.OutcomeCommitted ||
			prompt.result.Target.HandleID == "" ||
			prompt.result.Target.RunID == "" ||
			prompt.result.Target.TurnID == "" {
			t.Fatalf("Prompt(%q) = %#v, %v", prompt.sessionID, prompt.result, prompt.err)
		}
	}

	maxActive := 0
	crossWorkspaceParallel := false
	results := make([]realMimoObservation, 0, len(observers))
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for len(results) < len(observers) {
		select {
		case result := <-observed:
			results = append(results, result)
		case <-ticker.C:
			activeA, activeB := 0, 0
			for _, spec := range specs {
				state, inspectErr := remote.InspectSession(ctx, controlclient.StateRequest{SessionID: spec.sessionID})
				if inspectErr != nil {
					t.Fatalf("InspectSession(%q): %v", spec.sessionID, inspectErr)
				}
				if !state.Run.Active {
					continue
				}
				if spec.workspaceKey == "real-mimo-workspace-a" {
					activeA++
				} else {
					activeB++
				}
			}
			if activeA+activeB > maxActive {
				maxActive = activeA + activeB
			}
			crossWorkspaceParallel = crossWorkspaceParallel || activeA > 0 && activeB > 0
		case <-ctx.Done():
			t.Fatalf("real Mimo parallel Turns timed out: %v", ctx.Err())
		}
	}
	if maxActive < 2 || !crossWorkspaceParallel {
		t.Fatalf("parallel observation = max_active:%d cross_workspace:%t", maxActive, crossWorkspaceParallel)
	}

	primaryCursor := ""
	for _, result := range results {
		if result.err != nil {
			t.Fatalf("observer %q: %v", result.observer, result.err)
		}
		if result.lifecycleState != eventstream.LifecycleStateCompleted {
			t.Fatalf("observer %q lifecycle = %q", result.observer, result.lifecycleState)
		}
		if !strings.Contains(result.finalText, result.marker) {
			t.Fatalf("observer %q final text = %q, want marker %q", result.observer, result.finalText, result.marker)
		}
		foreignMarker := "E2E_WORKSPACE_ALPHA"
		if result.marker == foreignMarker {
			foreignMarker = "E2E_WORKSPACE_BETA"
		}
		if strings.Contains(result.finalText, foreignMarker) {
			t.Fatalf("observer %q received foreign workspace marker in %q", result.observer, result.finalText)
		}
		if result.cursor == "" || result.envelopes == 0 {
			t.Fatalf("observer %q observation = cursor:%q envelopes:%d", result.observer, result.cursor, result.envelopes)
		}
		if result.observer == specs[0].sessionID+"-primary" {
			primaryCursor = result.cursor
		}
	}
	if primaryCursor == "" {
		t.Fatal("primary observer did not retain a resume Cursor")
	}
	resumed, err := remote.Reconnect(ctx, controlclient.ReconnectRequest{
		SessionID: specs[0].sessionID,
		Cursor:    primaryCursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	resumedBackfill := 0
	for range resumed.Subscription.Backfill() {
		resumedBackfill++
	}
	if err := resumed.Subscription.Close(); err != nil {
		t.Fatal(err)
	}
	if resumed.State.Run.Active ||
		resumed.State.ResumeMode != controlclient.ResumeModeExact ||
		resumedBackfill != 0 {
		t.Fatalf(
			"resume from terminal Cursor = active:%t mode:%q backfill:%d",
			resumed.State.Run.Active,
			resumed.State.ResumeMode,
			resumedBackfill,
		)
	}

	for _, spec := range specs {
		result, err := remote.CloseSession(ctx, controlclient.CloseSessionRequest{
			WriteBase: controlclient.WriteBase{
				OperationID: "close-" + spec.sessionID,
				SessionID:   spec.sessionID,
			},
		})
		if err != nil || result.Outcome != controlclient.OutcomeCommitted {
			t.Fatalf("CloseSession(%q) = %#v, %v", spec.sessionID, result, err)
		}
		if _, err := remote.InspectSession(ctx, controlclient.StateRequest{SessionID: spec.sessionID}); err != nil {
			t.Fatalf("InspectSession(%q) after close: %v", spec.sessionID, err)
		}
	}
	t.Logf(
		"real Mimo app-server E2E passed: profile=%s Sessions=%d observers=%d max_active=%d cross_workspace=%t elapsed=%s",
		profile.ID,
		len(specs),
		len(observers),
		maxActive,
		crossWorkspaceParallel,
		time.Since(startedAt).Round(time.Millisecond),
	)
}

type realMimoSessionSpec struct {
	sessionID    string
	workspaceKey string
	cwd          string
	marker       string
}

type realMimoPromptResult struct {
	sessionID string
	result    controlclient.CommandResult
	err       error
}

type realMimoObservation struct {
	observer       string
	sessionID      string
	marker         string
	finalText      string
	lifecycleState string
	cursor         string
	envelopes      int
	err            error
}

func observeRealMimoTurn(
	ctx context.Context,
	observer string,
	sessionID string,
	marker string,
	subscription controlclient.FeedSubscription,
) realMimoObservation {
	result := realMimoObservation{observer: observer, sessionID: sessionID, marker: marker}
	defer func() {
		if err := subscription.Close(); result.err == nil && err != nil {
			result.err = err
		}
	}()
	var accumulator schema.FinalAssistantAccumulator
	observe := func(envelope eventstream.Envelope) bool {
		result.envelopes++
		if envelope.SessionID != sessionID {
			result.err = fmt.Errorf("received foreign Session %q", envelope.SessionID)
			return true
		}
		if envelope.Update != nil {
			accumulator.ObserveUpdate(envelope.Update)
		}
		if eventstream.IsTurnTerminalLifecycle(envelope) {
			result.lifecycleState = envelope.Lifecycle.State
			return true
		}
		return false
	}
	for envelope := range subscription.Backfill() {
		if observe(envelope) {
			result.finalText = strings.TrimSpace(accumulator.FinalText())
			result.cursor = subscription.LastCursor()
			return result
		}
	}
	for {
		select {
		case envelope, ok := <-subscription.Events():
			if !ok {
				result.err = subscription.Err()
				if result.err == nil {
					result.err = fmt.Errorf("Session feed closed before terminal lifecycle")
				}
				return result
			}
			if observe(envelope) {
				result.finalText = strings.TrimSpace(accumulator.FinalText())
				result.cursor = subscription.LastCursor()
				return result
			}
		case <-ctx.Done():
			result.err = ctx.Err()
			return result
		}
	}
}

func copyRealMimoConfiguration(
	t *testing.T,
	ctx context.Context,
	sourceStore string,
	targetStore string,
	profileID string,
) (modelprofile.ModelProfile, string) {
	t.Helper()
	doc, err := LoadAppConfig(sourceStore)
	if err != nil {
		t.Fatal(err)
	}
	profiles := modelprofile.NormalizeConfiguration(doc.ModelProfiles)
	profileID = modelprofile.NormalizeID(profileID)
	var selected modelprofile.ModelProfile
	for _, profile := range profiles.Profiles {
		if profile.ID == profileID {
			selected = profile
			break
		}
	}
	if selected.ID == "" ||
		selected.Backend.Provider == nil ||
		!strings.Contains(selected.ID, "/xiaomi/mimo-") {
		t.Fatalf("Mimo provider profile %q is unavailable", profileID)
	}
	modelConfigID := selected.Backend.Provider.ModelConfigID
	var selectedModel ModelConfig
	for _, configured := range doc.Models.Configs {
		normalized := modelconfig.NormalizeConfig(configured)
		if normalized.ID == modelConfigID {
			selectedModel = configured
			break
		}
	}
	if strings.TrimSpace(selectedModel.ID) == "" {
		t.Fatalf("Mimo model config %q is unavailable", modelConfigID)
	}
	endpointID := modelconfig.NormalizeConfig(selectedModel).ProviderEndpointID
	var selectedEndpoint ProviderEndpointConfig
	for _, endpoint := range doc.Models.ProviderEndpoints {
		normalized := modelconfig.NormalizeProviderEndpoint(endpoint)
		if normalized.ID == endpointID {
			selectedEndpoint = endpoint
			break
		}
	}
	credentialRef := modelconfig.NormalizeProviderEndpoint(selectedEndpoint).CredentialRef
	if credentialRef == "" {
		t.Fatalf("Mimo endpoint %q has no managed credential", endpointID)
	}

	targetDoc := AppConfig{
		SchemaVersion: doc.SchemaVersion,
		Models: persistedModelConfig{
			ProviderEndpoints: []ProviderEndpointConfig{selectedEndpoint},
			Configs:           []ModelConfig{selectedModel},
		},
		ModelProfiles: modelprofile.Configuration{
			DefaultProfileID: selected.ID,
			DefaultEffort:    selected.Effort.DefaultEffort,
			Profiles:         []modelprofile.ModelProfile{selected},
		},
		Runtime: RuntimeConfig{ApprovalMode: "auto-review"},
	}
	if err := newAppConfigStore(targetStore).Save(targetDoc); err != nil {
		t.Fatal(err)
	}
	sourceCredentials, err := credentialstore.New(sourceStore)
	if err != nil {
		t.Fatal(err)
	}
	targetCredentials, err := credentialstore.New(targetStore)
	if err != nil {
		t.Fatal(err)
	}
	apiKey, err := sourceCredentials.Get(ctx, credentialRef)
	if err != nil {
		t.Fatalf("load managed Mimo credential: %v", err)
	}
	if err := targetCredentials.Put(ctx, credentialRef, apiKey); err != nil {
		t.Fatalf("copy managed Mimo credential: %v", err)
	}

	effort := selected.Effort.DefaultEffort
	for _, choice := range selected.Effort.Choices {
		if choice.Canonical == "none" {
			effort = "none"
			break
		}
	}
	return selected, effort
}
