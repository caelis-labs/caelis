// Package layer4 contains end-to-end tests for the Layer4 Agent Core.
//
// These tests use real API calls against the configured model provider
// to verify the full pipeline: session creation → model call → tool
// execution → event persistence → model context reconstruction →
// ACP projection.
//
// Run with: go test ./test/e2e/layer4/ -v -count=1
package layer4

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/acp"
	"github.com/OnslaughtSnail/caelis/agent"
	"github.com/OnslaughtSnail/caelis/agent/llmagent"
	"github.com/OnslaughtSnail/caelis/model"
	"github.com/OnslaughtSnail/caelis/model/providers"
	"github.com/OnslaughtSnail/caelis/policy"
	"github.com/OnslaughtSnail/caelis/runner"
	"github.com/OnslaughtSnail/caelis/sandbox"
	"github.com/OnslaughtSnail/caelis/session"
	filesession "github.com/OnslaughtSnail/caelis/session/file"
	"github.com/OnslaughtSnail/caelis/tool"
	"github.com/OnslaughtSnail/caelis/tool/builtin/filesystem"
	"github.com/OnslaughtSnail/caelis/tool/builtin/shell"
)

// ─── Config loading ──────────────────────────────────────────────────

type caelisConfig struct {
	Models struct {
		DefaultModelID string `json:"default_model_id"`
		DefaultAlias   string `json:"default_alias"`
		Profiles       []struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
			BaseURL  string `json:"base_url"`
			Token    string `json:"token"`
		} `json:"profiles"`
		Configs []struct {
			ID        string `json:"id"`
			ProfileID string `json:"profile_id"`
			Model     string `json:"model"`
		} `json:"configs"`
	} `json:"models"`
}

