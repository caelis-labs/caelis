//go:build e2e

package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/agent/local/chat"
	"github.com/OnslaughtSnail/caelis/impl/model/providers/e2etest"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/host"
	sessionfile "github.com/OnslaughtSnail/caelis/impl/session/file"
	taskfile "github.com/OnslaughtSnail/caelis/impl/task/file"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/filesystem"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/plan"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/shell"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/spawn"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/assembly"
	"github.com/OnslaughtSnail/caelis/ports/compact"
	"github.com/OnslaughtSnail/caelis/ports/delegation"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

type promptRecord struct {
	Instructions  string
	Messages      []string
	MessageTokens int
}

const (
	providerCompactionE2EMaxTokens           = 256
	providerCompactionShortRequestTimeout    = 120 * time.Second
	providerCompactionLongRequestTimeout     = 180 * time.Second
	providerCompactionShortContextTimeout    = 300 * time.Second
	providerCompactionLongContextTimeout     = 420 * time.Second
	providerCompactionVeryLongContextTimeout = 480 * time.Second
)

func testACPAssembly(configs ...assembly.AgentConfig) (assembly.ResolvedAssembly, []delegation.Agent) {
	assembly := assembly.ResolvedAssembly{
		Agents: make([]assembly.AgentConfig, 0, len(configs)),
	}
	agents := make([]delegation.Agent, 0, len(configs))
	for _, one := range configs {
		cfg := assembly.CloneAgentConfig(one)
		assembly.Agents = append(assembly.Agents, cfg)
		agents = append(agents, delegation.Agent{
			Name:        strings.TrimSpace(cfg.Name),
			Description: strings.TrimSpace(cfg.Description),
		})
	}
	return assembly, agents
}

