//go:build e2e

package eval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/core/config"
	"github.com/OnslaughtSnail/caelis/core/model"
	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/internal/app/local"
	"github.com/OnslaughtSnail/caelis/internal/app/services"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
)

func TestLocalStackProviderLiveReasoningBoundaryFromAppSettingsE2E(t *testing.T) {
	if strings.TrimSpace(os.Getenv("CAELIS_LIVE_E2E")) == "" {
		t.Skip("set CAELIS_LIVE_E2E=1 to run local-config live provider e2e")
	}
	modelCfg, ok := loadLocalAppModelConfig(t, "minimax")
	if !ok {
		t.Skip("no local minimax app settings config found")
	}
	modelCfg.ReasoningEffort = "high"
	modelCfg.DefaultReasoningEffort = "high"

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatalf("settings.NewManager() error = %v", err)
	}
	modelCfg, err = manager.UpsertModel(ctx, modelCfg)
	if err != nil {
		t.Fatalf("settings.UpsertModel() error = %v", err)
	}
	if _, err := manager.SetDefaultModel(ctx, modelCfg.ID); err != nil {
		t.Fatalf("settings.SetDefaultModel() error = %v", err)
	}
	workdir := t.TempDir()
	stack, err := local.New(local.Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "gateway-live-local-config-e2e",
			WorkspaceKey: "gateway-live-local-config-e2e",
			WorkspaceCWD: workdir,
			Store: config.Store{
				Backend: "jsonl",
				URI:     filepath.Join(t.TempDir(), "sessions"),
			},
		},
		Settings: manager,
	})
	if err != nil {
		t.Fatalf("local.New() error = %v", err)
	}
	activeSession, err := stack.Services().Sessions().Start(ctx, services.StartSessionRequest{
		PreferredSessionID: "gateway-live-local-config-e2e",
		Workspace: session.Workspace{
			Key: "gateway-live-local-config-e2e",
			CWD: workdir,
		},
		Title: "gateway-live-local-config-e2e",
	})
	if err != nil {
		t.Fatalf("Sessions.Start() error = %v", err)
	}
	if _, err := stack.Services().Models().Use(ctx, activeSession.Ref, modelCfg.ID, "high"); err != nil {
		t.Fatalf("Models.Use(%s, high) error = %v", modelCfg.ID, err)
	}

	turn, err := stack.Services().Turns().Begin(ctx, services.BeginTurnRequest{
		SessionRef: activeSession.Ref,
		Input:      "介绍一下你自己。",
		Surface:    "cli-tui",
	})
	if err != nil {
		t.Fatalf("Turns.Begin() error = %v", err)
	}
	defer turn.Close()

	var trace liveReasoningTrace
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatalf("turn event error = %s", env.Err)
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

type liveReasoningTrace struct {
	reasoningChunks   []string
	answerChunks      []string
	finalRawReasoning string
	finalRawText      string
	finalEventText    string
	finalPayloadText  string
	firstOverlapLayer string
}

func (t *liveReasoningTrace) capture(env coreruntime.EventEnvelope) {
	if t == nil || env.Event.Type != session.EventAssistant || env.Event.Message == nil {
		return
	}
	if env.Event.Visibility == session.VisibilityUIOnly {
		return
	}
	if env.Event.Visibility != session.VisibilityCanonical {
		return
	}
	t.finalRawReasoning = messageReasoningText(*env.Event.Message)
	t.finalRawText = strings.TrimSpace(env.Event.Message.TextContent())
	t.finalEventText = session.EventText(env.Event)
	t.finalPayloadText = t.finalRawText
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

func messageReasoningText(message model.Message) string {
	var parts []string
	for _, part := range message.Parts {
		if part.Kind != model.PartReasoning || part.Reasoning == nil {
			continue
		}
		if text := strings.TrimSpace(part.Reasoning.VisibleText); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func loadLocalAppModelConfig(t *testing.T, provider string) (appsettings.ModelConfig, bool) {
	t.Helper()

	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		t.Logf("UserHomeDir() error = %v", err)
		return appsettings.ModelConfig{}, false
	}
	root := filepath.Join(home, ".caelis")
	store := appsettings.NewFileStore(root)
	doc, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load app settings config %q error = %v", root, err)
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	defaultID := strings.ToLower(strings.TrimSpace(doc.Models.DefaultID))
	for _, cfg := range doc.Models.Configs {
		cfg = appsettings.NormalizeModelConfig(cfg)
		if strings.ToLower(strings.TrimSpace(cfg.Provider)) != provider {
			continue
		}
		if defaultID != "" && strings.ToLower(strings.TrimSpace(cfg.ID)) == defaultID {
			return cfg, true
		}
	}
	for _, cfg := range doc.Models.Configs {
		cfg = appsettings.NormalizeModelConfig(cfg)
		if strings.ToLower(strings.TrimSpace(cfg.Provider)) == provider {
			return cfg, true
		}
	}
	return appsettings.ModelConfig{}, false
}