func loadConfig(t *testing.T) caelisConfig {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("get home: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".caelis", "config.json"))
	if err != nil {
		if os.IsNotExist(err) {
			t.Skip("~/.caelis/config.json not configured")
		}
		t.Fatalf("read config: %v", err)
	}
	var cfg caelisConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return cfg
}

func findProvider(cfg caelisConfig, profileID string) (baseURL, token string) {
	for _, p := range cfg.Models.Profiles {
		if p.ID == profileID {
			return p.BaseURL, p.Token
		}
	}
	return "", ""
}

func defaultModel(cfg caelisConfig) (providers.OpenAIConfig, bool) {
	// Parse default_model_id format: "profile_id/provider/model"
	parts := strings.SplitN(cfg.Models.DefaultModelID, "/", 3)
	if len(parts) < 3 {
		return providers.OpenAIConfig{}, false
	}
	profileID := parts[0]
	modelName := parts[2]

	baseURL, token := findProvider(cfg, profileID)
	if baseURL == "" || token == "" {
		return providers.OpenAIConfig{}, false
	}

	return providers.OpenAIConfig{
		Name:    cfg.Models.DefaultModelID,
		BaseURL: baseURL,
		Token:   token,
		Model:   modelName,
	}, true
}

// ─── E2E Tests ───────────────────────────────────────────────────────

// TestE2E_RealProviderSmokeUsesRunnerRegistry tests the real provider path:
// session create → runner resolves model registry → real model stream →
// assistant response → event persistence → ACP projection.
func TestE2E_RealProviderSmokeUsesRunnerRegistry(t *testing.T) {
	cfg := loadConfig(t)
	modelCfg, ok := defaultModel(cfg)
	if !ok {
		t.Skip("no default model configured")
	}
	t.Logf("Using model: %s @ %s", modelCfg.Model, modelCfg.BaseURL)

	// Create provider.
	llm := providers.NewOpenAI(modelCfg)
	modelReg := &recordingModelRegistry{
		llm: llm,
		info: model.ModelInfo{
			ModelID:       modelCfg.Model,
			DisplayName:   modelCfg.Model,
			Provider:      "configured",
			SupportsTools: false,
		},
	}

	// Create session store.
	svc := session.InMemoryService()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create session.
	sess, err := svc.Create(ctx, session.CreateRequest{
		AppName: "e2e-test", UserID: "test", WorkspaceKey: "e2e",
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	// Create agent.
	llmAgent := llmagent.New(llmagent.Config{
		Name:     "e2e-agent",
		ModelRef: model.Ref{ModelID: modelCfg.Model},
	})

	// Create runner.
	r, err := runner.New(runner.Config{
		Agent:         llmAgent,
		Sessions:      svc,
		ModelRegistry: modelReg,
	})
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}

	// Run a simple smoke prompt. This test verifies provider/runner plumbing,
	// not exact natural-language compliance.
	const marker = "E2E_TEST_PASSED"
	var events []session.Event
	for evt, err := range r.Run(ctx, runner.RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "Reply with a short acknowledgement containing " + marker}}},
	}) {
		if err != nil {
			t.Fatalf("Run error: %v", err)
		}
		events = append(events, evt)
		t.Logf("Event: kind=%s vis=%s text=%q", evt.Kind, evt.Visibility, evt.TextContent())
	}

	// Verify events.
	if len(events) < 2 {
		t.Fatalf("got %d events, want >= 2", len(events))
	}
	if len(modelReg.resolved) != 1 {
		t.Fatalf("model registry resolves: got %d, want 1", len(modelReg.resolved))
	}
	if events[0].Kind != session.EventKindUser {
		t.Errorf("event 0 kind: got %q, want %q", events[0].Kind, session.EventKindUser)
	}
	if events[1].Kind != session.EventKindAssistant {
		t.Errorf("event 1 kind: got %q, want %q", events[1].Kind, session.EventKindAssistant)
	}
	assistantText := events[1].TextContent()
	if strings.TrimSpace(assistantText) == "" {
		t.Fatal("assistant response is empty")
	}
	if !strings.Contains(assistantText, marker) {
		t.Fatalf("assistant response %q does not contain %q", assistantText, marker)
	}

	// Verify events persisted.
	persisted, err := svc.Events(ctx, session.EventsRequest{SessionRef: sess.Ref})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(persisted) < 2 {
		t.Fatalf("persisted: got %d, want >= 2", len(persisted))
	}

	// Verify model context reconstruction.
	modelCtx := session.ModelContextFromEvents(persisted)
	if len(modelCtx) < 2 {
		t.Fatalf("model context: got %d messages, want >= 2", len(modelCtx))
	}
	if modelCtx[0].Role != model.RoleUser {
		t.Errorf("model context msg 0 role: got %q", modelCtx[0].Role)
	}
	if modelCtx[1].Role != model.RoleAssistant {
		t.Errorf("model context msg 1 role: got %q", modelCtx[1].Role)
	}

	// Verify ACP projection via acp.ProjectEvent (canonical path).
	var acpUpdates []acp.Update
	for _, e := range persisted {
		updates := acp.ProjectEvent(&e)
		acpUpdates = append(acpUpdates, updates...)
	}
	if len(acpUpdates) == 0 {
		t.Error("expected ACP updates from persisted events")
	}
	// Verify _meta.caelis namespace on tool-related updates.
	for _, u := range acpUpdates {
		if tc, ok := u.(acp.ToolCallUpdate); ok && tc.Meta != nil {
			if _, hasCaelis := tc.Meta["caelis"]; !hasCaelis {
				t.Error("tool_call _meta should have caelis namespace")
			}
		}
	}

	t.Logf("E2E basic flow: %d events, %d model messages", len(events), len(modelCtx))
}