func TestRuntimeProviderE2E(t *testing.T) {
	spec := e2etest.RequireLLM(t, e2etest.Config{
		DefaultProvider: "minimax",
		DefaultModel:    "MiniMax-M2",
		Timeout:         90 * time.Second,
		MaxTokens:       512,
	})

	root := t.TempDir()
	sessions := newFileSessionService(root, "sess-runtime-provider")
	session, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-e2e",
			CWD: "/tmp/caelis-sdk-runtime",
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Answer tersely.",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	result, err := runtime.Run(ctx, agent.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "Reply with exactly: runtime provider e2e ok",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: spec.LLM,
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var finalText string
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			t.Fatalf("runner error = %v", seqErr)
		}
		if event != nil && event.Type == session.EventTypeAssistant {
			finalText = strings.TrimSpace(event.Text)
		}
	}
	if finalText == "" {
		t.Fatal("expected non-empty assistant text")
	}

	loaded, err := sessions.LoadSession(ctx, session.LoadSessionRequest{
		SessionRef: session.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if got, want := len(loaded.Events), 2; got != want {
		t.Fatalf("len(loaded.Events) = %d, want %d", got, want)
	}

	doc := readPersistedSessionDocument(t, root, session.SessionID)
	assertPersistedDocumentShape(t, doc, session.SessionID)
	if got := len(documentEvents(doc)); got != 2 {
		t.Fatalf("persisted event count = %d, want %d", got, 2)
	}
}

func TestRuntimeProviderLiveTurnE2E(t *testing.T) {
	spec := e2etest.RequireLLM(t, e2etest.Config{
		DefaultProvider: "minimax",
		DefaultModel:    "MiniMax-M2",
		Timeout:         90 * time.Second,
		MaxTokens:       512,
	})

	root := t.TempDir()
	sessions := newFileSessionService(root, "sess-runtime-live-provider")
	session, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-live-e2e",
			CWD: "/tmp/caelis-sdk-runtime-live",
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Answer tersely.",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	start := time.Now()
	result, err := runtime.Run(ctx, agent.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "Reply with exactly: runtime live e2e ok",
		Request: agent.ModelRequestOptions{
			Stream: boolPtrForE2E(true),
		},
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: spec.LLM,
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Run() blocked for %s, want live handle return under 1s", elapsed)
	}

	var (
		sawUser      bool
		sawChunk     bool
		finalText    string
		firstEventAt time.Time
	)
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			t.Fatalf("runner error = %v", seqErr)
		}
		if event == nil {
			continue
		}
		if firstEventAt.IsZero() {
			firstEventAt = time.Now()
		}
		switch {
		case event.Type == session.EventTypeUser:
			sawUser = true
		case event.Type == session.EventTypeAssistant && event.Visibility == session.VisibilityUIOnly && event.Protocol != nil &&
			(event.Protocol.UpdateType == string(session.ProtocolUpdateTypeAgentMessage) ||
				event.Protocol.UpdateType == string(session.ProtocolUpdateTypeAgentThought)):
			sawChunk = true
		case event.Type == session.EventTypeAssistant && event.Visibility == session.VisibilityCanonical:
			finalText = strings.TrimSpace(event.Text)
		}
	}
	if firstEventAt.IsZero() {
		t.Fatal("expected at least one live event")
	}
	if delay := firstEventAt.Sub(start); delay > 2*time.Second {
		t.Fatalf("first event arrived after %s, want live publication under 2s", delay)
	}
	if !sawUser {
		t.Fatal("expected live user event before completion")
	}
	if !sawChunk {
		t.Fatal("expected ACP-compatible assistant chunk/thought event before final response")
	}
	if finalText == "" {
		t.Fatal("expected final canonical assistant text")
	}

	loaded, err := sessions.LoadSession(ctx, session.LoadSessionRequest{
		SessionRef: session.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	for _, event := range loaded.Events {
		if event == nil || event.Visibility != session.VisibilityUIOnly {
			continue
		}
		t.Fatalf("persisted history unexpectedly contains UI-only chunk event: %+v", event)
	}
}

func TestRuntimeProviderToolLoopE2E(t *testing.T) {
	spec := e2etest.RequireLLM(t, e2etest.Config{
		DefaultProvider: "minimax",
		DefaultModel:    "MiniMax-M2",
		Timeout:         90 * time.Second,
		MaxTokens:       768,
	})

	dir := t.TempDir()
	targetFile := filepath.Join(dir, "facts.txt")
	if err := os.WriteFile(targetFile, []byte("runtime minimax tool loop ok\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	sandboxRuntime, err := host.New(host.Config{CWD: dir})
	if err != nil {
		t.Fatalf("host.New() error = %v", err)
	}
	readTool, err := filesystem.NewRead(filesystem.DefaultReadConfig(), sandboxRuntime)
	if err != nil {
		t.Fatalf("filesystem.NewRead() error = %v", err)
	}

	root := t.TempDir()
	sessions := newFileSessionService(root, "sess-runtime-tool")
	session, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: dir,
			CWD: dir,
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Use tools when the user asks for exact file-derived information. Do not guess.",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	result, err := runtime.Run(ctx, agent.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "You must use the READ tool on " + targetFile + ". Then reply with exactly the value of the content field from the tool result and nothing else.",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: spec.LLM,
			Tools: []tool.Tool{readTool},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var (
		finalText  string
		toolUsed   bool
		toolCallCt int
		protocolOK bool
	)
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			t.Fatalf("runner error = %v", seqErr)
		}
		if event == nil {
			continue
		}
		if event.Type == session.EventTypeToolCall {
			toolCallCt++
		}
		if event.Type == session.EventTypeToolResult {
			toolUsed = true
		}
		if event.Protocol != nil && (event.Protocol.UpdateType == string(session.ProtocolUpdateTypeToolCall) || event.Protocol.UpdateType == string(session.ProtocolUpdateTypeToolUpdate)) {
			protocolOK = true
		}
		if event.Type == session.EventTypeAssistant {
			finalText = strings.TrimSpace(event.Text)
		}
	}
	if toolCallCt == 0 {
		t.Fatal("expected at least one tool call event")
	}
	if !toolUsed {
		t.Fatal("expected at least one tool result event")
	}
	if !protocolOK {
		t.Fatal("expected ACP-compatible protocol payloads on tool loop events")
	}
	if finalText != "1: runtime minimax tool loop ok" && finalText != "runtime minimax tool loop ok" {
		t.Fatalf("final assistant text = %q, want one of expected tool-derived answers", finalText)
	}

	loaded, err := sessions.LoadSession(ctx, session.LoadSessionRequest{
		SessionRef: session.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if len(loaded.Events) < 4 {
		t.Fatalf("len(loaded.Events) = %d, want >= 4", len(loaded.Events))
	}

	doc := readPersistedSessionDocument(t, root, session.SessionID)
	assertPersistedDocumentShape(t, doc, session.SessionID)
	if got := len(documentEvents(doc)); got < 4 {
		t.Fatalf("persisted event count = %d, want >= 4", got)
	}
}

func TestRuntimeProviderPlanLoopE2E(t *testing.T) {
	spec := e2etest.RequireLLM(t, e2etest.Config{
		DefaultProvider: "minimax",
		DefaultModel:    "MiniMax-M2",
		Timeout:         90 * time.Second,
		MaxTokens:       768,
	})

	root := t.TempDir()
	sessions := newFileSessionService(root, "sess-runtime-plan")
	session, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-plan",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "When the user asks for a plan, you must call the PLAN tool before answering. Keep answers terse.",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	result, err := runtime.Run(ctx, agent.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "Create a short 2-step plan to verify the runtime plan bridge, call the PLAN tool with that full plan, then reply with exactly: plan loop ok",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: spec.LLM,
			Tools: []tool.Tool{plan.New()},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var (
		finalText string
		sawPlan   bool
	)
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			t.Fatalf("runner error = %v", seqErr)
		}
		if event == nil {
			continue
		}
		if event.Type == session.EventTypePlan && event.Protocol != nil && event.Protocol.Plan != nil {
			sawPlan = true
		}
		if event.Type == session.EventTypeAssistant {
			finalText = strings.TrimSpace(event.Text)
		}
	}
	if !sawPlan {
		t.Fatal("expected plan event")
	}
	if finalText != "plan loop ok" {
		t.Fatalf("final assistant text = %q, want %q", finalText, "plan loop ok")
	}

	state, err := sessions.SnapshotState(ctx, session.SessionRef)
	if err != nil {
		t.Fatalf("SnapshotState() error = %v", err)
	}
	if _, ok := state["plan"]; !ok {
		t.Fatalf("state[plan] missing: %#v", state)
	}

	doc := readPersistedSessionDocument(t, root, session.SessionID)
	assertPersistedDocumentShape(t, doc, session.SessionID)
	documentState, ok := doc["state"].(map[string]any)
	if !ok {
		t.Fatalf("persisted state = %#v, want object", doc["state"])
	}
	if _, ok := documentState["plan"]; !ok {
		t.Fatalf("persisted state[plan] missing: %#v", documentState)
	}
}

func TestRuntimeProviderCompactionContinuityE2E(t *testing.T) {
	spec := e2etest.RequireLLM(t, e2etest.Config{
		DefaultProvider: "minimax",
		DefaultModel:    "MiniMax-M2",
		Timeout:         providerCompactionShortRequestTimeout,
		MaxTokens:       providerCompactionE2EMaxTokens,
	})
	wrapped := &recordingLLM{base: spec.LLM}

	root := t.TempDir()
	sessions := newFileSessionService(root, "sess-runtime-compact-continuity")
	session, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-compact",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Answer tersely. When the user asks to restate session facts, preserve the key phrases faithfully.",
		},
		Compaction: CompactionConfig{
			Enabled:                    true,
			WatermarkRatio:             0.7,
			ForceWatermarkRatio:        0.85,
			DefaultContextWindowTokens: 180,
			ReserveOutputTokens:        64,
			SafetyMarginTokens:         16,
			SegmentTokenBudget:         160,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), providerCompactionShortContextTimeout)
	defer cancel()

	turns := []string{
		"Session objective is: build compact runtime continuity. Current blocker is: provider intermittently returns 529 overloaded_error under long context. Next action is: validate with real e2e tests and tune prompt shape. Files touched include impl/agent/local/runtime.go and impl/agent/local/compaction.go. Keep all three items exact across compaction. Additional continuity note 1. Additional continuity note 2. Additional continuity note 3. Additional continuity note 4. Additional continuity note 5. Reply exactly: ack-1",
		"Keep preserving the exact same objective, blocker, and next action across compact. Add another continuity reminder about checkpoint durability, replay safety, provider variance, prompt repair, and regression coverage. Additional continuity note 6. Additional continuity note 7. Additional continuity note 8. Additional continuity note 9. Additional continuity note 10. Reply exactly: ack-2",
		"Restate nothing yet, just keep the continuity anchors stable while the session grows. Mention impl/agent/local/compaction.go, impl/agent/local/e2e_test.go, impl/agent/local/compaction.go, and ports/compact/compact.go as touched areas. Additional continuity note 11. Additional continuity note 12. Additional continuity note 13. Additional continuity note 14. Additional continuity note 15. Reply exactly: ack-3",
	}
	for _, input := range turns {
		if _, err := runtime.Run(ctx, agent.RunRequest{
			SessionRef: session.SessionRef,
			Input:      input,
			AgentSpec: agent.AgentSpec{
				Name:  "chat",
				Model: wrapped,
			},
		}); err != nil {
			t.Fatalf("seed Run(%q) error = %v", input, err)
		}
	}

	finalQuery := "Reply in one line with the exact objective, blocker, and next action from this session. Do not infer, rewrite, or improve the next action; repeat the exact phrase that was stated earlier."
	result, err := runtime.Run(ctx, agent.RunRequest{
		SessionRef: session.SessionRef,
		Input:      finalQuery,
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: wrapped,
		},
	})
	if err != nil {
		t.Fatalf("final Run() error = %v", err)
	}
	var finalText string
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			t.Fatalf("runner error = %v", seqErr)
		}
		if event != nil && event.Type == session.EventTypeAssistant {
			finalText = strings.TrimSpace(event.Text)
		}
	}
	finalTextLower := strings.ToLower(finalText)
	for _, needle := range []string{
		"build compact runtime continuity",
		"529 overloaded_error",
		"validate with real e2e tests and tune prompt shape",
	} {
		if !strings.Contains(finalTextLower, strings.ToLower(needle)) {
			t.Fatalf("finalText missing %q: %q\nlastNormalMessages=%v", needle, finalText, wrapped.lastNormalMessages)
		}
	}
	if wrapped.compactionCalls == 0 {
		t.Fatal("expected at least one model-backed compaction call")
	}
	if !containsMessageForE2E(wrapped.lastNormalMessages, "CONTEXT CHECKPOINT") {
		t.Fatalf("last normal messages missing compact checkpoint: %v", wrapped.lastNormalMessages)
	}
	if containsMessageForE2E(wrapped.lastNormalMessages, "Session objective is: build compact runtime continuity. Current blocker is: provider intermittently returns 529 overloaded_error under long context. Next action is: validate with real e2e tests and tune prompt shape. Files touched include impl/agent/local/runtime.go and impl/agent/local/compaction.go. Keep all three items exact across compaction. Additional continuity note 1. Additional continuity note 2. Additional continuity note 3. Additional continuity note 4. Additional continuity note 5. Reply exactly: ack-1") {
		t.Fatalf("last normal messages still contain raw pre-compact objective turn: %v", wrapped.lastNormalMessages)
	}

	loaded, err := sessions.LoadSession(ctx, session.LoadSessionRequest{
		SessionRef: session.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	sawCompact := false
	var compactText string
	for _, event := range loaded.Events {
		if event != nil && event.Type == session.EventTypeCompact {
			sawCompact = true
			compactText = strings.TrimSpace(event.Text)
			break
		}
	}
	if !sawCompact {
		t.Fatal("expected persisted compact event")
	}
	if !strings.Contains(compactText, "build compact runtime continuity") {
		t.Fatalf("compact event text missing objective: %q", compactText)
	}
	compactEvent, ok := latestCompactEventForTest(loaded.Events)
	if !ok {
		t.Fatal("expected compact event for checkpoint overlay assertions")
	}
	data, ok := compact.CompactEventDataFromEvent(compactEvent)
	if !ok {
		t.Fatalf("compact event meta missing compact payload: %+v", compactEvent.Meta)
	}
	if data.SourceEventCount == 0 {
		t.Fatalf("compact source event count = %d, want > 0", data.SourceEventCount)
	}
	promptEvents := compact.PromptEventsFromLatestCompact(loaded.Events)
	if len(promptEvents) == 0 || !strings.Contains(strings.ToLower(session.EventText(promptEvents[0])), "build compact runtime continuity") {
		t.Fatalf("prompt events missing continuity objective in checkpoint overlay: %+v", promptEvents)
	}
	rawHistoryTokens := 0
	for _, input := range turns {
		rawHistoryTokens += estimateTextTokens(input)
	}
	compactTokens := estimateTextTokens(compactText)
	if compactTokens >= rawHistoryTokens {
		t.Fatalf("compactTokens = %d, want < rawHistoryTokens = %d", compactTokens, rawHistoryTokens)
	}
	finalPromptTokens := 0
	for _, text := range wrapped.lastNormalMessages {
		finalPromptTokens += estimateTextTokens(text)
	}
	if finalPromptTokens >= rawHistoryTokens+estimateTextTokens(finalQuery) {
		t.Fatalf("finalPromptTokens = %d, want compacted prompt < raw replay = %d (messages=%v)", finalPromptTokens, rawHistoryTokens+estimateTextTokens(finalQuery), wrapped.lastNormalMessages)
	}
	state, err := sessions.SnapshotState(ctx, session.SessionRef)
	if err != nil {
		t.Fatalf("SnapshotState() error = %v", err)
	}
	if len(state) != 0 {
		t.Fatalf("session state = %v, want compaction continuity to rely on append-only events", state)
	}
}

func TestRuntimeProviderCompactionPlanContinuityE2E(t *testing.T) {
	spec := e2etest.RequireLLM(t, e2etest.Config{
		DefaultProvider: "minimax",
		DefaultModel:    "MiniMax-M2",
		Timeout:         providerCompactionShortRequestTimeout,
		MaxTokens:       providerCompactionE2EMaxTokens,
	})
	wrapped := &recordingLLM{base: spec.LLM}

	root := t.TempDir()
	sessions := newFileSessionService(root, "sess-runtime-compact-plan")
	session, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-compact-plan",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "When asked for a plan, you must call the PLAN tool before answering. When asked which plan step is currently in_progress, reply with that exact step content only and nothing else. Keep answers terse.",
		},
		Compaction: CompactionConfig{
			Enabled:                    true,
			WatermarkRatio:             0.7,
			ForceWatermarkRatio:        0.85,
			DefaultContextWindowTokens: 256,
			ReserveOutputTokens:        64,
			SafetyMarginTokens:         16,
			SegmentTokenBudget:         160,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), providerCompactionShortContextTimeout)
	defer cancel()

	if _, err := runtime.Run(ctx, agent.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "Create a short 2-step plan with exactly these entries: 1. Inspect repo [completed], 2. Validate compact e2e [in_progress]. Call PLAN with that plan and then reply exactly: plan seed ok",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: wrapped,
			Tools: []tool.Tool{plan.New()},
		},
	}); err != nil {
		t.Fatalf("plan seed Run() error = %v", err)
	}

	for _, input := range []string{
		"Remember that the plan should stay visible across compaction. Reply exactly: ack-a",
		"Remember that plan continuity matters more than raw transcript replay. Reply exactly: ack-b",
		"Remember that compact runtime state should retain in-progress steps. Reply exactly: ack-c",
		"Remember that this should survive file-backed resume as well. Reply exactly: ack-d",
	} {
		if _, err := runtime.Run(ctx, agent.RunRequest{
			SessionRef: session.SessionRef,
			Input:      input,
			AgentSpec: agent.AgentSpec{
				Name:  "chat",
				Model: wrapped,
			},
		}); err != nil {
			t.Fatalf("seed Run(%q) error = %v", input, err)
		}
	}

	finalText := runAndCollectAssistantTextForE2E(ctx, t, runtime, agent.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "From the current session plan, what is the exact step currently marked in_progress? Reply with the step content only and nothing else.",
		AgentSpec: agent.AgentSpec{
			Name: "chat",
			Model: &contextProbeModel{
				t: t,
				wantMessageContains: []string{
					"CONTEXT CHECKPOINT",
					"Validate compact e2e",
				},
				replyText: "Validate compact e2e",
			},
		},
	})
	if !strings.Contains(strings.ToLower(finalText), "validate compact e2e") {
		t.Fatalf("finalText = %q, want in-progress plan step", finalText)
	}
	if wrapped.compactionCalls == 0 {
		t.Fatal("expected at least one model-backed compaction call")
	}
	if !containsMessageForE2E(wrapped.lastNormalMessages, "CONTEXT CHECKPOINT") {
		t.Fatalf("last normal messages missing compact checkpoint: %v", wrapped.lastNormalMessages)
	}
}

