//go:build e2e

package gateway_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
	appgateway "github.com/OnslaughtSnail/caelis/gateway"
	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	"github.com/OnslaughtSnail/caelis/sdk/model/providers/e2etest"
	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	"github.com/OnslaughtSnail/caelis/sdk/runtime/agents/chat"
	localruntime "github.com/OnslaughtSnail/caelis/sdk/runtime/local"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sessionfile "github.com/OnslaughtSnail/caelis/sdk/session/file"
)

func TestGatewayProviderLiveTurnAndReplayE2E(t *testing.T) {
	spec := e2etest.RequireLLM(t, e2etest.Config{
		DefaultProvider: "minimax",
		DefaultModel:    "MiniMax-M2",
		Timeout:         90 * time.Second,
		MaxTokens:       256,
	})

	gw, session := newGatewayProviderStack(t, spec.LLM)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	start := time.Now()
	result, err := gw.BeginTurn(ctx, appgateway.BeginTurnRequest{
		SessionRef: session.SessionRef,
		Input:      "Reply with exactly: gateway provider live e2e ok",
		Surface:    "cli-tui",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	defer result.Handle.Close()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("BeginTurn() blocked for %s, want live handle under 1s", elapsed)
	}

	var (
		sawUser      bool
		sawChunk     bool
		finalText    string
		firstEventAt time.Time
	)
	for env := range result.Handle.Events() {
		if env.Err != nil {
			t.Fatalf("handle event error = %v", env.Err)
		}
		payload := env.Event.Narrative
		if payload == nil {
			continue
		}
		if firstEventAt.IsZero() {
			firstEventAt = time.Now()
		}
		switch {
		case payload.Role == appgateway.NarrativeRoleUser:
			sawUser = true
		case payload.Role == appgateway.NarrativeRoleAssistant &&
			payload.Visibility == string(sdksession.VisibilityUIOnly) &&
			(payload.UpdateType == string(sdksession.ProtocolUpdateTypeAgentMessage) ||
				payload.UpdateType == string(sdksession.ProtocolUpdateTypeAgentThought)):
			sawChunk = true
		case payload.Role == appgateway.NarrativeRoleAssistant &&
			payload.Visibility == string(sdksession.VisibilityCanonical):
			finalText = strings.TrimSpace(payload.Text)
		}
	}
	if firstEventAt.IsZero() {
		t.Fatal("expected at least one live gateway event")
	}
	if delay := firstEventAt.Sub(start); delay > 2*time.Second {
		t.Fatalf("first gateway event arrived after %s, want under 2s", delay)
	}
	if !sawUser {
		t.Fatal("expected live user event")
	}
	if !sawChunk {
		t.Fatal("expected ACP-compatible assistant chunk/thought event before final response")
	}
	if got := strings.TrimSpace(finalText); got != "gateway provider live e2e ok" {
		t.Fatalf("final assistant = %q, want %q", got, "gateway provider live e2e ok")
	}

	replayed, err := gw.ReplayEvents(ctx, appgateway.ReplayEventsRequest{
		SessionRef: session.SessionRef,
	})
	if err != nil {
		t.Fatalf("ReplayEvents() error = %v", err)
	}
	if replayed.HasLiveHandle {
		t.Fatal("ReplayEvents().HasLiveHandle = true, want false after turn completion")
	}
	var (
		replayUser  bool
		replayFinal string
	)
	for _, env := range replayed.Events {
		payload := env.Event.Narrative
		if payload == nil {
			continue
		}
		switch payload.Role {
		case appgateway.NarrativeRoleUser:
			replayUser = true
		case appgateway.NarrativeRoleAssistant:
			if payload.Visibility == string(sdksession.VisibilityUIOnly) {
				t.Fatalf("ReplayEvents() included transient UI-only event: %+v", payload)
			}
			replayFinal = strings.TrimSpace(payload.Text)
		}
	}
	if !replayUser {
		t.Fatal("expected replayed user event")
	}
	if replayFinal != "gateway provider live e2e ok" {
		t.Fatalf("replayed final assistant = %q, want %q", replayFinal, "gateway provider live e2e ok")
	}
}

func TestGatewayProviderNonStreamingOverrideE2E(t *testing.T) {
	spec := e2etest.RequireLLM(t, e2etest.Config{
		DefaultProvider: "minimax",
		DefaultModel:    "MiniMax-M2",
		Timeout:         90 * time.Second,
		MaxTokens:       256,
	})

	gw, session := newGatewayProviderStack(t, spec.LLM)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	result, err := gw.BeginTurn(ctx, appgateway.BeginTurnRequest{
		SessionRef: session.SessionRef,
		Input:      "Reply with exactly: gateway provider nonstream e2e ok",
		Surface:    "headless",
		Request: sdkruntime.ModelRequestOptions{
			Stream: boolPtr(false),
		},
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	defer result.Handle.Close()

	var (
		sawChunk  bool
		finalText string
	)
	for env := range result.Handle.Events() {
		if env.Err != nil {
			t.Fatalf("handle event error = %v", env.Err)
		}
		payload := env.Event.Narrative
		if payload == nil || payload.Role != appgateway.NarrativeRoleAssistant {
			continue
		}
		if payload.Visibility == string(sdksession.VisibilityUIOnly) {
			sawChunk = true
		}
		if payload.Visibility == string(sdksession.VisibilityCanonical) {
			finalText = strings.TrimSpace(payload.Text)
		}
	}
	if sawChunk {
		t.Fatal("expected no UI-only chunk events when stream=false")
	}
	if got := strings.TrimSpace(finalText); got != "gateway provider nonstream e2e ok" {
		t.Fatalf("final assistant = %q, want %q", got, "gateway provider nonstream e2e ok")
	}
}

func TestGatewayProviderHeadlessDefaultNonStreamingE2E(t *testing.T) {
	spec := e2etest.RequireLLM(t, e2etest.Config{
		DefaultProvider: "minimax",
		DefaultModel:    "MiniMax-M2",
		Timeout:         90 * time.Second,
		MaxTokens:       256,
	})

	gw, session := newGatewayProviderStack(t, spec.LLM)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	result, err := gw.BeginTurn(ctx, appgateway.BeginTurnRequest{
		SessionRef: session.SessionRef,
		Input:      "Reply with exactly: gateway provider headless default e2e ok",
		Surface:    "headless",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	defer result.Handle.Close()

	var (
		sawChunk  bool
		finalText string
	)
	for env := range result.Handle.Events() {
		if env.Err != nil {
			t.Fatalf("handle event error = %v", env.Err)
		}
		payload := env.Event.Narrative
		if payload == nil || payload.Role != appgateway.NarrativeRoleAssistant {
			continue
		}
		if payload.Visibility == string(sdksession.VisibilityUIOnly) {
			sawChunk = true
		}
		if payload.Visibility == string(sdksession.VisibilityCanonical) {
			finalText = strings.TrimSpace(payload.Text)
		}
	}
	if sawChunk {
		t.Fatal("expected no UI-only chunk events for headless default surface policy")
	}
	if got := strings.TrimSpace(finalText); got != "gateway provider headless default e2e ok" {
		t.Fatalf("final assistant = %q, want %q", got, "gateway provider headless default e2e ok")
	}
}

func TestGatewayProviderLiveReasoningBoundaryE2E(t *testing.T) {
	spec := e2etest.RequireLLM(t, e2etest.Config{
		DefaultProvider: "minimax",
		DefaultModel:    "MiniMax-M2",
		Timeout:         90 * time.Second,
		MaxTokens:       512,
	})

	gw, session := newGatewayProviderStackWithMetadata(t, spec.LLM, map[string]any{
		"reasoning_effort": "high",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	result, err := gw.BeginTurn(ctx, appgateway.BeginTurnRequest{
		SessionRef: session.SessionRef,
		Input:      "介绍一下你自己。",
		Surface:    "cli-tui",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	defer result.Handle.Close()

	var trace liveReasoningTrace
	for env := range result.Handle.Events() {
		if env.Err != nil {
			t.Fatalf("handle event error = %v", env.Err)
		}
		trace.capture(env)
	}
	if strings.TrimSpace(trace.finalRawReasoning) == "" {
		t.Fatalf("expected live provider to emit final reasoning under high effort; trace:\n%s", trace.summary())
	}
	if trace.firstOverlapLayer != "" {
		t.Fatalf("reasoning/answer overlap first appears in %s; trace:\n%s", trace.firstOverlapLayer, trace.summary())
	}
	t.Logf("live reasoning trace:\n%s", trace.summary())
}

func TestGatewayProviderLiveReasoningBoundaryFromLocalConfigE2E(t *testing.T) {
	if strings.TrimSpace(os.Getenv("CAELIS_LIVE_E2E")) == "" {
		t.Skip("set CAELIS_LIVE_E2E=1 to run local-config live provider e2e")
	}
	modelCfg, ok := loadLocalGatewayModelConfig(t, "minimax")
	if !ok {
		t.Skip("no local minimax gateway config found")
	}
	modelCfg.ReasoningEffort = "high"

	stack, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "gateway-live-local-config-e2e",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   "gateway-live-local-config-e2e",
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Model:          modelCfg,
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	session, err := stack.StartSession(context.Background(), "gateway-live-local-config-e2e", "")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	result, err := stack.Gateway.BeginTurn(ctx, appgateway.BeginTurnRequest{
		SessionRef: session.SessionRef,
		Input:      "介绍一下你自己。",
		Surface:    "cli-tui",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	defer result.Handle.Close()

	var trace liveReasoningTrace
	for env := range result.Handle.Events() {
		if env.Err != nil {
			t.Fatalf("handle event error = %v", env.Err)
		}
		trace.capture(env)
	}
	if strings.TrimSpace(trace.finalRawReasoning) == "" {
		t.Fatalf("expected live provider to emit final reasoning from local config; trace:\n%s", trace.summary())
	}
	if trace.firstOverlapLayer != "" {
		t.Fatalf("reasoning/answer overlap first appears in %s; trace:\n%s", trace.firstOverlapLayer, trace.summary())
	}
	t.Logf("live reasoning trace from local config:\n%s", trace.summary())
}

func newGatewayProviderStack(t *testing.T, model sdkmodel.LLM) (*appgateway.Gateway, sdksession.Session) {
	return newGatewayProviderStackWithMetadata(t, model, nil)
}

func newGatewayProviderStackWithMetadata(t *testing.T, model sdkmodel.LLM, metadata map[string]any) (*appgateway.Gateway, sdksession.Session) {
	t.Helper()

	root := t.TempDir()
	sessions := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: root}))
	rt, err := localruntime.New(localruntime.Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Answer tersely.",
		},
	})
	if err != nil {
		t.Fatalf("localruntime.New() error = %v", err)
	}
	gw, err := appgateway.New(appgateway.Config{
		Sessions: sessions,
		Runtime:  rt,
		Resolver: testResolver{model: model, metadata: metadata},
	})
	if err != nil {
		t.Fatalf("gateway.New() error = %v", err)
	}
	session, err := gw.StartSession(context.Background(), appgateway.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: sdksession.WorkspaceRef{
			Key: "ws-gateway-e2e",
			CWD: root,
		},
		PreferredSessionID: "gateway-provider-e2e",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	return gw, session
}

type liveReasoningTrace struct {
	reasoningChunks   []string
	answerChunks      []string
	finalRawReasoning string
	finalRawText      string
	finalEventText    string
	finalPayloadText  string
	firstOverlapLayer string
}

func (t *liveReasoningTrace) capture(env appgateway.EventEnvelope) {
	if t == nil || env.Event.Narrative == nil {
		return
	}
	payload := env.Event.Narrative
	if payload.Role != appgateway.NarrativeRoleAssistant {
		return
	}
	if payload.Visibility == string(sdksession.VisibilityUIOnly) {
		switch payload.UpdateType {
		case string(sdksession.ProtocolUpdateTypeAgentThought):
			t.reasoningChunks = append(t.reasoningChunks, strings.TrimSpace(payload.ReasoningText))
		case string(sdksession.ProtocolUpdateTypeAgentMessage):
			t.answerChunks = append(t.answerChunks, strings.TrimSpace(payload.Text))
		}
		return
	}
	if payload.Visibility != string(sdksession.VisibilityCanonical) {
		return
	}
	t.finalRawReasoning = strings.TrimSpace(payload.ReasoningText)
	t.finalRawText = strings.TrimSpace(payload.Text)
	t.finalEventText = strings.TrimSpace(payload.Text)
	t.finalPayloadText = strings.TrimSpace(payload.Text)
	t.classify()
}

func (t *liveReasoningTrace) classify() {
	if t == nil || t.firstOverlapLayer != "" {
		return
	}
	switch {
	case hasReasoningPrefixOverlap(t.finalRawText, t.finalRawReasoning):
		t.firstOverlapLayer = "provider final message"
	case hasReasoningPrefixOverlap(t.finalEventText, t.finalRawReasoning):
		t.firstOverlapLayer = "runtime session event"
	case hasReasoningPrefixOverlap(t.finalPayloadText, t.finalRawReasoning):
		t.firstOverlapLayer = "gateway canonical narrative"
	}
}

func (t *liveReasoningTrace) summary() string {
	if t == nil {
		return ""
	}
	return fmt.Sprintf(
		"reasoning_chunks=%q\nanswer_chunks=%q\nfinal_raw_reasoning=%q\nfinal_raw_text=%q\nfinal_event_text=%q\nfinal_payload_text=%q\nfirst_overlap_layer=%q",
		t.reasoningChunks,
		t.answerChunks,
		t.finalRawReasoning,
		t.finalRawText,
		t.finalEventText,
		t.finalPayloadText,
		t.firstOverlapLayer,
	)
}

func hasReasoningPrefixOverlap(text string, reasoning string) bool {
	text = strings.TrimSpace(text)
	reasoning = strings.TrimSpace(reasoning)
	return text != "" && reasoning != "" && strings.HasPrefix(text, reasoning)
}

func loadLocalGatewayModelConfig(t *testing.T, provider string) (gatewayapp.ModelConfig, bool) {
	t.Helper()

	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		t.Logf("UserHomeDir() error = %v", err)
		return gatewayapp.ModelConfig{}, false
	}
	root := filepath.Join(home, ".caelis")
	doc, err := gatewayapp.LoadAppConfig(root)
	if err != nil {
		t.Fatalf("LoadAppConfig(%q) error = %v", root, err)
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	defaultAlias := strings.ToLower(strings.TrimSpace(doc.Models.DefaultAlias))
	for _, cfg := range doc.Models.Configs {
		if strings.ToLower(strings.TrimSpace(cfg.Provider)) != provider {
			continue
		}
		if defaultAlias != "" && strings.ToLower(strings.TrimSpace(cfg.Alias)) == defaultAlias {
			return cfg, true
		}
	}
	for _, cfg := range doc.Models.Configs {
		if strings.ToLower(strings.TrimSpace(cfg.Provider)) == provider {
			return cfg, true
		}
	}
	return gatewayapp.ModelConfig{}, false
}

type testResolver struct {
	model    sdkmodel.LLM
	metadata map[string]any
}

func (r testResolver) ResolveTurn(_ context.Context, intent appgateway.TurnIntent) (appgateway.ResolvedTurn, error) {
	metadata := map[string]any{}
	for key, value := range r.metadata {
		metadata[key] = value
	}
	return appgateway.ResolvedTurn{
		RunRequest: sdkruntime.RunRequest{
			SessionRef: intent.SessionRef,
			Input:      intent.Input,
			AgentSpec: sdkruntime.AgentSpec{
				Name:     "main",
				Model:    r.model,
				Metadata: metadata,
			},
		},
	}, nil
}

func boolPtr(v bool) *bool { return &v }