// TestE2E_RunnerToolLoopApprovalSandboxAndReplay verifies the critical Layer4
// chain without relying on a model's tool-call support:
// runner registry resolution → model tool_call → policy approval →
// sandbox backend execution → durable tool events → replay parity → ACP.
func TestE2E_RunnerToolLoopApprovalSandboxAndReplay(t *testing.T) {
	ctx := context.Background()
	fileSvc, err := filesession.New(filesession.Config{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New file service: %v", err)
	}
	sess, err := fileSvc.Create(ctx, session.CreateRequest{
		AppName: "e2e-tool", UserID: "test", WorkspaceKey: "workspace",
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	llm := &scriptedToolLLM{}
	modelReg := &recordingModelRegistry{
		llm:  llm,
		info: model.ModelInfo{ModelID: "scripted-tool", SupportsTools: true},
	}
	toolReg := newTestToolRegistry(shell.All()...)
	backend := &recordingSandboxBackend{fs: newTempFS(t.TempDir())}
	sandboxFactory := &recordingSandboxFactory{backend: backend}
	approver := &recordingApprover{approved: true}
	llmAgent := llmagent.New(llmagent.Config{
		Name:     "e2e-tool-agent",
		ModelRef: model.Ref{ModelID: "scripted-tool"},
		Tools:    []string{"RUN_COMMAND"},
	})
	r, err := runner.New(runner.Config{
		Agent:         llmAgent,
		Sessions:      fileSvc,
		ModelRegistry: modelReg,
		ToolRegistry:  toolReg,
		Sandbox:       sandboxFactory,
		Policy:        &approvalNeededPolicy{},
		Approver:      approver,
	})
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}

	events := collectRun(t, r, ctx, runner.RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "run the sandbox command"}}},
	})

	assertEventKinds(t, persistedLiveEvents(events),
		session.EventKindUser,
		session.EventKindToolCall,
		session.EventKindToolResult,
		session.EventKindAssistant,
	)
	assertTransientNoticeCount(t, events, 2)
	if len(modelReg.resolved) != 1 {
		t.Fatalf("model registry resolves: got %d, want 1", len(modelReg.resolved))
	}
	if got := toolReg.lookups; !reflect.DeepEqual(got, []string{"RUN_COMMAND"}) {
		t.Fatalf("tool registry lookups: got %#v, want RUN_COMMAND", got)
	}
	if sandboxFactory.created != 1 {
		t.Fatalf("sandbox creates: got %d, want 1", sandboxFactory.created)
	}
	if len(backend.runRequests) != 1 {
		t.Fatalf("sandbox backend runs: got %d, want 1", len(backend.runRequests))
	}
	if backend.runRequests[0].Command != "layer4-e2e-command" {
		t.Fatalf("sandbox command: got %q", backend.runRequests[0].Command)
	}
	if len(approver.requests) != 1 {
		t.Fatalf("approval requests: got %d, want 1", len(approver.requests))
	}
	if approver.requests[0].CallID != "shell-call-1" {
		t.Fatalf("approval call id: got %q", approver.requests[0].CallID)
	}

	persisted, err := fileSvc.Events(ctx, session.EventsRequest{SessionRef: sess.Ref})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	assertEventKinds(t, persisted,
		session.EventKindUser,
		session.EventKindToolCall,
		session.EventKindToolResult,
		session.EventKindAssistant,
	)
	if len(llm.requests) != 2 {
		t.Fatalf("model requests: got %d, want 2", len(llm.requests))
	}
	assertToolSpecs(t, llm.requests[0].Tools, "RUN_COMMAND", "TASK")

	// The second live model request must equal durable replay through the
	// tool result boundary. This catches runtime/replay drift.
	replayedPrefix := session.ModelContextFromEvents(persisted[:len(persisted)-1])
	assertMessagesEqual(t, llm.requests[1].Messages, replayedPrefix)

	fullReplay := session.ModelContextFromEvents(persisted)
	if len(fullReplay) != 4 {
		t.Fatalf("full replay messages: got %d, want 4", len(fullReplay))
	}
	if fullReplay[1].Role != model.RoleAssistant || fullReplay[1].Content[0].ToolUse == nil {
		t.Fatalf("replay message 1 = %#v, want assistant tool_use", fullReplay[1])
	}
	if fullReplay[2].Role != model.RoleTool || fullReplay[2].Content[0].ToolResult == nil {
		t.Fatalf("replay message 2 = %#v, want tool result", fullReplay[2])
	}
	if !strings.Contains(fullReplay[2].Content[0].ToolResult.Content, "SANDBOX_BACKEND") {
		t.Fatalf("tool replay output: got %q", fullReplay[2].Content[0].ToolResult.Content)
	}

	projections := projectAllACP(persisted)
	assertACPSequence(t, projections,
		acp.UpdateUserMessage,
		acp.UpdateToolCall,
		acp.UpdateToolCallInfo,
		acp.UpdateAgentMessage,
	)
	call := requireToolCallUpdate(t, projections, 1)
	result := requireToolCallUpdate(t, projections, 2)
	if call.ToolCallID != "shell-call-1" || result.ToolCallID != "shell-call-1" {
		t.Fatalf("ACP tool call ids not linked: %#v %#v", call, result)
	}
	if call.Meta == nil || result.Meta == nil || call.Meta["caelis"] == nil || result.Meta["caelis"] == nil {
		t.Fatal("ACP tool projections must carry _meta.caelis")
	}
}