func TestRuntimeProviderCompactionMultiCompactLongTaskE2E(t *testing.T) {
	spec := e2etest.RequireLLM(t, e2etest.Config{
		DefaultProvider: "minimax",
		DefaultModel:    "MiniMax-M2",
		Timeout:         providerCompactionLongRequestTimeout,
		MaxTokens:       providerCompactionE2EMaxTokens,
	})
	wrapped := &recordingLLM{base: spec.LLM}

	root := t.TempDir()
	sessions := newFileSessionService(root, "sess-runtime-compact-multi")
	session, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-compact-multi",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	compactionCfg := CompactionConfig{
		Enabled:                    true,
		WatermarkRatio:             0.6,
		ForceWatermarkRatio:        0.75,
		DefaultContextWindowTokens: 320,
		ReserveOutputTokens:        64,
		SafetyMarginTokens:         16,
		SegmentTokenBudget:         140,
	}
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Answer tersely. For ordinary status updates that do not ask a direct question, respond with a short acknowledgment. When asked to restate session facts, preserve the exact objective, blocker, next action, and latest completed milestone marker.",
		},
		Compaction: compactionCfg,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), providerCompactionVeryLongContextTimeout)
	defer cancel()

	appendCanonicalDialogueForE2E(t, sessions, session.SessionRef, []string{
		"Objective: finish sdk compact hardening. Blocker: remaining real-provider compact hardening coverage is incomplete. Next action: add multi-compaction, segmented retry, budget, compression, and prefix-stability e2e. Latest completed milestone: milestone-1.",
		"Latest completed milestone: milestone-2. Keep the same exact objective, blocker, and next action. Note beta: prompt trimming, replay safety, and regression coverage.",
		"Latest completed milestone: milestone-3. Keep the same exact objective, blocker, and next action. Note gamma: checkpoint overlay, budget tracking, and compact quality.",
		"Latest completed milestone: milestone-4. Keep the same exact objective, blocker, and next action. Note delta: segmented retry, overflow handling, and continuity anchors.",
		"Keep the same exact objective, blocker, and next action. Note epsilon: prompt-budget assertions, compact trigger reliability, and append-only replay.",
		"Keep the same exact objective, blocker, and next action. Note zeta: compact checkpoint density, replay safety, and long-task continuity.",
	})
	_ = runAndCollectAssistantTextForE2E(ctx, t, runtime, agent.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "Acknowledge the current compact-hardening session in one short line.",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: wrapped,
		},
	})
	appendCanonicalDialogueForE2E(t, sessions, session.SessionRef, []string{
		"Latest completed milestone: milestone-5. Keep the same exact objective, blocker, and next action. Note eta: compression ratio, prompt-prefix stability, and resume safety.",
		"Latest completed milestone: milestone-6. Keep the same exact objective, blocker, and next action. Note theta: token-budget enforcement and compacted prompt density.",
		"Keep the same exact objective, blocker, and next action. Note iota: stale-detail removal and long-task execution progress.",
		"Keep the same exact objective, blocker, and next action. Note kappa: append-only replay and checkpoint durability.",
	})

	finalQuery := "Reply in one line with the exact objective, blocker, next action, and latest completed milestone from this session."
	finalText := runAndCollectAssistantTextForE2E(ctx, t, runtime, agent.RunRequest{
		SessionRef: session.SessionRef,
		Input:      finalQuery,
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: wrapped,
		},
	})
	for _, needle := range []string{
		"finish sdk compact hardening",
		"remaining real-provider compact hardening coverage is incomplete",
		"add multi-compaction, segmented retry, budget, compression, and prefix-stability e2e",
		"milestone-6",
	} {
		if !strings.Contains(strings.ToLower(finalText), strings.ToLower(needle)) {
			t.Fatalf("finalText missing %q: %q", needle, finalText)
		}
	}

	snapshot := wrapped.snapshot()
	if snapshot.CompactionCalls < 2 {
		t.Fatalf("compactionCalls = %d, want >= 2", snapshot.CompactionCalls)
	}
	if !containsMessageForE2E(snapshot.LastNormalMessages, "CONTEXT CHECKPOINT") {
		t.Fatalf("last normal messages missing compact checkpoint: %v", snapshot.LastNormalMessages)
	}

	loaded, err := sessions.LoadSession(ctx, session.LoadSessionRequest{
		SessionRef: session.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if compactCount := countCompactEvents(loaded.Events); compactCount < 2 {
		t.Fatalf("compact event count = %d, want >= 2", compactCount)
	}
	latestCompact, ok := latestCompactEventForTest(loaded.Events)
	if !ok {
		t.Fatal("expected latest compact event")
	}
	rawReplayTokens := 0
	for _, event := range loaded.Events {
		if event == nil || event.Type == session.EventTypeCompact {
			continue
		}
		rawReplayTokens += estimatePromptEventTokens(event)
	}
	compactTokens := estimateTextTokens(compactTextFromEvent(latestCompact))
	if compactTokens >= rawReplayTokens {
		t.Fatalf("compactTokens = %d, want < rawReplayTokens = %d", compactTokens, rawReplayTokens)
	}
	effectiveBudget := resolveEffectiveInputBudget(
		compactionCfg.DefaultContextWindowTokens,
		resolveReserveOutputTokens(compactionCfg.DefaultContextWindowTokens, compactionCfg.ReserveOutputTokens),
		resolveSafetyMarginTokens(compactionCfg.DefaultContextWindowTokens, compactionCfg.SafetyMarginTokens),
	)
	if snapshot.LastNormalTokenCount > effectiveBudget {
		t.Fatalf("last normal prompt tokens = %d, want <= effective budget %d", snapshot.LastNormalTokenCount, effectiveBudget)
	}
	rawPromptTokens := rawReplayTokens + estimateTextTokens(finalQuery)
	if snapshot.LastNormalTokenCount >= rawPromptTokens {
		t.Fatalf("compacted prompt tokens = %d, want < raw prompt tokens = %d", snapshot.LastNormalTokenCount, rawPromptTokens)
	}
	t.Logf("multi-compact compression raw_prompt_tokens=%d compacted_prompt_tokens=%d latest_compact_tokens=%d ratio=%.3f",
		rawPromptTokens,
		snapshot.LastNormalTokenCount,
		compactTokens,
		float64(snapshot.LastNormalTokenCount)/float64(rawPromptTokens),
	)
}

func TestRuntimeProviderCompactionSegmentedRetryE2E(t *testing.T) {
	spec := e2etest.RequireLLM(t, e2etest.Config{
		DefaultProvider: "minimax",
		DefaultModel:    "MiniMax-M2",
		Timeout:         providerCompactionLongRequestTimeout,
		MaxTokens:       providerCompactionE2EMaxTokens,
	})
	wrapped := &recordingLLM{
		base:                         spec.LLM,
		forceCompactionOverflows:     1,
		forceCompactionOverflowFloor: 80,
	}

	root := t.TempDir()
	sessions := newFileSessionService(root, "sess-runtime-compact-segmented")
	session, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-compact-segmented",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Answer tersely. For ordinary status updates that do not ask a direct question, respond with a short acknowledgment. Preserve exact objective, blocker, and next action when asked to restate them.",
		},
		Compaction: CompactionConfig{
			Enabled:                    true,
			WatermarkRatio:             0.65,
			ForceWatermarkRatio:        0.8,
			DefaultContextWindowTokens: 220,
			ReserveOutputTokens:        64,
			SafetyMarginTokens:         16,
			SegmentTokenBudget:         90,
			MaxSegmentDepth:            8,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), providerCompactionLongContextTimeout)
	defer cancel()

	appendCanonicalDialogueForE2E(t, sessions, session.SessionRef, []string{
		"Objective: validate segmented compact retry. Blocker: the first compact request should overflow and then be retried in smaller segments. Next action: prove segmented retry still preserves exact continuity anchors.",
		"Keep the same exact objective, blocker, and next action. Note beta: append-only replay and budget pressure.",
		"Keep the same exact objective, blocker, and next action. Note gamma: checkpoint continuity and compact quality.",
		"Keep the same exact objective, blocker, and next action. Note delta: overflow handling and retry segmentation.",
		"Keep the same exact objective, blocker, and next action. Note epsilon: segment folding and retry depth.",
		"Keep the same exact objective, blocker, and next action. Note zeta: provider-backed compaction and retry safety.",
		"Keep the same exact objective, blocker, and next action. Note eta: exact-anchor preservation during smaller segment retries.",
	})

	finalText := runAndCollectAssistantTextForE2E(ctx, t, runtime, agent.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "Reply in one line with the exact objective, blocker, and next action from this session.",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: wrapped,
		},
	})
	for _, needle := range []string{
		"validate segmented compact retry",
		"smaller segments",
		"exact continuity anchors",
	} {
		if !strings.Contains(strings.ToLower(finalText), strings.ToLower(needle)) {
			t.Fatalf("finalText missing %q: %q", needle, finalText)
		}
	}

	snapshot := wrapped.snapshot()
	if snapshot.InjectedOverflows == 0 {
		t.Fatalf("expected at least one injected compaction overflow; compaction records=%+v", snapshot.CompactionRecords)
	}
	if snapshot.CompactionCalls < 2 {
		t.Fatalf("compactionCalls = %d, want >= 2", snapshot.CompactionCalls)
	}
	if len(snapshot.OverflowedCallIndexes) == 0 || len(snapshot.OverflowedCallTokens) == 0 {
		t.Fatalf("expected recorded overflowed compaction call, got snapshot=%+v", snapshot)
	}
	overflowIndex := snapshot.OverflowedCallIndexes[0]
	if overflowIndex >= len(snapshot.CompactionRecords)-1 {
		t.Fatalf("expected follow-up compaction calls after overflowed request, got records=%+v", snapshot.CompactionRecords)
	}
}

