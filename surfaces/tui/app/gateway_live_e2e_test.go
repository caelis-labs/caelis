//go:build e2e

package tuiapp

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
	"github.com/OnslaughtSnail/caelis/kernel"
)

func TestTUILiveGatewayReasoningBoundaryFromLocalConfigE2E(t *testing.T) {
	if strings.TrimSpace(os.Getenv("CAELIS_LIVE_E2E")) == "" {
		t.Skip("set CAELIS_LIVE_E2E=1 to run local-config live gateway/tui e2e")
	}
	modelCfg, ok := loadLocalGatewayModelConfigForTUI(t, "minimax")
	if !ok {
		t.Skip("no local minimax gateway config found")
	}
	modelCfg.ReasoningEffort = "high"

	stack, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "tui-live-local-config-e2e",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   "tui-live-local-config-e2e",
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Model:          modelCfg,
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	session, err := stack.StartSession(context.Background(), "tui-live-local-config-e2e", "")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	turn, err := stack.Gateway.BeginTurn(ctx, kernel.BeginTurnRequest{
		SessionRef: session.SessionRef,
		Input:      "介绍一下你自己。",
		Surface:    "cli-tui",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	defer turn.Handle.Close()

	model := newGatewayEventTestModel()
	var reasoningChunks []string
	var answerChunks []string
	for env := range turn.Handle.Events() {
		if env.Err != nil {
			t.Fatalf("turn event error = %v", env.Err)
		}
		if payload := env.Event.Narrative; payload != nil && payload.Role == kernel.NarrativeRoleAssistant && payload.Visibility == "ui_only" {
			switch payload.UpdateType {
			case "agent_thought":
				reasoningChunks = append(reasoningChunks, strings.TrimSpace(payload.ReasoningText))
			case "agent_message":
				answerChunks = append(answerChunks, strings.TrimSpace(payload.Text))
			}
		}
		updated, _ := model.Update(env)
		model = updated.(*Model)
	}

	blocks := model.doc.Blocks()
	if got := len(blocks); got == 0 {
		t.Fatal("expected at least one rendered block")
	}
	var block *MainACPTurnBlock
	for _, candidate := range blocks {
		if typed, ok := candidate.(*MainACPTurnBlock); ok {
			block = typed
			break
		}
	}
	if block == nil {
		t.Fatalf("doc blocks = %#v, want MainACPTurnBlock", blocks)
	}

	var (
		finalReasoning string
		finalAnswer    string
	)
	for _, ev := range block.Events {
		switch ev.Kind {
		case SEReasoning:
			if text := strings.TrimSpace(ev.Text); text != "" {
				finalReasoning = text
			}
		case SEAssistant:
			if text := strings.TrimSpace(ev.Text); text != "" {
				finalAnswer = text
			}
		}
	}
	if finalReasoning == "" {
		t.Fatalf("expected reasoning event in final TUI block; reasoning_chunks=%q answer_chunks=%q events=%#v", reasoningChunks, answerChunks, block.Events)
	}
	if finalAnswer == "" {
		t.Fatalf("expected assistant event in final TUI block; reasoning_chunks=%q answer_chunks=%q events=%#v", reasoningChunks, answerChunks, block.Events)
	}
	if strings.HasPrefix(finalAnswer, finalReasoning) {
		t.Fatalf("TUI final assistant overlaps reasoning prefix; reasoning_chunks=%q answer_chunks=%q reasoning=%q answer=%q events=%#v", reasoningChunks, answerChunks, finalReasoning, finalAnswer, block.Events)
	}

	rows := block.Render(BlockRenderContext{
		Width:     100,
		TermWidth: 100,
		Theme:     model.theme,
	})
	var (
		plain           []string
		reasoningPrefix int
		answerPrefix    int
	)
	for _, row := range rows {
		plain = append(plain, row.Plain)
		if strings.HasPrefix(row.Plain, "· ") {
			reasoningPrefix++
		}
		if strings.HasPrefix(row.Plain, "* ") {
			answerPrefix++
		}
	}
	rendered := strings.Join(plain, "\n")
	if reasoningPrefix != 1 {
		t.Fatalf("rendered rows contain %d reasoning prefixes, want 1; reasoning_chunks=%q answer_chunks=%q rows=%q", reasoningPrefix, reasoningChunks, answerChunks, rendered)
	}
	if answerPrefix != 1 {
		t.Fatalf("rendered rows contain %d answer prefixes, want 1; reasoning_chunks=%q answer_chunks=%q rows=%q", answerPrefix, reasoningChunks, answerChunks, rendered)
	}
}

func TestTUILiveGatewayReasoningBoundaryTraceFromLocalConfig(t *testing.T) {
	if testing.Short() || strings.TrimSpace(os.Getenv("CAELIS_TRACE_LIVE_REASONING")) == "" {
		t.Skip("set CAELIS_TRACE_LIVE_REASONING=1 to run live trace diagnosis")
	}
	modelCfg, ok := loadLocalGatewayModelConfigForTUI(t, "minimax")
	if !ok {
		t.Skip("no local minimax gateway config found")
	}
	modelCfg.ReasoningEffort = "high"

	stack, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "tui-live-trace-local-config",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   "tui-live-trace-local-config",
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Model:          modelCfg,
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	session, err := stack.StartSession(context.Background(), "tui-live-trace-local-config", "")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	turn, err := stack.Gateway.BeginTurn(ctx, kernel.BeginTurnRequest{
		SessionRef: session.SessionRef,
		Input:      "介绍一下你自己。",
		Surface:    "cli-tui",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	defer turn.Handle.Close()

	model := newGatewayEventTestModel()
	step := 0
	for env := range turn.Handle.Events() {
		if env.Err != nil {
			t.Fatalf("turn event error = %v", env.Err)
		}
		if env.Event.Kind == kernel.EventKindAssistantMessage && env.Event.Narrative != nil {
			payload := env.Event.Narrative
			t.Logf("assistant event[%d]: final=%v reasoning=%q text=%q", step, payload.Final, payload.ReasoningText, payload.Text)
		}
		updated, _ := model.Update(env)
		model = updated.(*Model)
		if block := firstMainACPTurnBlock(model); block != nil {
			t.Logf("block state[%d]: %s", step, summarizeNarrativeEvents(block.Events))
		}
		step++
	}
}

func loadLocalGatewayModelConfigForTUI(t *testing.T, provider string) (gatewayapp.ModelConfig, bool) {
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

func firstMainACPTurnBlock(model *Model) *MainACPTurnBlock {
	if model == nil {
		return nil
	}
	for _, candidate := range model.doc.Blocks() {
		if block, ok := candidate.(*MainACPTurnBlock); ok {
			return block
		}
	}
	return nil
}

func summarizeNarrativeEvents(events []SubagentEvent) string {
	if len(events) == 0 {
		return "<empty>"
	}
	parts := make([]string, 0, len(events))
	for _, ev := range events {
		switch ev.Kind {
		case SEReasoning:
			parts = append(parts, "reasoning="+strconv.Quote(strings.TrimSpace(ev.Text)))
		case SEAssistant:
			parts = append(parts, "answer="+strconv.Quote(strings.TrimSpace(ev.Text)))
		}
	}
	if len(parts) == 0 {
		return "<no-narrative>"
	}
	return strings.Join(parts, " | ")
}