// TestE2E_FilesystemToolsThroughRunner verifies filesystem tools are not only
// unit-tested components: the model requests WRITE/READ through runner, the
// sandbox filesystem is used, and replay parity holds across multiple tools.
func TestE2E_FilesystemToolsThroughRunner(t *testing.T) {
	ctx := context.Background()
	fileSvc, err := filesession.New(filesession.Config{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New file service: %v", err)
	}
	sess, err := fileSvc.Create(ctx, session.CreateRequest{
		AppName: "e2e-fs", UserID: "test", WorkspaceKey: "workspace",
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	llm := &scriptedFilesystemLLM{}
	modelReg := &recordingModelRegistry{
		llm:  llm,
		info: model.ModelInfo{ModelID: "scripted-fs", SupportsTools: true},
	}
	fs := newTempFS(t.TempDir())
	backend := &recordingSandboxBackend{fs: fs}
	llmAgent := llmagent.New(llmagent.Config{
		Name:     "e2e-fs-agent",
		ModelRef: model.Ref{ModelID: "scripted-fs"},
		Tools:    []string{"WRITE", "READ"},
	})
	r, err := runner.New(runner.Config{
		Agent:         llmAgent,
		Sessions:      fileSvc,
		ModelRegistry: modelReg,
		ToolRegistry:  newTestToolRegistry(filesystem.All()...),
		Sandbox:       &recordingSandboxFactory{backend: backend},
	})
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}

	events := collectRun(t, r, ctx, runner.RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "write then read the file"}}},
	})
	assertEventKinds(t, persistedLiveEvents(events),
		session.EventKindUser,
		session.EventKindToolCall,
		session.EventKindToolResult,
		session.EventKindToolCall,
		session.EventKindToolResult,
		session.EventKindAssistant,
	)
	assertTransientNoticeCount(t, events, 4)
	data, err := os.ReadFile(filepath.Join(fs.root, "notes", "layer4.txt"))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "filesystem-e2e-content" {
		t.Fatalf("written file = %q", string(data))
	}
	if len(llm.requests) != 3 {
		t.Fatalf("model requests: got %d, want 3", len(llm.requests))
	}
	assertToolSpecs(t, llm.requests[0].Tools, "WRITE", "READ")

	persisted, err := fileSvc.Events(ctx, session.EventsRequest{SessionRef: sess.Ref})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	replayAfterWrite := session.ModelContextFromEvents(persisted[:3])
	assertMessagesEqual(t, llm.requests[1].Messages, replayAfterWrite)
	replayAfterRead := session.ModelContextFromEvents(persisted[:5])
	assertMessagesEqual(t, llm.requests[2].Messages, replayAfterRead)

	projections := projectAllACP(persisted)
	assertACPSequence(t, projections,
		acp.UpdateUserMessage,
		acp.UpdateToolCall,
		acp.UpdateToolCallInfo,
		acp.UpdateToolCall,
		acp.UpdateToolCallInfo,
		acp.UpdateAgentMessage,
	)
}