func TestRuntimeProviderCompactionPrefixStabilityE2E(t *testing.T) {
	spec := e2etest.RequireLLM(t, e2etest.Config{
		DefaultProvider: "minimax",
		DefaultModel:    "MiniMax-M2",
		Timeout:         providerCompactionLongRequestTimeout,
		MaxTokens:       providerCompactionE2EMaxTokens,
	})
	wrapped := &recordingLLM{base: spec.LLM}

	root := t.TempDir()
	sessions := newFileSessionService(root, "sess-runtime-compact-prefix")
	session, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-compact-prefix",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Answer tersely. For ordinary status updates that do not ask a direct question, respond with a short acknowledgment. Preserve exact continuity anchors when asked to restate session facts.",
		},
		Compaction: CompactionConfig{
			Enabled:                    true,
			WatermarkRatio:             0.7,
			ForceWatermarkRatio:        0.85,
			DefaultContextWindowTokens: 360,
			ReserveOutputTokens:        64,
			SafetyMarginTokens:         16,
			SegmentTokenBudget:         140,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), providerCompactionVeryLongContextTimeout)
	defer cancel()

	appendCanonicalDialogueForE2E(t, sessions, session.SessionRef, []string{
		"Objective: prove prefix stability between compactions. Blocker: prompt prefix drift would reduce cache reuse and continuity confidence. Next action: verify the compact-prefix messages remain byte-stable until the next compact.",
		"Keep the same exact objective, blocker, and next action. Note beta: stable checkpoint overlay and replay determinism.",
		"Keep the same exact objective, blocker, and next action. Note gamma: compacted baselines and low-drift prompt construction.",
		"Keep the same exact objective, blocker, and next action. Note delta: checkpoint continuity inputs and checkpoint continuity.",
		"Keep the same exact objective, blocker, and next action. Note epsilon: stable prefixes and context reuse.",
		"Keep the same exact objective, blocker, and next action. Note zeta: compact baseline stability and prefix cache reuse.",
		"Keep the same exact objective, blocker, and next action. Note eta: deterministic prompt baselines and compact checkpoint reuse.",
		"Keep the same exact objective, blocker, and next action. Note theta: cache-friendly prompt prefixes and continuity confidence.",
	})

	_ = runAndCollectAssistantTextForE2E(ctx, t, runtime, agent.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "Reply exactly: prefix-stable-a",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: wrapped,
		},
	})
	snapshotA := wrapped.snapshot()
	if snapshotA.CompactionCalls == 0 {
		t.Fatal("expected a compact before prefix stability assertions")
	}
	prefixA := compactPrefixForMessages(snapshotA.LastNormalMessages)
	if len(prefixA) == 0 {
		t.Fatalf("prefixA missing compact prefix: %v", snapshotA.LastNormalMessages)
	}
	stableCompactionCount := snapshotA.CompactionCalls
	stablePrefix := prefixA

	_ = runAndCollectAssistantTextForE2E(ctx, t, runtime, agent.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "Reply exactly: prefix-stable-b",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: wrapped,
		},
	})
	snapshotB := wrapped.snapshot()
	if snapshotB.CompactionCalls != stableCompactionCount {
		stableCompactionCount = snapshotB.CompactionCalls
		stablePrefix = compactPrefixForMessages(snapshotB.LastNormalMessages)
		if len(stablePrefix) == 0 {
			t.Fatalf("prefixB missing compact prefix after refresh: %v", snapshotB.LastNormalMessages)
		}
		_ = runAndCollectAssistantTextForE2E(ctx, t, runtime, agent.RunRequest{
			SessionRef: session.SessionRef,
			Input:      "Reply exactly: prefix-stable-c",
			AgentSpec: agent.AgentSpec{
				Name:  "chat",
				Model: wrapped,
			},
		})
		snapshotC := wrapped.snapshot()
		if snapshotC.CompactionCalls != stableCompactionCount {
			t.Fatalf("unexpected extra compact during stability probe: before=%d after=%d", stableCompactionCount, snapshotC.CompactionCalls)
		}
		prefixC := compactPrefixForMessages(snapshotC.LastNormalMessages)
		if !reflect.DeepEqual(stablePrefix, prefixC) {
			t.Fatalf("prompt prefix changed between compactions:\nB=%v\nC=%v", stablePrefix, prefixC)
		}
	} else {
		prefixB := compactPrefixForMessages(snapshotB.LastNormalMessages)
		if !reflect.DeepEqual(stablePrefix, prefixB) {
			t.Fatalf("prompt prefix changed between compactions:\nA=%v\nB=%v", stablePrefix, prefixB)
		}
	}

	appendCanonicalDialogueForE2E(t, sessions, session.SessionRef, []string{
		"Keep the same exact objective, blocker, and next action. Note iota: prefix stability, compact checkpoint density, and provider budget pressure.",
		"Keep the same exact objective, blocker, and next action. Note kappa: append-only replay, cache reuse, and deterministic prompt baselines.",
		"Keep the same exact objective, blocker, and next action. Note lambda: stable prompt prefixes, checkpoint-overlay durability, and post-compact replay growth.",
		"Keep the same exact objective, blocker, and next action. Note mu: fresh transcript growth should eventually force a later compact refresh.",
	})
	_ = runAndCollectAssistantTextForE2E(ctx, t, runtime, agent.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "Reply exactly: prefix-stable-d",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: wrapped,
		},
	})
	snapshotAfterRefresh := wrapped.snapshot()
	if snapshotAfterRefresh.CompactionCalls <= stableCompactionCount {
		t.Fatalf("expected a later compact after the stability interval: before=%d after=%d", stableCompactionCount, snapshotAfterRefresh.CompactionCalls)
	}
}

