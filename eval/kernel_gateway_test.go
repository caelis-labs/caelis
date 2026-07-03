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

	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

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
		AppName:      "caelis",
		UserID:       "gateway-live-local-config-e2e",
		StoreDir:     t.TempDir(),
		WorkspaceKey: "gateway-live-local-config-e2e",
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Model:        modelCfg,
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	activeSession, err := stack.StartSession(context.Background(), "gateway-live-local-config-e2e", "")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	result, err := stack.KernelTurns().BeginTurn(ctx, gateway.BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "介绍一下你自己。",
		Surface:    "cli-tui",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	defer result.Handle.Close()

	var trace liveReasoningTrace
	for env := range result.Handle.ACPEvents() {
		if env.Kind == eventstream.KindError {
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

type liveReasoningTrace struct {
	reasoningChunks   []string
	answerChunks      []string
	finalRawReasoning string
	finalRawText      string
	finalEventText    string
	finalPayloadText  string
	firstOverlapLayer string
}

func (t *liveReasoningTrace) capture(env eventstream.Envelope) {
	if t == nil || env.Kind != eventstream.KindSessionUpdate {
		return
	}
	chunk, ok := env.Update.(schema.ContentChunk)
	if !ok {
		return
	}
	text := strings.TrimSpace(schema.ExtractTextValue(chunk.Content))
	switch chunk.SessionUpdate {
	case schema.UpdateAgentThought:
		t.reasoningChunks = append(t.reasoningChunks, text)
		if env.Final {
			t.finalRawReasoning = text
		}
	case schema.UpdateAgentMessage:
		t.answerChunks = append(t.answerChunks, text)
		if env.Final {
			t.finalRawText = text
			t.finalEventText = text
			t.finalPayloadText = text
		}
	}
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