// TestE2E_ReplayGoldenRoundTrip tests that durable events can
// reconstruct the same model context as the runtime.
func TestE2E_ReplayGoldenRoundTrip(t *testing.T) {
	cfg := loadConfig(t)
	modelCfg, ok := defaultModel(cfg)
	if !ok {
		t.Skip("no default model configured")
	}

	llm := providers.NewOpenAI(modelCfg)
	modelReg := &recordingModelRegistry{
		llm: llm,
		info: model.ModelInfo{
			ModelID:       modelCfg.Model,
			DisplayName:   modelCfg.Model,
			Provider:      "configured",
			SupportsTools: false,
		},
	}

	// Use file-backed store for persistence test.
	dir := t.TempDir()
	fileSvc, err := filesession.New(filesession.Config{RootDir: dir})
	if err != nil {
		t.Fatalf("New file service: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	sess, _ := fileSvc.Create(ctx, session.CreateRequest{
		AppName: "e2e-replay", UserID: "test", WorkspaceKey: "e2e",
	})

	llmAgent := llmagent.New(llmagent.Config{
		Name:     "e2e-replay-agent",
		ModelRef: model.Ref{ModelID: modelCfg.Model},
	})

	r, err := runner.New(runner.Config{
		Agent:         llmAgent,
		Sessions:      fileSvc,
		ModelRegistry: modelReg,
	})
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}

	// First turn.
	var turn1Events []session.Event
	for evt, err := range r.Run(ctx, runner.RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "Remember the number 42. Reply with just '42'."}}},
	}) {
		if err != nil {
			t.Fatalf("Turn 1 error: %v", err)
		}
		turn1Events = append(turn1Events, evt)
	}

	// Read back from disk.
	durable1, err := fileSvc.Events(ctx, session.EventsRequest{SessionRef: sess.Ref})
	if err != nil {
		t.Fatalf("Events after turn 1: %v", err)
	}
	t.Logf("Turn 1: %d events persisted", len(durable1))

	// Reconstruct model context.
	ctx1 := session.ModelContextFromEvents(durable1)
	if len(ctx1) < 2 {
		t.Fatalf("model context after turn 1: got %d, want >= 2", len(ctx1))
	}

	// Second turn — should see history.
	var turn2Events []session.Event
	for evt, err := range r.Run(ctx, runner.RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "What number did I ask you to remember?"}}},
	}) {
		if err != nil {
			t.Fatalf("Turn 2 error: %v", err)
		}
		turn2Events = append(turn2Events, evt)
	}

	// Read all events.
	durable2, _ := fileSvc.Events(ctx, session.EventsRequest{SessionRef: sess.Ref})
	t.Logf("Turn 2: %d total events persisted", len(durable2))

	// Reconstruct full model context — should include both turns.
	ctx2 := session.ModelContextFromEvents(durable2)
	if len(ctx2) < 4 {
		t.Fatalf("model context after turn 2: got %d, want >= 4", len(ctx2))
	}
	if len(modelReg.resolved) != 2 {
		t.Fatalf("model registry resolves: got %d, want 2", len(modelReg.resolved))
	}

	// Verify the second turn's assistant response mentions "42".
	lastMsg := ctx2[len(ctx2)-1]
	if lastMsg.Role != model.RoleAssistant {
		t.Errorf("last message role: got %q, want %q", lastMsg.Role, model.RoleAssistant)
	}

	t.Logf("E2E replay: %d total model messages across 2 turns", len(ctx2))
}