type recordingLLM struct {
	base                         model.LLM
	contextWindowTokens          int
	compactionOverflowTokenLimit int
	forceCompactionOverflows     int
	forceCompactionOverflowFloor int

	mu                     sync.Mutex
	compactionCalls        int
	normalCalls            int
	injectedOverflows      int
	overflowedCallIndexes  []int
	overflowedCallTokens   []int
	lastNormalInstructions string
	lastNormalMessages     []string
	lastNormalTokenCount   int
	normalRecords          []promptRecord
	compactionRecords      []promptRecord
}

func (m *recordingLLM) Name() string {
	if m == nil || m.base == nil {
		return "recording"
	}
	return m.base.Name()
}

func (m *recordingLLM) ContextWindowTokens() int {
	if m == nil {
		return 0
	}
	return m.contextWindowTokens
}

func (m *recordingLLM) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	if m == nil || m.base == nil {
		return func(yield func(*model.StreamEvent, error) bool) {
			yield(nil, context.Canceled)
		}
	}
	instructions := requestInstructionsTextForE2E(req)
	record := promptRecord{
		Instructions:  instructions,
		Messages:      requestMessageTextsForE2E(req),
		MessageTokens: requestMessageTokenCountForE2E(req),
	}
	if strings.Contains(instructions, "CONTEXT CHECKPOINT COMPACTION") {
		m.mu.Lock()
		m.compactionCalls++
		m.compactionRecords = append(m.compactionRecords, record)
		overflowLimit := m.compactionOverflowTokenLimit
		forceOverflow := m.forceCompactionOverflows > 0 && (m.forceCompactionOverflowFloor <= 0 || record.MessageTokens >= m.forceCompactionOverflowFloor)
		if forceOverflow {
			m.forceCompactionOverflows--
			m.overflowedCallIndexes = append(m.overflowedCallIndexes, len(m.compactionRecords)-1)
			m.overflowedCallTokens = append(m.overflowedCallTokens, record.MessageTokens)
		}
		m.mu.Unlock()
		if forceOverflow || (overflowLimit > 0 && record.MessageTokens > overflowLimit) {
			m.mu.Lock()
			m.injectedOverflows++
			m.mu.Unlock()
			return func(yield func(*model.StreamEvent, error) bool) {
				yield(nil, &model.ContextOverflowError{
					Cause: fmt.Errorf("synthetic compaction overflow at %d tokens (limit=%d)", record.MessageTokens, overflowLimit),
				})
			}
		}
	} else {
		m.mu.Lock()
		m.normalCalls++
		m.lastNormalInstructions = instructions
		m.lastNormalMessages = append([]string(nil), record.Messages...)
		m.lastNormalTokenCount = record.MessageTokens
		m.normalRecords = append(m.normalRecords, record)
		m.mu.Unlock()
	}
	return m.base.Generate(ctx, req)
}

func (m *recordingLLM) snapshot() recordingLLMSnapshot {
	if m == nil {
		return recordingLLMSnapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return recordingLLMSnapshot{
		CompactionCalls:        m.compactionCalls,
		NormalCalls:            m.normalCalls,
		InjectedOverflows:      m.injectedOverflows,
		OverflowedCallIndexes:  append([]int(nil), m.overflowedCallIndexes...),
		OverflowedCallTokens:   append([]int(nil), m.overflowedCallTokens...),
		LastNormalInstructions: m.lastNormalInstructions,
		LastNormalMessages:     append([]string(nil), m.lastNormalMessages...),
		LastNormalTokenCount:   m.lastNormalTokenCount,
		NormalRecords:          clonePromptRecords(m.normalRecords),
		CompactionRecords:      clonePromptRecords(m.compactionRecords),
	}
}

type recordingLLMSnapshot struct {
	CompactionCalls        int
	NormalCalls            int
	InjectedOverflows      int
	OverflowedCallIndexes  []int
	OverflowedCallTokens   []int
	LastNormalInstructions string
	LastNormalMessages     []string
	LastNormalTokenCount   int
	NormalRecords          []promptRecord
	CompactionRecords      []promptRecord
}

type exactReplyLLM struct {
	text string
}

func (m exactReplyLLM) Name() string { return "exact-reply" }

func (m exactReplyLLM) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, strings.TrimSpace(m.text)),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
			},
		}, nil)
	}
}

func requestInstructionsTextForE2E(req *model.Request) string {
	if req == nil {
		return ""
	}
	parts := make([]string, 0, len(req.Instructions))
	for _, part := range req.Instructions {
		if part.Text != nil && strings.TrimSpace(part.Text.Text) != "" {
			parts = append(parts, strings.TrimSpace(part.Text.Text))
		}
	}
	return strings.Join(parts, "\n")
}

