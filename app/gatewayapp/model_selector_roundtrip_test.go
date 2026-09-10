package gatewayapp

import (
	"context"
	"net/http"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/kernel"
)

// TestSessionPublicSelectorRoundTripsIntoResolvedModelRequest proves that
// UseSessionModel accepts a public non-default selector, persists the exact
// internal model configuration and ModelProfile identity behind it, and that a
// reopened Host resolves the same model and provider endpoint through the
// Runtime Turn request path rather than presentation state.
func TestSessionPublicSelectorRoundTripsIntoResolvedModelRequest(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workspace := t.TempDir()
	const (
		appName  = "caelis"
		userID   = "selector-round-trip"
		selector = "xiaomi@token-plan-cn/mimo-v2.5-pro"
		secret   = "token-plan-round-trip-secret"
	)

	first, err := newGatewayAppTestStack(t, Config{
		AppName:      appName,
		UserID:       userID,
		StoreDir:     root,
		WorkspaceKey: workspace,
		WorkspaceCWD: workspace,
		ApprovalMode: "auto-review",
		Assembly:     assembly.ResolvedAssembly{},
		Model: ModelConfig{
			Provider: "ollama", API: providers.APIOllama, Model: "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack(first) error = %v", err)
	}
	active, err := startGatewayAppTestSession(ctx, first, "selector-round-trip-session")
	if err != nil {
		_ = first.Close()
		t.Fatalf("StartSession() error = %v", err)
	}
	hostDefault := first.composition.lookup.DefaultID()
	if hostDefault == "" {
		_ = first.Close()
		t.Fatal("first stack has no default model")
	}

	// The standard Xiaomi endpoint supplies the visible alias the token-plan
	// endpoint also carries, so only a fully qualified public selector can name
	// the intended route.
	if _, err := first.connectTestModel(ModelConfig{
		Provider: "xiaomi", API: providers.APIMimo, Model: "mimo-v2.5-pro",
		BaseURL: modelconfig.XiaomiAPIBaseURL, Token: "api-cn-round-trip-secret",
	}); err != nil {
		_ = first.Close()
		t.Fatalf("Connect(api-cn) error = %v", err)
	}
	tokenPlanProfile, err := first.connectTestModel(ModelConfig{
		Provider: "xiaomi", API: providers.APIMimo, Model: "mimo-v2.5-pro",
		BaseURL: modelconfig.XiaomiTokenPlanCNBaseURL, Token: secret,
	})
	if err != nil {
		_ = first.Close()
		t.Fatalf("Connect(token-plan-cn) error = %v", err)
	}
	tokenPlanID := tokenPlanProfile.Backend.Provider.ModelConfigID
	if tokenPlanID == hostDefault {
		_ = first.Close()
		t.Fatalf("token-plan profile %q is the Host default; the selector must be non-default", tokenPlanID)
	}
	if got := first.composition.lookup.DefaultID(); got != hostDefault {
		_ = first.Close()
		t.Fatalf("connecting the token-plan profile moved the Host default to %q", got)
	}
	tokenPlanConfig, err := first.composition.lookup.ResolveConfig(selector)
	if err != nil {
		_ = first.Close()
		t.Fatalf("ResolveConfig(%q) error = %v", selector, err)
	}
	if tokenPlanConfig.ID != tokenPlanID {
		_ = first.Close()
		t.Fatalf("selector %q resolved to %q, want %q", selector, tokenPlanConfig.ID, tokenPlanID)
	}
	if got := modelconfig.PublicSelector(tokenPlanConfig); got != selector {
		_ = first.Close()
		t.Fatalf("PublicSelector(token-plan profile) = %q, want %q", got, selector)
	}

	current := mustCurrentSession(t, first, active.SessionID)
	selected, err := first.ConfigurationCommands().UseSessionModel(ctx, appserver.Principal{ID: userID}, appserver.SessionModelRequest{
		WriteBase: appserver.WriteBase{
			OperationID:             "select-public-token-plan-selector",
			SessionID:               current.SessionID,
			ExpectedRevision:        &current.Revision,
			ExpectedControllerEpoch: current.Controller.EpochID,
		},
		Model: selector,
	})
	if err != nil || selected.Outcome != appserver.OutcomeCommitted {
		_ = first.Close()
		t.Fatalf("UseSessionModel(%q) = %#v, %v", selector, selected, err)
	}

	persistedState, err := first.composition.sessions.SnapshotState(ctx, active.SessionRef)
	if err != nil {
		_ = first.Close()
		t.Fatalf("SnapshotState() error = %v", err)
	}
	if got := kernel.CurrentModelAlias(persistedState); got != tokenPlanID {
		_ = first.Close()
		t.Fatalf("persisted Session model = %q, want internal model config id %q", got, tokenPlanID)
	}

	doc, err := LoadAppConfig(root)
	if err != nil {
		_ = first.Close()
		t.Fatalf("LoadAppConfig() error = %v", err)
	}
	var durable ModelConfig
	configured := false
	for _, cfg := range doc.Models.Configs {
		if cfg.ID == tokenPlanID {
			durable, configured = cfg, true
		}
	}
	if !configured {
		_ = first.Close()
		t.Fatalf("persisted model configs = %#v, missing %q", doc.Models.Configs, tokenPlanID)
	}
	if durable.ProviderEndpointID != "xiaomi@token-plan-cn" || durable.Model != "mimo-v2.5-pro" {
		_ = first.Close()
		t.Fatalf("persisted token-plan config = %#v, want the token-plan-cn provider endpoint", durable)
	}
	var durableEndpoint ProviderEndpointConfig
	endpointConfigured := false
	for _, endpoint := range doc.Models.ProviderEndpoints {
		if endpoint.ID == "xiaomi@token-plan-cn" {
			durableEndpoint, endpointConfigured = endpoint, true
		}
	}
	if !endpointConfigured || durableEndpoint.Provider != "xiaomi" || durableEndpoint.BaseURL != modelconfig.XiaomiTokenPlanCNBaseURL {
		_ = first.Close()
		t.Fatalf("persisted provider endpoints = %#v, want the token-plan-cn endpoint", doc.Models.ProviderEndpoints)
	}
	if durableEndpoint.CredentialRef == "" {
		_ = first.Close()
		t.Fatal("persisted token-plan endpoint lost its provider credential reference")
	}
	profileID := modelprofile.BuildProviderID(tokenPlanID)
	if _, ok := modelprofile.Lookup(doc.ModelProfiles, profileID); !ok {
		_ = first.Close()
		t.Fatalf("persisted model profiles = %#v, missing %q", doc.ModelProfiles.Profiles, profileID)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close(first) error = %v", err)
	}

	reloaded, err := newGatewayAppTestStack(t, Config{
		AppName: appName, UserID: userID, StoreDir: root,
		WorkspaceKey: workspace, WorkspaceCWD: workspace,
		ApprovalMode: "auto-review", Assembly: assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack(reloaded) error = %v", err)
	}
	defer reloaded.Close()
	if got := reloaded.composition.lookup.DefaultID(); got != hostDefault || got == tokenPlanID {
		t.Fatalf("reloaded Host default = %q, want unchanged non-selected default %q", got, hostDefault)
	}
	var resolvedConfigs []ModelConfig
	reloaded.composition.lookup.resolveTransportHTTPClient = func(_ context.Context, cfg ModelConfig) (*http.Client, error) {
		resolvedConfigs = append(resolvedConfigs, cfg)
		return http.DefaultClient, nil
	}

	client, err := appserver.BindSessionClient(reloaded.ControlClient(), appserver.Principal{ID: userID})
	if err != nil {
		t.Fatalf("BindSessionClient() error = %v", err)
	}
	reconnected, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: active.SessionID})
	if err != nil {
		t.Fatalf("Reconnect() error = %v", err)
	}
	if reconnected.Subscription != nil {
		defer func() { _ = reconnected.Subscription.Close() }()
	}
	resumedRef := session.SessionRef{SessionID: reconnected.State.SessionID}
	runtimeState, err := reloaded.ControlStatus().SessionRuntimeState(ctx, resumedRef)
	if err != nil || runtimeState.ModelID != tokenPlanID || runtimeState.ModelAlias != "xiaomi/mimo-v2.5-pro" {
		t.Fatalf("SessionRuntimeState(reloaded) = %#v, %v; want %q", runtimeState, err, tokenPlanID)
	}

	resolved, err := reloaded.composition.currentGateway().Resolver().ResolveTurn(ctx, kernel.TurnIntent{SessionRef: resumedRef})
	if err != nil {
		t.Fatalf("ResolveTurn(reloaded) error = %v", err)
	}
	if len(resolvedConfigs) != 1 {
		t.Fatalf("ResolveTurn resolved provider configurations = %#v, want exactly the selected one", resolvedConfigs)
	}
	used := resolvedConfigs[0]
	if used.ID != tokenPlanID || used.ProviderEndpointID != "xiaomi@token-plan-cn" ||
		used.BaseURL != modelconfig.XiaomiTokenPlanCNBaseURL || used.Model != "mimo-v2.5-pro" {
		t.Fatalf("ResolveTurn provider configuration = %#v, want the persisted token-plan model", used)
	}
	if used.Token != secret {
		t.Fatalf("ResolveTurn credential = %q, want the persisted provider credential", used.Token)
	}
	requestModel := resolved.RunRequest.AgentSpec.Model
	if requestModel == nil {
		t.Fatal("ResolveTurn produced no request model")
	}
	if got := requestModel.Name(); got != "mimo-v2.5-pro" {
		t.Fatalf("request model name = %q, want mimo-v2.5-pro", got)
	}
	if provider, ok := requestModel.(interface{ ProviderName() string }); !ok || provider.ProviderName() != "xiaomi" {
		t.Fatalf("request model provider = %T, want provider-backed xiaomi model", requestModel)
	}
}