// TestE2E_Compaction tests that compaction triggers when context
// is large enough.
func TestE2E_Compaction(t *testing.T) {
	// Create a large fake message set.
	msgs := make([]model.Message, 100)
	for i := range msgs {
		msgs[i] = model.Message{
			Role:    model.RoleUser,
			Content: []model.Part{{Text: strings.Repeat("x", 5000)}},
		}
	}

	// Small context window should trigger compaction.
	compacted, ok, _ := runner.CompactModelContext(msgs, 50000)
	if !ok {
		t.Error("expected compaction to trigger")
	}
	if len(compacted) >= len(msgs) {
		t.Errorf("compacted %d messages, expected fewer than %d", len(compacted), len(msgs))
	}

	// Verify summary message exists.
	found := false
	for _, m := range compacted {
		if m.Role == model.RoleSystem && len(m.Content) > 0 {
			if strings.Contains(m.Content[0].Text, "compacted") {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected summary system message in compacted context")
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────

func collectRun(t *testing.T, r *runner.Runner, ctx context.Context, req runner.RunRequest) []session.Event {
	t.Helper()
	var events []session.Event
	for evt, err := range r.Run(ctx, req) {
		if err != nil {
			t.Fatalf("Run error: %v", err)
		}
		events = append(events, evt)
	}
	return events
}

func assertEventKinds(t *testing.T, events []session.Event, want ...session.EventKind) {
	t.Helper()
	if len(events) != len(want) {
		t.Fatalf("events: got %d, want %d\n%v", len(events), len(want), eventKinds(events))
	}
	for i, kind := range want {
		if events[i].Kind != kind {
			t.Fatalf("event %d kind: got %q, want %q\n%v", i, events[i].Kind, kind, eventKinds(events))
		}
	}
}

func persistedLiveEvents(events []session.Event) []session.Event {
	out := make([]session.Event, 0, len(events))
	for _, event := range events {
		if event.Visibility.IsPersisted() {
			out = append(out, event)
		}
	}
	return out
}

func assertTransientNoticeCount(t *testing.T, events []session.Event, want int) {
	t.Helper()
	var got int
	for _, event := range events {
		if event.Kind == session.EventKindNotice && event.Visibility == session.VisibilityUIOnly {
			got++
		}
	}
	if got != want {
		t.Fatalf("transient notice count = %d, want %d\n%v", got, want, eventKinds(events))
	}
}

func eventKinds(events []session.Event) []session.EventKind {
	kinds := make([]session.EventKind, len(events))
	for i := range events {
		kinds[i] = events[i].Kind
	}
	return kinds
}

func assertMessagesEqual(t *testing.T, got, want []model.Message) {
	t.Helper()
	if reflect.DeepEqual(got, want) {
		return
	}
	gotJSON, _ := json.MarshalIndent(got, "", "  ")
	wantJSON, _ := json.MarshalIndent(want, "", "  ")
	t.Fatalf("model messages mismatch\n--- got ---\n%s\n--- want ---\n%s", gotJSON, wantJSON)
}

func assertToolSpecs(t *testing.T, got []model.ToolSpec, want ...string) {
	t.Helper()
	names := make([]string, len(got))
	for i := range got {
		names[i] = got[i].Name
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("model tool specs: got %#v, want %#v", names, want)
	}
}

func projectAllACP(events []session.Event) []acp.Update {
	var projections []acp.Update
	for i := range events {
		projections = append(projections, acp.ProjectEvent(&events[i])...)
	}
	return projections
}

func assertACPSequence(t *testing.T, projections []acp.Update, want ...acp.UpdateKind) {
	t.Helper()
	if len(projections) != len(want) {
		t.Fatalf("ACP projections: got %d, want %d\n%v", len(projections), len(want), acpKinds(projections))
	}
	for i, kind := range want {
		if projections[i].SessionUpdateType() != kind {
			t.Fatalf("ACP projection %d: got %q, want %q\n%v", i, projections[i].SessionUpdateType(), kind, acpKinds(projections))
		}
	}
}

func requireToolCallUpdate(t *testing.T, projections []acp.Update, idx int) acp.ToolCallUpdate {
	t.Helper()
	update, ok := projections[idx].(acp.ToolCallUpdate)
	if !ok {
		t.Fatalf("ACP projection %d: got %T, want ToolCallUpdate", idx, projections[idx])
	}
	return update
}

func acpKinds(projections []acp.Update) []acp.UpdateKind {
	kinds := make([]acp.UpdateKind, len(projections))
	for i := range projections {
		kinds[i] = projections[i].SessionUpdateType()
	}
	return kinds
}

type recordingModelRegistry struct {
	llm      model.LLM
	info     model.ModelInfo
	resolved []model.Ref
}

func (r *recordingModelRegistry) Resolve(_ context.Context, ref model.Ref) (model.LLM, model.ModelInfo, error) {
	r.resolved = append(r.resolved, ref)
	if r.llm == nil {
		return nil, model.ModelInfo{}, fmt.Errorf("test model registry: no llm")
	}
	info := r.info
	if info.ModelID == "" {
		info.ModelID = ref.ModelID
	}
	return r.llm, info, nil
}

func (r *recordingModelRegistry) List(context.Context) ([]model.ModelInfo, error) {
	return []model.ModelInfo{r.info}, nil
}

type testToolRegistry struct {
	tools   map[string]tool.Tool
	lookups []string
}

func newTestToolRegistry(tools ...tool.Tool) *testToolRegistry {
	r := &testToolRegistry{tools: make(map[string]tool.Tool)}
	for _, t := range tools {
		if t == nil {
			continue
		}
		r.tools[strings.ToUpper(t.Definition().Name)] = t
	}
	return r
}

func (r *testToolRegistry) Lookup(_ context.Context, name string) (tool.Tool, bool, error) {
	r.lookups = append(r.lookups, name)
	t, ok := r.tools[strings.ToUpper(name)]
	return t, ok, nil
}

func (r *testToolRegistry) List(context.Context) ([]tool.Tool, error) {
	out := make([]tool.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out, nil
}

type scriptedToolLLM struct {
	requests []model.Request
}

func (m *scriptedToolLLM) Name() string { return "scripted-tool" }

func (m *scriptedToolLLM) Generate(_ context.Context, req model.Request) iter.Seq2[model.ResponseEvent, error] {
	callIndex := len(m.requests)
	m.requests = append(m.requests, cloneModelRequest(req))
	return func(yield func(model.ResponseEvent, error) bool) {
		switch callIndex {
		case 0:
			yield(model.ResponseEvent{ToolCall: &model.ToolCallDelta{
				CallID: "shell-call-1",
				Name:   "RUN_COMMAND",
				Args: map[string]any{
					"command":             "layer4-e2e-command",
					"sandbox_permissions": "require_escalated",
				},
			}}, nil)
		case 1:
			yield(model.ResponseEvent{TextDelta: "sandbox-result-ok"}, nil)
		default:
			yield(model.ResponseEvent{}, fmt.Errorf("unexpected model call %d", callIndex))
		}
	}
}

type scriptedFilesystemLLM struct {
	requests []model.Request
}

func (m *scriptedFilesystemLLM) Name() string { return "scripted-fs" }

func (m *scriptedFilesystemLLM) Generate(_ context.Context, req model.Request) iter.Seq2[model.ResponseEvent, error] {
	callIndex := len(m.requests)
	m.requests = append(m.requests, cloneModelRequest(req))
	return func(yield func(model.ResponseEvent, error) bool) {
		switch callIndex {
		case 0:
			yield(model.ResponseEvent{ToolCall: &model.ToolCallDelta{
				CallID: "write-call-1",
				Name:   "WRITE",
				Args: map[string]any{
					"path":    "notes/layer4.txt",
					"content": "filesystem-e2e-content",
				},
			}}, nil)
		case 1:
			yield(model.ResponseEvent{ToolCall: &model.ToolCallDelta{
				CallID: "read-call-1",
				Name:   "READ",
				Args: map[string]any{
					"path": "notes/layer4.txt",
				},
			}}, nil)
		case 2:
			yield(model.ResponseEvent{TextDelta: "filesystem-result-ok"}, nil)
		default:
			yield(model.ResponseEvent{}, fmt.Errorf("unexpected model call %d", callIndex))
		}
	}
}

func cloneModelRequest(req model.Request) model.Request {
	cp := req
	cp.Messages = cloneMessages(req.Messages)
	cp.Tools = append([]model.ToolSpec(nil), req.Tools...)
	if req.Metadata != nil {
		cp.Metadata = make(map[string]any, len(req.Metadata))
		for k, v := range req.Metadata {
			cp.Metadata[k] = v
		}
	}
	return cp
}

func cloneMessages(messages []model.Message) []model.Message {
	out := make([]model.Message, len(messages))
	for i := range messages {
		out[i].Role = messages[i].Role
		out[i].Content = make([]model.Part, len(messages[i].Content))
		for j := range messages[i].Content {
			out[i].Content[j] = clonePart(messages[i].Content[j])
		}
	}
	return out
}

func clonePart(part model.Part) model.Part {
	cp := part
	if part.ToolUse != nil {
		args := make(map[string]any, len(part.ToolUse.Args))
		for k, v := range part.ToolUse.Args {
			args[k] = v
		}
		cp.ToolUse = &model.ToolUse{
			CallID: part.ToolUse.CallID,
			Name:   part.ToolUse.Name,
			Args:   args,
		}
	}
	if part.ToolResult != nil {
		cp.ToolResult = &model.ToolResult{
			CallID:  part.ToolResult.CallID,
			Content: part.ToolResult.Content,
			IsError: part.ToolResult.IsError,
		}
	}
	if part.InlineData != nil {
		cp.InlineData = &model.InlineData{
			MIMEType: part.InlineData.MIMEType,
			Data:     append([]byte(nil), part.InlineData.Data...),
		}
	}
	if part.FileRef != nil {
		cp.FileRef = &model.FileRef{
			URI:      part.FileRef.URI,
			MIMEType: part.FileRef.MIMEType,
		}
	}
	return cp
}

type approvalNeededPolicy struct{}

func (p *approvalNeededPolicy) Evaluate(_ context.Context, _ policy.Request) (policy.Decision, error) {
	return policy.Decision{Outcome: policy.OutcomeApprovalNeeded, Reason: "test approval required"}, nil
}

type recordingApprover struct {
	approved bool
	requests []agent.ApprovalRequest
}

func (a *recordingApprover) RequestApproval(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
	a.requests = append(a.requests, req)
	return agent.ApprovalResponse{Approved: a.approved, Reason: "test approval"}, nil
}

type recordingSandboxFactory struct {
	backend *recordingSandboxBackend
	created int
}

func (f *recordingSandboxFactory) Available(context.Context) ([]sandbox.Descriptor, error) {
	return []sandbox.Descriptor{{Name: "recording", Description: "recording test sandbox"}}, nil
}

func (f *recordingSandboxFactory) Create(_ context.Context, _ sandbox.Config) (sandbox.Backend, error) {
	f.created++
	if f.backend == nil {
		f.backend = &recordingSandboxBackend{fs: newTempFS(os.TempDir())}
	}
	return f.backend, nil
}

type recordingSandboxBackend struct {
	fs          sandbox.FileSystem
	runRequests []sandbox.CommandRequest
}

func (b *recordingSandboxBackend) Name() string { return "recording" }

func (b *recordingSandboxBackend) Describe(context.Context) (sandbox.Descriptor, error) {
	return sandbox.Descriptor{Name: "recording", Description: "recording test sandbox"}, nil
}

func (b *recordingSandboxBackend) Run(_ context.Context, req sandbox.CommandRequest) (sandbox.CommandResult, error) {
	b.runRequests = append(b.runRequests, req)
	return sandbox.CommandResult{
		Stdout:   []byte("SANDBOX_BACKEND:" + req.Command),
		ExitCode: 0,
	}, nil
}

func (b *recordingSandboxBackend) FileSystem(context.Context, sandbox.Constraints) (sandbox.FileSystem, error) {
	if b.fs == nil {
		b.fs = newTempFS(os.TempDir())
	}
	return b.fs, nil
}

func (b *recordingSandboxBackend) Status(context.Context) (sandbox.Status, error) {
	return sandbox.Status{Running: true}, nil
}

func (b *recordingSandboxBackend) Close() error { return nil }

type tempFS struct {
	root string
}

func newTempFS(root string) *tempFS {
	return &tempFS{root: root}
}

func (fs *tempFS) Read(path string) ([]byte, error) {
	return os.ReadFile(fs.fullPath(path))
}

func (fs *tempFS) Write(path string, data []byte) error {
	full := fs.fullPath(path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, data, 0o644)
}

func (fs *tempFS) List(path string) ([]string, error) {
	entries, err := os.ReadDir(fs.fullPath(path))
	if err != nil {
		return nil, err
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return names, nil
}

func (fs *tempFS) Exists(path string) (bool, error) {
	_, err := os.Stat(fs.fullPath(path))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (fs *tempFS) Delete(path string) error {
	return os.Remove(fs.fullPath(path))
}

func (fs *tempFS) Stat(path string) (sandbox.FileInfo, error) {
	info, err := os.Stat(fs.fullPath(path))
	if err != nil {
		return sandbox.FileInfo{}, err
	}
	return sandbox.FileInfo{
		Name:    info.Name(),
		IsDir:   info.IsDir(),
		Size:    info.Size(),
		ModTime: info.ModTime(),
	}, nil
}

func (fs *tempFS) fullPath(path string) string {
	clean := filepath.Clean(strings.TrimPrefix(path, string(filepath.Separator)))
	return filepath.Join(fs.root, clean)
}