func requestMessageTextsForE2E(req *model.Request) []string {
	if req == nil {
		return nil
	}
	out := make([]string, 0, len(req.Messages))
	for _, message := range req.Messages {
		if text := strings.TrimSpace(message.TextContent()); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func requestMessageTokenCountForE2E(req *model.Request) int {
	if req == nil {
		return 0
	}
	total := 0
	for _, message := range req.Messages {
		total += estimateMessageTokens(message)
	}
	return total
}

func clonePromptRecords(in []promptRecord) []promptRecord {
	if len(in) == 0 {
		return nil
	}
	out := make([]promptRecord, 0, len(in))
	for _, item := range in {
		out = append(out, promptRecord{
			Instructions:  item.Instructions,
			Messages:      append([]string(nil), item.Messages...),
			MessageTokens: item.MessageTokens,
		})
	}
	return out
}

func appendCanonicalDialogueForE2E(t *testing.T, sessions session.Service, ref session.SessionRef, turns []string) {
	t.Helper()
	for _, text := range turns {
		appendTestEvent(t, sessions, ref, userTextEvent(text))
		appendTestEvent(t, sessions, ref, assistantEvent("ack"))
	}
}

func countCompactEvents(events []*session.Event) int {
	count := 0
	for _, event := range events {
		if event != nil && event.Type == session.EventTypeCompact {
			count++
		}
	}
	return count
}

func compactPrefixForMessages(messages []string) []string {
	index := -1
	for i, text := range messages {
		if strings.Contains(strings.ToUpper(text), "CONTEXT CHECKPOINT") {
			index = i
		}
	}
	if index < 0 {
		return nil
	}
	return append([]string(nil), messages[:index+1]...)
}

func containsMessageForE2E(messages []string, needle string) bool {
	for _, text := range messages {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func runAndCollectAssistantTextForE2E(ctx context.Context, t *testing.T, runtime *Runtime, req agent.RunRequest) string {
	t.Helper()
	result, err := runtime.Run(ctx, req)
	if err != nil {
		t.Fatalf("Run(%q) error = %v", req.Input, err)
	}
	var finalText string
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			t.Fatalf("runner error = %v", seqErr)
		}
		if event != nil && event.Type == session.EventTypeAssistant {
			finalText = strings.TrimSpace(event.Text)
		}
	}
	return finalText
}

func boolPtrForE2E(v bool) *bool { return &v }

func TestRuntimeAsyncBashFileE2E(t *testing.T) {
	root := t.TempDir()
	workdir := t.TempDir()
	sessions := newFileSessionService(root, "sess-runtime-async-bash")
	tasks := taskfile.NewStore(taskfile.Config{RootDir: filepath.Join(root, "tasks")})
	session, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: workdir,
			CWD: workdir,
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	runtime, err := New(Config{
		Sessions:  sessions,
		TaskStore: tasks,
		AgentFactory: chat.Factory{
			SystemPrompt: "Use tools when necessary.",
		},
		DefaultPolicyMode: "auto-review",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	bashTool, err := shell.NewBash(shell.BashConfig{Runtime: hostRuntimeForTest(t, workdir)})
	if err != nil {
		t.Fatalf("shell.NewBash() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "run async bash",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: &bashTaskLoopRuntimeModel{t: t},
			Tools: []tool.Tool{bashTool},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var finalText string
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			t.Fatalf("runner error = %v", seqErr)
		}
		if event != nil && event.Type == session.EventTypeAssistant {
			finalText = strings.TrimSpace(event.Text)
		}
	}
	if finalText != "async bash done" {
		t.Fatalf("finalText = %q, want %q", finalText, "async bash done")
	}

	doc := readPersistedSessionDocument(t, root, session.SessionID)
	assertPersistedDocumentShape(t, doc, session.SessionID)
	events := documentEvents(doc)
	if len(events) < 6 {
		t.Fatalf("persisted event count = %d, want >= 6", len(events))
	}
	var sawTaskID bool
	for _, item := range events {
		event, _ := item.(map[string]any)
		meta, _ := event["meta"].(map[string]any)
		if meta == nil {
			continue
		}
		if taskID, _ := meta["task_id"].(string); strings.TrimSpace(taskID) != "" {
			sawTaskID = true
			break
		}
	}
	if !sawTaskID {
		t.Fatalf("persisted events missing task_id metadata: %#v", events)
	}
	taskID := mustDocumentTaskID(t, events)
	terminalSnap, err := runtime.Streams().Read(context.Background(), stream.ReadRequest{
		Ref: stream.Ref{
			SessionID: session.SessionID,
			TaskID:    taskID,
		},
	})
	if err != nil {
		t.Fatalf("terminal Read() error = %v", err)
	}
	if terminalSnap.Running {
		t.Fatalf("terminal snapshot still running: %+v", terminalSnap)
	}
	if got := terminalFramesText(terminalSnap.Frames); !strings.Contains(got, "async bash done") {
		t.Fatalf("terminal text = %q, want async bash done", got)
	}
}

func TestRuntimeSpawnACPSubagentFileE2E(t *testing.T) {
	repo := repoRootForE2E(t)
	root := t.TempDir()
	workdir := t.TempDir()
	childSessionRoot := filepath.Join(root, "child-sessions")
	childTaskRoot := filepath.Join(root, "child-tasks")
	sessions := newFileSessionService(root, "sess-runtime-spawn")
	tasks := taskfile.NewStore(taskfile.Config{RootDir: filepath.Join(root, "tasks")})
	session, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: workdir,
			CWD: workdir,
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	attachSpawnParticipant(t, sessions, session.SessionRef, "self")
	assembly, agents := testACPAssembly(assembly.AgentConfig{
		Name:        "self",
		Description: "Spawn a sibling ACP child session.",
		Command:     "go",
		Args:        []string{"run", "./internal/acpe2eagent"},
		WorkDir:     repo,
		Env: map[string]string{
			"SDK_ACP_STUB_REPLY":    "spawn child ok",
			"SDK_ACP_STUB_DELAY_MS": "60",
			"SDK_ACP_SESSION_ROOT":  childSessionRoot,
			"SDK_ACP_TASK_ROOT":     childTaskRoot,
		},
	})
	runtime, err := New(Config{
		Sessions:  sessions,
		TaskStore: tasks,
		Assembly:  assembly,
		AgentFactory: chat.Factory{
			SystemPrompt: "Use tools when necessary.",
		},
		DefaultPolicyMode: "auto-review",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "run spawn flow",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: &spawnTaskLoopRuntimeModel{t: t},
			Tools: []tool.Tool{spawn.New(agents)},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var finalText string
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			t.Fatalf("runner error = %v", seqErr)
		}
		if event != nil && event.Type == session.EventTypeAssistant {
			finalText = strings.TrimSpace(event.Text)
		}
	}
	if finalText != "spawn child ok" {
		t.Fatalf("finalText = %q, want %q", finalText, "spawn child ok")
	}

	doc := readPersistedSessionDocument(t, root, session.SessionID)
	assertPersistedDocumentShape(t, doc, session.SessionID)
	sessionDoc, _ := doc["session"].(map[string]any)
	participants, _ := sessionDoc["participants"].([]any)
	participant := findDocumentParticipant(t, participants, string(session.ParticipantKindSubagent), string(session.ParticipantRoleDelegated))
	childSessionID, _ := participant["session_id"].(string)
	if strings.TrimSpace(childSessionID) == "" {
		t.Fatalf("participant missing child session_id: %#v", participant)
	}
	if got, _ := participant["kind"].(string); got != string(session.ParticipantKindSubagent) {
		t.Fatalf("participant kind = %q, want %q", got, session.ParticipantKindSubagent)
	}
	if got, _ := participant["role"].(string); got != string(session.ParticipantRoleDelegated) {
		t.Fatalf("participant role = %q, want %q", got, session.ParticipantRoleDelegated)
	}

	events := documentEvents(doc)
	var assistantCount int
	for _, item := range events {
		event, _ := item.(map[string]any)
		if got, _ := event["type"].(string); got == string(session.EventTypeAssistant) {
			assistantCount++
		}
	}
	if assistantCount != 1 {
		t.Fatalf("parent assistant event count = %d, want exactly 1 final parent response", assistantCount)
	}
	taskID := mustDocumentTaskID(t, events)
	taskEntry, err := tasks.Get(context.Background(), taskID)
	if err != nil {
		t.Fatalf("task store Get() error = %v", err)
	}
	if taskEntry.Kind != "subagent" {
		t.Fatalf("task kind = %q, want %q", taskEntry.Kind, "subagent")
	}
	if got, _ := taskEntry.Result["result"].(string); got != "spawn child ok" {
		t.Fatalf("subagent result = %q, want %q", got, "spawn child ok")
	}

	childDoc := readPersistedSessionDocument(t, childSessionRoot, childSessionID)
	assertPersistedDocumentShape(t, childDoc, childSessionID)
	var childAssistant string
	for _, item := range documentEvents(childDoc) {
		event, _ := item.(map[string]any)
		if got, _ := event["type"].(string); got == string(session.EventTypeAssistant) {
			childAssistant, _ = event["text"].(string)
		}
	}
	if strings.TrimSpace(childAssistant) != "spawn child ok" {
		t.Fatalf("child assistant text = %q, want %q", childAssistant, "spawn child ok")
	}
}

func TestRuntimeSpawnACPSubagentApprovalPassthroughE2E(t *testing.T) {
	repo := repoRootForE2E(t)
	root := t.TempDir()
	workdir := t.TempDir()
	childSessionRoot := filepath.Join(root, "child-sessions")
	childTaskRoot := filepath.Join(root, "child-tasks")
	sessions := newFileSessionService(root, "sess-runtime-spawn-approval-default")
	tasks := taskfile.NewStore(taskfile.Config{RootDir: filepath.Join(root, "tasks")})
	session, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: workdir,
			CWD: workdir,
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	attachSpawnParticipant(t, sessions, session.SessionRef, "codex")
	assembly, agents := testACPAssembly(assembly.AgentConfig{
		Name:        "codex",
		Description: "External ACP coding agent.",
		Command:     "go",
		Args:        []string{"run", "./internal/acpe2eagent"},
		WorkDir:     repo,
		Env: map[string]string{
			"SDK_ACP_SCRIPTED_MODE": "approval_bash",
			"SDK_ACP_SESSION_ROOT":  childSessionRoot,
			"SDK_ACP_TASK_ROOT":     childTaskRoot,
		},
	})
	runtime, err := New(Config{
		Sessions:  sessions,
		TaskStore: tasks,
		Assembly:  assembly,
		AgentFactory: chat.Factory{
			SystemPrompt: "Use tools when necessary.",
		},
		DefaultPolicyMode: "default",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	var approvalCount int
	requester := approvalRequesterFunc(func(_ context.Context, _ agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		approvalCount++
		return agent.ApprovalResponse{
			Outcome:  "selected",
			OptionID: "allow_once",
			Approved: true,
		}, nil
	})
	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef:        session.SessionRef,
		Input:             "run spawn approval flow",
		ApprovalRequester: requester,
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: &spawnApprovalTaskLoopRuntimeModel{t: t, agent: "codex"},
			Tools: []tool.Tool{spawn.New(agents)},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var finalText string
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			t.Fatalf("runner error = %v", seqErr)
		}
		if event != nil && event.Type == session.EventTypeAssistant {
			finalText = strings.TrimSpace(event.Text)
		}
	}
	if finalText != "child approval ok" {
		t.Fatalf("finalText = %q, want %q", finalText, "child approval ok")
	}
	if approvalCount != 1 {
		t.Fatalf("approvalCount = %d, want 1", approvalCount)
	}
}

func TestRuntimeSpawnACPSubagentFullAccessAutoApprovesE2E(t *testing.T) {
	repo := repoRootForE2E(t)
	root := t.TempDir()
	workdir := t.TempDir()
	childSessionRoot := filepath.Join(root, "child-sessions")
	childTaskRoot := filepath.Join(root, "child-tasks")
	sessions := newFileSessionService(root, "sess-runtime-spawn-approval-full")
	tasks := taskfile.NewStore(taskfile.Config{RootDir: filepath.Join(root, "tasks")})
	session, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: workdir,
			CWD: workdir,
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	attachSpawnParticipant(t, sessions, session.SessionRef, "codex")
	assembly, agents := testACPAssembly(assembly.AgentConfig{
		Name:        "codex",
		Description: "External ACP coding agent.",
		Command:     "go",
		Args:        []string{"run", "./internal/acpe2eagent"},
		WorkDir:     repo,
		Env: map[string]string{
			"SDK_ACP_SCRIPTED_MODE": "approval_bash",
			"SDK_ACP_SESSION_ROOT":  childSessionRoot,
			"SDK_ACP_TASK_ROOT":     childTaskRoot,
		},
	})
	runtime, err := New(Config{
		Sessions:  sessions,
		TaskStore: tasks,
		Assembly:  assembly,
		AgentFactory: chat.Factory{
			SystemPrompt: "Use tools when necessary.",
		},
		DefaultPolicyMode: "auto-review",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	requester := approvalRequesterFunc(func(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		t.Fatalf("unexpected interactive approval request: %+v", req)
		return agent.ApprovalResponse{}, nil
	})
	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef:        session.SessionRef,
		Input:             "run spawn full access flow",
		ApprovalRequester: requester,
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: &spawnApprovalTaskLoopRuntimeModel{t: t, agent: "codex"},
			Tools: []tool.Tool{spawn.New(agents)},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var finalText string
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			t.Fatalf("runner error = %v", seqErr)
		}
		if event != nil && event.Type == session.EventTypeAssistant {
			finalText = strings.TrimSpace(event.Text)
		}
	}
	if finalText != "child approval ok" {
		t.Fatalf("finalText = %q, want %q", finalText, "child approval ok")
	}
}

func TestRuntimeSpawnSelfDisablesNestedSpawnE2E(t *testing.T) {
	repo := repoRootForE2E(t)
	root := t.TempDir()
	workdir := t.TempDir()
	childSessionRoot := filepath.Join(root, "child-sessions")
	childTaskRoot := filepath.Join(root, "child-tasks")
	sessions := newFileSessionService(root, "sess-runtime-spawn-probe")
	tasks := taskfile.NewStore(taskfile.Config{RootDir: filepath.Join(root, "tasks")})
	session, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: workdir,
			CWD: workdir,
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	attachSpawnParticipant(t, sessions, session.SessionRef, "self")
	assembly, agents := testACPAssembly(assembly.AgentConfig{
		Name:        "self",
		Description: "Spawn a sibling ACP child session.",
		Command:     "go",
		Args:        []string{"run", "./internal/acpe2eagent"},
		WorkDir:     repo,
		Env: map[string]string{
			"SDK_ACP_ENABLE_SPAWN":  "1",
			"SDK_ACP_SCRIPTED_MODE": "probe_spawn",
			"SDK_ACP_SESSION_ROOT":  childSessionRoot,
			"SDK_ACP_TASK_ROOT":     childTaskRoot,
		},
	})
	runtime, err := New(Config{
		Sessions:  sessions,
		TaskStore: tasks,
		Assembly:  assembly,
		AgentFactory: chat.Factory{
			SystemPrompt: "Use tools when necessary.",
		},
		DefaultPolicyMode: "auto-review",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "probe nested spawn",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: &spawnProbeTaskLoopRuntimeModel{t: t},
			Tools: []tool.Tool{spawn.New(agents)},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var finalText string
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			t.Fatalf("runner error = %v", seqErr)
		}
		if event != nil && event.Type == session.EventTypeAssistant {
			finalText = strings.TrimSpace(event.Text)
		}
	}
	if finalText != "spawn disabled" {
		t.Fatalf("finalText = %q, want %q", finalText, "spawn disabled")
	}
}

func TestRuntimeAttachACPParticipantSidecarE2E(t *testing.T) {
	repo := repoRootForE2E(t)
	root := t.TempDir()
	workdir := t.TempDir()
	sessions := newFileSessionService(root, "sess-runtime-sidecar")
	session, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: workdir,
			CWD: workdir,
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	assembly, _ := testACPAssembly(assembly.AgentConfig{
		Name:        "copilot",
		Description: "ACP sidecar agent.",
		Command:     "go",
		Args:        []string{"run", "./internal/acpe2eagent"},
		WorkDir:     repo,
		Env: map[string]string{
			"SDK_ACP_STUB_REPLY":   "sidecar ready",
			"SDK_ACP_SESSION_ROOT": filepath.Join(root, "sidecar-sessions"),
			"SDK_ACP_TASK_ROOT":    filepath.Join(root, "sidecar-tasks"),
		},
	})
	runtime, err := New(Config{
		Sessions: sessions,
		Assembly: assembly,
		AgentFactory: chat.Factory{
			SystemPrompt: "Answer tersely.",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	updated, err := runtime.AttachACPParticipant(context.Background(), agent.AttachACPParticipantRequest{
		SessionRef: session.SessionRef,
		Agent:      "copilot",
		Role:       session.ParticipantRoleSidecar,
		Source:     "user_attach",
		Label:      "Copilot Sidecar",
	})
	if err != nil {
		t.Fatalf("AttachACPParticipant() error = %v", err)
	}
	if updated.Controller.Kind != session.ControllerKindKernel {
		t.Fatalf("controller kind = %q, want kernel", updated.Controller.Kind)
	}
	if got := len(updated.Participants); got != 1 {
		t.Fatalf("participants = %d, want 1", got)
	}
	sidecar := updated.Participants[0]
	if sidecar.Kind != session.ParticipantKindACP || sidecar.Role != session.ParticipantRoleSidecar {
		t.Fatalf("participant = %#v, want ACP sidecar binding", sidecar)
	}

	updated, err = runtime.DetachACPParticipant(context.Background(), agent.DetachACPParticipantRequest{
		SessionRef:    session.SessionRef,
		ParticipantID: sidecar.ID,
		Source:        "user_detach",
	})
	if err != nil {
		t.Fatalf("DetachACPParticipant() error = %v", err)
	}
	if got := len(updated.Participants); got != 0 {
		t.Fatalf("participants after detach = %d, want 0", got)
	}

	doc := readPersistedSessionDocument(t, root, session.SessionID)
	assertPersistedDocumentShape(t, doc, session.SessionID)
	var participantActions []string
	for _, item := range documentEvents(doc) {
		event, _ := item.(map[string]any)
		if got, _ := event["type"].(string); got != string(session.EventTypeParticipant) {
			continue
		}
		protocol, _ := event["protocol"].(map[string]any)
		participant, _ := protocol["participant"].(map[string]any)
		action, _ := participant["action"].(string)
		participantActions = append(participantActions, action)
	}
	if !reflect.DeepEqual(participantActions, []string{"attached", "detached"}) {
		t.Fatalf("participant actions = %#v, want attached/detached", participantActions)
	}
}

func TestRuntimeControllerHandoffACPAndBackE2E(t *testing.T) {
	repo := repoRootForE2E(t)
	root := t.TempDir()
	workdir := t.TempDir()
	sessions := newFileSessionService(root, "sess-runtime-handoff")
	session, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: workdir,
			CWD: workdir,
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	assembly, _ := testACPAssembly(assembly.AgentConfig{
		Name:        "codex",
		Description: "ACP main controller.",
		Command:     "go",
		Args:        []string{"run", "./internal/acpe2eagent"},
		WorkDir:     repo,
		Env: map[string]string{
			"SDK_ACP_STUB_REPLY":   "controller handoff ok",
			"SDK_ACP_SESSION_ROOT": filepath.Join(root, "controller-sessions"),
			"SDK_ACP_TASK_ROOT":    filepath.Join(root, "controller-tasks"),
		},
	})
	runtime, err := New(Config{
		Sessions: sessions,
		Assembly: assembly,
		AgentFactory: chat.Factory{
			SystemPrompt: "Answer tersely.",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	updated, err := runtime.HandoffController(context.Background(), agent.HandoffControllerRequest{
		SessionRef: session.SessionRef,
		Kind:       session.ControllerKindACP,
		Agent:      "codex",
		Source:     "user",
		Reason:     "delegate main control",
	})
	if err != nil {
		t.Fatalf("HandoffController(ACP) error = %v", err)
	}
	if updated.Controller.Kind != session.ControllerKindACP {
		t.Fatalf("controller kind = %q, want acp", updated.Controller.Kind)
	}

	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "run through acp controller",
	})
	if err != nil {
		t.Fatalf("Run(ACP) error = %v", err)
	}
	var acpText string
	var acpControllerSeen bool
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			t.Fatalf("runner error = %v", seqErr)
		}
		if event == nil {
			continue
		}
		if event.Scope != nil && event.Scope.Controller.Kind == session.ControllerKindACP {
			acpControllerSeen = true
		}
		if event.Type == session.EventTypeAssistant {
			acpText = strings.TrimSpace(event.Text)
		}
	}
	if acpText != "controller handoff ok" {
		t.Fatalf("ACP assistant text = %q, want %q", acpText, "controller handoff ok")
	}
	if !acpControllerSeen {
		t.Fatal("expected ACP-scoped controller events")
	}

	updated, err = runtime.HandoffController(context.Background(), agent.HandoffControllerRequest{
		SessionRef: session.SessionRef,
		Kind:       session.ControllerKindKernel,
		Source:     "user",
		Reason:     "resume local control",
	})
	if err != nil {
		t.Fatalf("HandoffController(kernel) error = %v", err)
	}
	if updated.Controller.Kind != session.ControllerKindKernel {
		t.Fatalf("controller kind after handoff back = %q, want kernel", updated.Controller.Kind)
	}

	result, err = runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "run through kernel controller",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: exactReplyLLM{text: "kernel handoff back ok"},
		},
	})
	if err != nil {
		t.Fatalf("Run(kernel) error = %v", err)
	}
	var kernelText string
	var kernelControllerSeen bool
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			t.Fatalf("runner error = %v", seqErr)
		}
		if event == nil {
			continue
		}
		if event.Scope != nil && event.Scope.Controller.Kind == session.ControllerKindKernel {
			kernelControllerSeen = true
		}
		if event.Type == session.EventTypeAssistant {
			kernelText = strings.TrimSpace(event.Text)
		}
	}
	if kernelText != "kernel handoff back ok" {
		t.Fatalf("kernel assistant text = %q, want %q", kernelText, "kernel handoff back ok")
	}
	if !kernelControllerSeen {
		t.Fatal("expected kernel-scoped controller events after handoff back")
	}

	doc := readPersistedSessionDocument(t, root, session.SessionID)
	assertPersistedDocumentShape(t, doc, session.SessionID)
	sessionDoc, _ := doc["session"].(map[string]any)
	controllerDoc, _ := sessionDoc["controller"].(map[string]any)
	if got, _ := controllerDoc["kind"].(string); got != string(session.ControllerKindKernel) {
		t.Fatalf("persisted controller kind = %q, want kernel", got)
	}
	var handoffCount int
	for _, item := range documentEvents(doc) {
		event, _ := item.(map[string]any)
		if got, _ := event["type"].(string); got == string(session.EventTypeHandoff) {
			handoffCount++
		}
	}
	if handoffCount < 2 {
		t.Fatalf("handoff event count = %d, want >= 2", handoffCount)
	}
}

func newFileSessionService(root string, sessionID string) session.Service {
	return sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return sessionID },
	}))
}

func readPersistedSessionDocument(t *testing.T, root string, sessionID string) map[string]any {
	t.Helper()

	path := findPersistedSessionDocumentPath(t, root, sessionID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v", path, err)
	}

	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("json.Unmarshal(%q) error = %v", path, err)
	}
	return doc
}

func findPersistedSessionDocumentPath(t *testing.T, root string, sessionID string) string {
	t.Helper()

	var found string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(d.Name()) != ".json" {
			return nil
		}
		if strings.HasSuffix(d.Name(), "-"+sessionID+".json") {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		t.Fatalf("WalkDir(%q) error = %v", root, err)
	}
	if strings.TrimSpace(found) == "" {
		t.Fatalf("persisted document for session %q not found under %q", sessionID, root)
	}
	return found
}

func assertPersistedDocumentShape(t *testing.T, doc map[string]any, sessionID string) {
	t.Helper()

	if got := doc["kind"]; got != "caelis.sdk.session" {
		t.Fatalf("persisted kind = %#v, want %q", got, "caelis.sdk.session")
	}
	if got := doc["version"]; got != float64(1) {
		t.Fatalf("persisted version = %#v, want %d", got, 1)
	}
	sessionDoc, ok := doc["session"].(map[string]any)
	if !ok {
		t.Fatalf("persisted session = %#v, want object", doc["session"])
	}
	if got := sessionDoc["session_id"]; got != sessionID {
		t.Fatalf("persisted session_id = %#v, want %q", got, sessionID)
	}
	if _, ok := doc["events"].([]any); !ok {
		t.Fatalf("persisted events = %#v, want array", doc["events"])
	}
	if _, ok := doc["state"].(map[string]any); !ok {
		t.Fatalf("persisted state = %#v, want object", doc["state"])
	}
}

func mustDocumentTaskID(t *testing.T, events []any) string {
	t.Helper()
	for _, item := range events {
		event, _ := item.(map[string]any)
		meta, _ := event["meta"].(map[string]any)
		if meta == nil {
			continue
		}
		if taskID, _ := meta["task_id"].(string); strings.TrimSpace(taskID) != "" {
			return strings.TrimSpace(taskID)
		}
	}
	t.Fatal("persisted document missing task_id metadata")
	return ""
}

func documentEvents(doc map[string]any) []any {
	events, _ := doc["events"].([]any)
	return events
}

func attachSpawnParticipant(t *testing.T, sessions session.Service, ref session.SessionRef, agent string) {
	t.Helper()
	agent = strings.TrimSpace(agent)
	if agent == "" {
		t.Fatal("attachSpawnParticipant requires agent")
	}
	_, err := sessions.PutParticipant(context.Background(), session.PutParticipantRequest{
		SessionRef: ref,
		Binding: session.ParticipantBinding{
			ID:        "sidecar-" + strings.ToLower(agent),
			Kind:      session.ParticipantKindACP,
			Role:      session.ParticipantRoleSidecar,
			Label:     agent,
			SessionID: "remote-" + strings.ToLower(agent),
			Source:    "test_attach",
		},
	})
	if err != nil {
		t.Fatalf("PutParticipant(%q) error = %v", agent, err)
	}
}

func findDocumentParticipant(t *testing.T, participants []any, kind string, role string) map[string]any {
	t.Helper()
	for _, item := range participants {
		participant, _ := item.(map[string]any)
		if participant == nil {
			continue
		}
		gotKind, _ := participant["kind"].(string)
		gotRole, _ := participant["role"].(string)
		if gotKind == kind && gotRole == role {
			return participant
		}
	}
	t.Fatalf("participants = %#v, want kind=%q role=%q", participants, kind, role)
	return nil
}

func repoRootForE2E(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() error = %v", err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repo root")
		}
		dir = parent
	}
}
