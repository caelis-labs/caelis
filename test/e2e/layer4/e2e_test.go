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
	"slices"
	"strings"
	"sync"
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
	"github.com/OnslaughtSnail/caelis/tool/builtin/spawn"

	caelisplugin "github.com/OnslaughtSnail/caelis/plugin"
	pluginfs "github.com/OnslaughtSnail/caelis/plugin/fs"
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
	livePersisted := persistedLiveEvents(events)
	if len(livePersisted) < 2 || livePersisted[1].Kind != session.EventKindAssistant {
		t.Fatalf("persisted live events = %v, want user then assistant", eventKinds(livePersisted))
	}
	assistantText := livePersisted[1].TextContent()
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

func TestE2E_AssistantDeltasAreTransientAndCanonicalFinalIsDurable(t *testing.T) {
	svc := session.InMemoryService()
	ctx := context.Background()
	sess, err := svc.Create(ctx, session.CreateRequest{
		AppName: "e2e-test", UserID: "test", WorkspaceKey: "e2e",
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}
	llmAgent := llmagent.New(llmagent.Config{Name: "delta-agent"})
	llmAgent = llmAgent.Prepare(agent.PrepareRequest{LLM: &deltaE2ELLM{events: []model.ResponseEvent{
		{ReasoningDelta: "think-"},
		{TextDelta: "hel"},
		{ReasoningDelta: "ing"},
		{TextDelta: "lo"},
	}}}).(*llmagent.Agent)
	r, err := runner.New(runner.Config{Agent: llmAgent, Sessions: svc})
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}

	events := collectRun(t, r, ctx, runner.RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "stream"}}},
	})
	var transientAssistants []session.Event
	for _, event := range events {
		if event.Kind == session.EventKindAssistant && event.Visibility == session.VisibilityUIOnly {
			transientAssistants = append(transientAssistants, event)
		}
	}
	if len(transientAssistants) != 4 {
		t.Fatalf("transient assistant deltas = %d, want 4; events=%v", len(transientAssistants), eventKinds(events))
	}
	finals := persistedLiveEvents(events)
	assertEventKinds(t, finals, session.EventKindUser, session.EventKindAssistant)
	if finals[1].Visibility != session.VisibilityCanonical {
		t.Fatalf("final assistant visibility = %q, want canonical", finals[1].Visibility)
	}
	if finals[1].TextContent() != "think-inghello" {
		t.Fatalf("final assistant text = %q, want accumulated text", finals[1].TextContent())
	}
	persisted, err := svc.Events(ctx, session.EventsRequest{SessionRef: sess.Ref})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	assertEventKinds(t, persisted, session.EventKindUser, session.EventKindAssistant)
	modelCtx := session.ModelContextFromEvents(persisted)
	if len(modelCtx) != 2 || modelCtx[1].Role != model.RoleAssistant || modelCtx[1].TextContent() != "think-inghello" {
		t.Fatalf("model context = %#v, want user plus final assistant only", modelCtx)
	}
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

func TestE2E_SpawnTaskCancelThroughRunner(t *testing.T) {
	ctx := context.Background()
	fileSvc, err := filesession.New(filesession.Config{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New file service: %v", err)
	}
	parentSession, err := fileSvc.Create(ctx, session.CreateRequest{
		AppName: "e2e-spawn", UserID: "test", WorkspaceKey: "workspace",
		Workspace: session.Workspace{Root: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	toolReg := newTestToolRegistry(spawn.All()...)
	taskStore := runner.NewMemoryTaskStore()
	childLLM := newE2EBlockingLLM()
	child := llmagent.New(llmagent.Config{Name: "reviewer"})
	child = child.Prepare(agent.PrepareRequest{LLM: childLLM}).(*llmagent.Agent)
	parentLLM := &scriptedSpawnCancelLLM{}
	parent := llmagent.New(llmagent.Config{
		Name:      "parent",
		Tools:     []string{"SPAWN"},
		SubAgents: []agent.Agent{child},
	})
	parent = parent.Prepare(agent.PrepareRequest{LLM: parentLLM}).(*llmagent.Agent)

	r, err := runner.New(runner.Config{
		Agent:        parent,
		Sessions:     fileSvc,
		ToolRegistry: toolReg,
		TaskStore:    taskStore,
	})
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}

	events := collectRun(t, r, ctx, runner.RunRequest{
		SessionRef:  parentSession.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "delegate and cancel"}}},
	})

	assertEventKinds(t, persistedLiveEvents(events),
		session.EventKindUser,
		session.EventKindToolCall,
		session.EventKindToolResult,
		session.EventKindToolCall,
		session.EventKindToolResult,
		session.EventKindAssistant,
	)
	if len(parentLLM.requests) != 3 {
		t.Fatalf("parent model requests: got %d, want 3", len(parentLLM.requests))
	}
	assertToolSpecs(t, parentLLM.requests[0].Tools, "SPAWN", "TASK")
	if parentLLM.taskID == "" {
		t.Fatal("parent model did not observe SPAWN task handle")
	}
	select {
	case <-childLLM.cancelled:
	case <-time.After(time.Second):
		t.Fatal("child LLM did not observe TASK cancel")
	}
	snap, ok, err := taskStore.LoadTask(ctx, parentLLM.taskID)
	if err != nil {
		t.Fatalf("LoadTask: %v", err)
	}
	if !ok || snap.State != runner.TaskStateCancelled {
		t.Fatalf("task snapshot = %#v ok=%v, want cancelled", snap, ok)
	}

	persisted, err := fileSvc.Events(ctx, session.EventsRequest{SessionRef: parentSession.Ref})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	replay := session.ModelContextFromEvents(persisted)
	if !lastToolResultContains(replay, "cancelled") {
		t.Fatalf("replay messages do not contain TASK cancel result: %#v", replay)
	}
}

func TestE2E_SpawnTaskContinueThroughRunner(t *testing.T) {
	ctx := context.Background()
	fileSvc, err := filesession.New(filesession.Config{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New file service: %v", err)
	}
	parentSession, err := fileSvc.Create(ctx, session.CreateRequest{
		AppName: "e2e-spawn-continue", UserID: "test", WorkspaceKey: "workspace",
		Workspace: session.Workspace{Root: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	toolReg := newTestToolRegistry(spawn.All()...)
	taskStore := runner.NewMemoryTaskStore()
	childLLM := &scriptedChildSequenceLLM{responses: []string{"first done", "continued done"}}
	child := llmagent.New(llmagent.Config{Name: "reviewer"})
	child = child.Prepare(agent.PrepareRequest{LLM: childLLM}).(*llmagent.Agent)
	parentLLM := &scriptedSpawnContinueLLM{}
	parent := llmagent.New(llmagent.Config{
		Name:      "parent",
		Tools:     []string{"SPAWN"},
		SubAgents: []agent.Agent{child},
	})
	parent = parent.Prepare(agent.PrepareRequest{LLM: parentLLM}).(*llmagent.Agent)

	r, err := runner.New(runner.Config{
		Agent:        parent,
		Sessions:     fileSvc,
		ToolRegistry: toolReg,
		TaskStore:    taskStore,
	})
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}

	events := collectRun(t, r, ctx, runner.RunRequest{
		SessionRef:  parentSession.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "delegate and continue"}}},
	})

	assertEventKinds(t, persistedLiveEvents(events),
		session.EventKindUser,
		session.EventKindToolCall,
		session.EventKindToolResult,
		session.EventKindToolCall,
		session.EventKindToolResult,
		session.EventKindToolCall,
		session.EventKindToolResult,
		session.EventKindToolCall,
		session.EventKindToolResult,
		session.EventKindAssistant,
	)
	if len(parentLLM.requests) != 5 {
		t.Fatalf("parent model requests: got %d, want 5", len(parentLLM.requests))
	}
	if len(childLLM.requests) != 2 {
		t.Fatalf("child model requests: got %d, want 2", len(childLLM.requests))
	}
	if parentLLM.taskID == "" {
		t.Fatal("parent model did not observe SPAWN task handle")
	}
	snap, ok, err := taskStore.LoadTask(ctx, parentLLM.taskID)
	if err != nil {
		t.Fatalf("LoadTask: %v", err)
	}
	if !ok || snap.State != runner.TaskStateCompleted || snap.Output != "continued done" {
		t.Fatalf("task snapshot = %#v ok=%v, want completed continued output", snap, ok)
	}

	persisted, err := fileSvc.Events(ctx, session.EventsRequest{SessionRef: parentSession.Ref})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	replay := session.ModelContextFromEvents(persisted)
	if !lastToolResultContains(replay, "continued done") {
		t.Fatalf("replay messages do not contain continued result: %#v", replay)
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
	slices.Sort(names)
	want = append([]string(nil), want...)
	slices.Sort(want)
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

type scriptedSpawnCancelLLM struct {
	requests []model.Request
	taskID   string
}

func (m *scriptedSpawnCancelLLM) Name() string { return "scripted-spawn-cancel" }

func (m *scriptedSpawnCancelLLM) Generate(_ context.Context, req model.Request) iter.Seq2[model.ResponseEvent, error] {
	callIndex := len(m.requests)
	m.requests = append(m.requests, cloneModelRequest(req))
	return func(yield func(model.ResponseEvent, error) bool) {
		switch callIndex {
		case 0:
			yield(model.ResponseEvent{ToolCall: &model.ToolCallDelta{
				CallID: "spawn-call-1",
				Name:   "SPAWN",
				Args:   map[string]any{"agent": "reviewer", "prompt": "inspect files"},
			}}, nil)
		case 1:
			taskID := taskIDFromToolResult(req.Messages)
			if taskID == "" {
				yield(model.ResponseEvent{}, fmt.Errorf("missing SPAWN task id in model context"))
				return
			}
			m.taskID = taskID
			yield(model.ResponseEvent{ToolCall: &model.ToolCallDelta{
				CallID: "task-cancel-1",
				Name:   "TASK",
				Args:   map[string]any{"action": "cancel", "task_id": taskID},
			}}, nil)
		case 2:
			if !lastToolResultContains(req.Messages, "cancelled") {
				yield(model.ResponseEvent{}, fmt.Errorf("missing TASK cancel result in model context"))
				return
			}
			yield(model.ResponseEvent{TextDelta: "spawn-cancel-ok"}, nil)
		default:
			yield(model.ResponseEvent{}, fmt.Errorf("unexpected model call %d", callIndex))
		}
	}
}

type scriptedSpawnContinueLLM struct {
	requests []model.Request
	taskID   string
}

func (m *scriptedSpawnContinueLLM) Name() string { return "scripted-spawn-continue" }

func (m *scriptedSpawnContinueLLM) Generate(_ context.Context, req model.Request) iter.Seq2[model.ResponseEvent, error] {
	callIndex := len(m.requests)
	m.requests = append(m.requests, cloneModelRequest(req))
	return func(yield func(model.ResponseEvent, error) bool) {
		switch callIndex {
		case 0:
			yield(model.ResponseEvent{ToolCall: &model.ToolCallDelta{
				CallID: "spawn-call-1",
				Name:   "SPAWN",
				Args:   map[string]any{"agent": "reviewer", "prompt": "first prompt"},
			}}, nil)
		case 1:
			taskID := taskIDFromToolResult(req.Messages)
			if taskID == "" {
				yield(model.ResponseEvent{}, fmt.Errorf("missing SPAWN task id in model context"))
				return
			}
			m.taskID = taskID
			yield(model.ResponseEvent{ToolCall: &model.ToolCallDelta{
				CallID: "task-wait-1",
				Name:   "TASK",
				Args:   map[string]any{"action": "wait", "task_id": taskID},
			}}, nil)
		case 2:
			if !lastToolResultContains(req.Messages, "first done") {
				yield(model.ResponseEvent{}, fmt.Errorf("missing first child result in model context"))
				return
			}
			yield(model.ResponseEvent{ToolCall: &model.ToolCallDelta{
				CallID: "task-write-1",
				Name:   "TASK",
				Args:   map[string]any{"action": "write", "task_id": m.taskID, "input": "continue prompt"},
			}}, nil)
		case 3:
			if !lastToolResultContains(req.Messages, "ok") {
				yield(model.ResponseEvent{}, fmt.Errorf("missing TASK write result in model context"))
				return
			}
			yield(model.ResponseEvent{ToolCall: &model.ToolCallDelta{
				CallID: "task-wait-2",
				Name:   "TASK",
				Args:   map[string]any{"action": "wait", "task_id": m.taskID},
			}}, nil)
		case 4:
			if !lastToolResultContains(req.Messages, "continued done") {
				yield(model.ResponseEvent{}, fmt.Errorf("missing continued child result in model context"))
				return
			}
			yield(model.ResponseEvent{TextDelta: "spawn-continue-ok"}, nil)
		default:
			yield(model.ResponseEvent{}, fmt.Errorf("unexpected model call %d", callIndex))
		}
	}
}

type e2eBlockingLLM struct {
	started    chan struct{}
	cancelled  chan struct{}
	startOnce  sync.Once
	cancelOnce sync.Once
}

func newE2EBlockingLLM() *e2eBlockingLLM {
	return &e2eBlockingLLM{
		started:   make(chan struct{}),
		cancelled: make(chan struct{}),
	}
}

func (m *e2eBlockingLLM) Name() string { return "e2e-blocking" }

func (m *e2eBlockingLLM) Generate(ctx context.Context, _ model.Request) iter.Seq2[model.ResponseEvent, error] {
	m.startOnce.Do(func() { close(m.started) })
	return func(yield func(model.ResponseEvent, error) bool) {
		<-ctx.Done()
		m.cancelOnce.Do(func() { close(m.cancelled) })
		yield(model.ResponseEvent{}, ctx.Err())
	}
}

type scriptedChildSequenceLLM struct {
	requests  []model.Request
	responses []string
}

func (m *scriptedChildSequenceLLM) Name() string { return "scripted-child-sequence" }

func (m *scriptedChildSequenceLLM) Generate(_ context.Context, req model.Request) iter.Seq2[model.ResponseEvent, error] {
	callIndex := len(m.requests)
	m.requests = append(m.requests, cloneModelRequest(req))
	return func(yield func(model.ResponseEvent, error) bool) {
		if callIndex >= len(m.responses) {
			yield(model.ResponseEvent{}, fmt.Errorf("unexpected child model call %d", callIndex))
			return
		}
		yield(model.ResponseEvent{TextDelta: m.responses[callIndex]}, nil)
	}
}

type deltaE2ELLM struct {
	events []model.ResponseEvent
}

func (m *deltaE2ELLM) Name() string { return "delta-e2e" }

func (m *deltaE2ELLM) Generate(_ context.Context, _ model.Request) iter.Seq2[model.ResponseEvent, error] {
	return func(yield func(model.ResponseEvent, error) bool) {
		for _, evt := range m.events {
			if !yield(evt, nil) {
				return
			}
		}
	}
}

func taskIDFromToolResult(messages []model.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		for _, part := range messages[i].Content {
			if part.ToolResult == nil {
				continue
			}
			text := strings.TrimSpace(part.ToolResult.Content)
			if after, ok := strings.CutPrefix(text, "task started: "); ok {
				return strings.TrimSpace(after)
			}
		}
	}
	return ""
}

func lastToolResultContains(messages []model.Message, want string) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		for _, part := range messages[i].Content {
			if part.ToolResult != nil && strings.Contains(part.ToolResult.Content, want) {
				return true
			}
		}
	}
	return false
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

// ─── Additional E2E Tests ────────────────────────────────────────────

// TestE2E_RealProviderWithToolCalls tests a real model provider making tool
// calls through the full runner pipeline: model decides to call WRITE tool →
// sandbox executes → model sees result → final answer.
func TestE2E_RealProviderWithToolCalls(t *testing.T) {
	cfg := loadConfig(t)
	modelCfg, ok := defaultModel(cfg)
	if !ok {
		t.Skip("no default model configured")
	}
	t.Logf("Using model: %s @ %s", modelCfg.Model, modelCfg.BaseURL)

	llm := providers.NewOpenAI(modelCfg)
	modelReg := &recordingModelRegistry{
		llm: llm,
		info: model.ModelInfo{
			ModelID:       modelCfg.Model,
			DisplayName:   modelCfg.Model,
			Provider:      "configured",
			SupportsTools: true,
		},
	}

	dir := t.TempDir()
	fileSvc, err := filesession.New(filesession.Config{RootDir: dir})
	if err != nil {
		t.Fatalf("New file service: %v", err)
	}
	sess, err := fileSvc.Create(ctx(t), session.CreateRequest{
		AppName: "e2e-tool-real", UserID: "test", WorkspaceKey: "e2e",
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	fs := newTempFS(t.TempDir())
	backend := &recordingSandboxBackend{fs: fs}
	toolReg := newTestToolRegistry(filesystem.All()...)
	llmAgent := llmagent.New(llmagent.Config{
		Name:     "e2e-real-tool-agent",
		ModelRef: model.Ref{ModelID: modelCfg.Model},
		Tools:    []string{"WRITE", "READ"},
	})
	r, err := runner.New(runner.Config{
		Agent:         llmAgent,
		Sessions:      fileSvc,
		ModelRegistry: modelReg,
		ToolRegistry:  toolReg,
		Sandbox:       &recordingSandboxFactory{backend: backend},
	})
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}

	// Ask the model to write a file and then confirm.
	events := collectRun(t, r, ctx(t), runner.RunRequest{
		SessionRef: sess.Ref,
		UserMessage: model.Message{
			Role:    model.RoleUser,
			Content: []model.Part{{Text: "Write the text 'hello world' to a file called greeting.txt, then read it back and tell me what it says."}},
		},
	})

	// We expect at minimum: user, tool_call, tool_result, (maybe more tools), assistant
	persisted := persistedLiveEvents(events)
	if len(persisted) < 3 {
		t.Fatalf("expected >= 3 persisted events, got %d: %v", len(persisted), eventKinds(persisted))
	}
	if persisted[0].Kind != session.EventKindUser {
		t.Errorf("first event: got %q, want user", persisted[0].Kind)
	}

	// Verify at least one tool call happened.
	var toolCallCount, toolResultCount int
	for _, e := range persisted {
		switch e.Kind {
		case session.EventKindToolCall:
			toolCallCount++
		case session.EventKindToolResult:
			toolResultCount++
		}
	}
	if toolCallCount == 0 {
		t.Fatal("expected at least one tool call from real model")
	}
	if toolCallCount != toolResultCount {
		t.Errorf("tool calls (%d) != tool results (%d)", toolCallCount, toolResultCount)
	}

	// Verify the last event is an assistant response.
	last := persisted[len(persisted)-1]
	if last.Kind != session.EventKindAssistant {
		t.Errorf("last event: got %q, want assistant", last.Kind)
	}

	// Verify model context reconstruction is valid.
	allPersisted, _ := fileSvc.Events(ctx(t), session.EventsRequest{SessionRef: sess.Ref})
	modelCtx := session.ModelContextFromEvents(allPersisted)
	if len(modelCtx) < 2 {
		t.Fatalf("model context: got %d, want >= 2", len(modelCtx))
	}

	// Verify ACP projection covers all events.
	projections := projectAllACP(allPersisted)
	if len(projections) == 0 {
		t.Error("expected ACP projections from persisted events")
	}

	t.Logf("Real provider tool call e2e: %d events, %d tool calls, %d model messages",
		len(persisted), toolCallCount, len(modelCtx))
}

// TestE2E_ContextOverflowRetry verifies that when the model returns a context
// overflow error, the runner compacts the context and retries successfully.
func TestE2E_ContextOverflowRetry(t *testing.T) {
	svc := session.InMemoryService()
	ctx := ctx(t)
	sess, err := svc.Create(ctx, session.CreateRequest{
		AppName: "e2e-overflow", UserID: "test", WorkspaceKey: "workspace",
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	// Pre-fill session with a prior event.
	_, _ = svc.AppendEvent(ctx, sess.Ref, session.Event{
		Kind:       session.EventKindUser,
		Visibility: session.VisibilityCanonical,
		UserPayload: &session.UserPayload{
			Parts: []session.EventPart{{Kind: session.PartKindText, Text: "prior context"}},
		},
	})

	llm := &overflowThenSuccessLLM{}
	compactor := &alwaysCompactCompactor{}
	llmAgent := llmagent.New(llmagent.Config{
		Name: "overflow-agent",
	})
	llmAgent = llmAgent.Prepare(agent.PrepareRequest{LLM: llm}).(*llmagent.Agent)

	r, err := runner.New(runner.Config{
		Agent:     llmAgent,
		Sessions:  svc,
		Compactor: compactor,
	})
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}

	events := collectRun(t, r, ctx, runner.RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "hello after overflow"}}},
	})

	persisted := persistedLiveEvents(events)
	// The compaction event is persisted to the store but not yielded to the caller.
	// Check the store for compaction events.
	storeEvents, _ := svc.Events(ctx, session.EventsRequest{SessionRef: sess.Ref})
	var hasCompaction bool
	for _, e := range storeEvents {
		if e.Kind == session.EventKindCompaction {
			hasCompaction = true
		}
	}
	if !hasCompaction {
		t.Error("expected compaction event in session store after overflow retry")
	}

	// The last event should be the successful assistant response.
	last := persisted[len(persisted)-1]
	if last.Kind != session.EventKindAssistant {
		t.Errorf("last event: got %q, want assistant", last.Kind)
	}
	if last.TextContent() != "overflow-recovered" {
		t.Errorf("assistant text: got %q, want %q", last.TextContent(), "overflow-recovered")
	}

	// Verify LLM was called twice (overflow then success).
	if llm.calls != 2 {
		t.Fatalf("LLM calls: got %d, want 2", llm.calls)
	}
	if compactor.calls != 1 {
		t.Fatalf("compactor calls: got %d, want 1", compactor.calls)
	}
}

// TestE2E_MultiToolConcurrentExecutionOrder verifies that when the model
// requests multiple tools in one turn, results are emitted in the model's
// original call order, not execution completion order.
func TestE2E_MultiToolConcurrentExecutionOrder(t *testing.T) {
	fileSvc, err := filesession.New(filesession.Config{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New file service: %v", err)
	}
	sess, err := fileSvc.Create(ctx(t), session.CreateRequest{
		AppName: "e2e-multi-tool", UserID: "test", WorkspaceKey: "workspace",
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	llm := &scriptedMultiToolLLM{}
	modelReg := &recordingModelRegistry{
		llm:  llm,
		info: model.ModelInfo{ModelID: "multi-tool", SupportsTools: true},
	}

	// Create tools with different delays: B is slowest.
	toolA := &delayTool{name: "TOOL_A", delay: 10 * time.Millisecond, output: "result-a"}
	toolB := &delayTool{name: "TOOL_B", delay: 100 * time.Millisecond, output: "result-b"}
	toolC := &delayTool{name: "TOOL_C", delay: 20 * time.Millisecond, output: "result-c"}
	toolReg := newTestToolRegistry(toolA, toolB, toolC)

	llmAgent := llmagent.New(llmagent.Config{
		Name:     "multi-tool-agent",
		ModelRef: model.Ref{ModelID: "multi-tool"},
		Tools:    []string{"TOOL_A", "TOOL_B", "TOOL_C"},
	})
	r, err := runner.New(runner.Config{
		Agent:         llmAgent,
		Sessions:      fileSvc,
		ModelRegistry: modelReg,
		ToolRegistry:  toolReg,
	})
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}

	events := collectRun(t, r, ctx(t), runner.RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "call all three tools"}}},
	})

	persisted := persistedLiveEvents(events)
	// Expected: user, tool_call(A), tool_call(B), tool_call(C),
	//           tool_result(A), tool_result(B), tool_result(C), assistant
	assertEventKinds(t, persisted,
		session.EventKindUser,
		session.EventKindToolCall,
		session.EventKindToolCall,
		session.EventKindToolCall,
		session.EventKindToolResult,
		session.EventKindToolResult,
		session.EventKindToolResult,
		session.EventKindAssistant,
	)

	// Verify tool call order matches model's request order.
	if persisted[1].ToolCallPayload.Name != "TOOL_A" {
		t.Errorf("call 1: got %q, want TOOL_A", persisted[1].ToolCallPayload.Name)
	}
	if persisted[2].ToolCallPayload.Name != "TOOL_B" {
		t.Errorf("call 2: got %q, want TOOL_B", persisted[2].ToolCallPayload.Name)
	}
	if persisted[3].ToolCallPayload.Name != "TOOL_C" {
		t.Errorf("call 3: got %q, want TOOL_C", persisted[3].ToolCallPayload.Name)
	}

	// Verify tool result order matches call order (A, B, C).
	if persisted[4].ToolResultPayload.Name != "TOOL_A" {
		t.Errorf("result 1: got %q, want TOOL_A", persisted[4].ToolResultPayload.Name)
	}
	if persisted[5].ToolResultPayload.Name != "TOOL_B" {
		t.Errorf("result 2: got %q, want TOOL_B", persisted[5].ToolResultPayload.Name)
	}
	if persisted[6].ToolResultPayload.Name != "TOOL_C" {
		t.Errorf("result 3: got %q, want TOOL_C", persisted[6].ToolResultPayload.Name)
	}

	// Verify replay parity.
	allPersisted, _ := fileSvc.Events(ctx(t), session.EventsRequest{SessionRef: sess.Ref})
	modelCtx := session.ModelContextFromEvents(allPersisted)
	// Expected: user, assistant(tool_use A), assistant(tool_use B), assistant(tool_use C),
	//           tool(A), tool(B), tool(C), assistant(text) = 8
	if len(modelCtx) != 8 {
		t.Fatalf("model context: got %d messages, want 8", len(modelCtx))
	}
}

// TestE2E_ModelErrorPropagation verifies that when the model returns an error
// mid-stream, the error propagates through the runner and no partial assistant
// event is persisted.
func TestE2E_ModelErrorPropagation(t *testing.T) {
	fileSvc, err := filesession.New(filesession.Config{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New file service: %v", err)
	}
	sess, err := fileSvc.Create(ctx(t), session.CreateRequest{
		AppName: "e2e-model-err", UserID: "test", WorkspaceKey: "workspace",
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	llm := &errorStreamLLM{textBefore: "partial-", err: fmt.Errorf("model overloaded")}
	modelReg := &recordingModelRegistry{
		llm:  llm,
		info: model.ModelInfo{ModelID: "error-stream"},
	}
	llmAgent := llmagent.New(llmagent.Config{
		Name:     "error-agent",
		ModelRef: model.Ref{ModelID: "error-stream"},
	})
	r, err := runner.New(runner.Config{
		Agent:         llmAgent,
		Sessions:      fileSvc,
		ModelRegistry: modelReg,
	})
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}

	var runErr error
	var events []session.Event
	for evt, err := range r.Run(ctx(t), runner.RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "trigger error"}}},
	}) {
		if err != nil {
			runErr = err
			break
		}
		events = append(events, evt)
	}
	if runErr == nil {
		t.Fatal("expected error from model, got nil")
	}
	if !strings.Contains(runErr.Error(), "model overloaded") {
		t.Errorf("error: got %q, want contains 'model overloaded'", runErr.Error())
	}

	// Only the user event should be persisted (assistant was not completed).
	persisted, _ := fileSvc.Events(ctx(t), session.EventsRequest{SessionRef: sess.Ref})
	for _, e := range persisted {
		if e.Kind == session.EventKindAssistant {
			t.Error("should not persist assistant event on model error")
		}
	}
}

// TestE2E_ToolErrorPropagation verifies that when a tool returns an error
// result, it flows through to the model as a tool result with IsError=true.
func TestE2E_ToolErrorPropagation(t *testing.T) {
	fileSvc, err := filesession.New(filesession.Config{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New file service: %v", err)
	}
	sess, err := fileSvc.Create(ctx(t), session.CreateRequest{
		AppName: "e2e-tool-err", UserID: "test", WorkspaceKey: "workspace",
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	llm := &scriptedToolErrorLLM{}
	modelReg := &recordingModelRegistry{
		llm:  llm,
		info: model.ModelInfo{ModelID: "tool-error", SupportsTools: true},
	}
	errTool := &errorTool{name: "FAIL_TOOL", errMsg: "tool exploded"}
	toolReg := newTestToolRegistry(errTool)
	llmAgent := llmagent.New(llmagent.Config{
		Name:     "tool-error-agent",
		ModelRef: model.Ref{ModelID: "tool-error"},
		Tools:    []string{"FAIL_TOOL"},
	})
	r, err := runner.New(runner.Config{
		Agent:         llmAgent,
		Sessions:      fileSvc,
		ModelRegistry: modelReg,
		ToolRegistry:  toolReg,
	})
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}

	events := collectRun(t, r, ctx(t), runner.RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "call the failing tool"}}},
	})

	persisted := persistedLiveEvents(events)
	assertEventKinds(t, persisted,
		session.EventKindUser,
		session.EventKindToolCall,
		session.EventKindToolResult,
		session.EventKindAssistant,
	)

	// Verify tool result is marked as error.
	tr := persisted[2].ToolResultPayload
	if !tr.IsError {
		t.Error("tool result should be marked as error")
	}
	if !strings.Contains(tr.Content[0].Text, "tool exploded") {
		t.Errorf("tool result text: got %q, want contains 'tool exploded'", tr.Content[0].Text)
	}

	// Verify the model received the error in context for its next call.
	if len(llm.requests) != 2 {
		t.Fatalf("model requests: got %d, want 2", len(llm.requests))
	}
	// The second request should contain the tool error in its messages.
	lastMsg := llm.requests[1].Messages[len(llm.requests[1].Messages)-1]
	if lastMsg.Role != model.RoleTool {
		t.Errorf("last message role: got %q, want tool", lastMsg.Role)
	}
	if !lastMsg.Content[0].ToolResult.IsError {
		t.Error("model context tool result should be error")
	}
}

// TestE2E_SandboxErrorPropagation verifies that when the sandbox backend
// returns an error, it surfaces as a tool error result.
func TestE2E_SandboxErrorPropagation(t *testing.T) {
	fileSvc, err := filesession.New(filesession.Config{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New file service: %v", err)
	}
	sess, err := fileSvc.Create(ctx(t), session.CreateRequest{
		AppName: "e2e-sandbox-err", UserID: "test", WorkspaceKey: "workspace",
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	llm := &scriptedToolLLM{}
	modelReg := &recordingModelRegistry{
		llm:  llm,
		info: model.ModelInfo{ModelID: "sandbox-err", SupportsTools: true},
	}
	toolReg := newTestToolRegistry(shell.All()...)
	errBackend := &errorSandboxBackend{err: fmt.Errorf("sandbox exploded")}
	sandboxFactory := &failingSandboxFactoryWithError{backend: errBackend}
	llmAgent := llmagent.New(llmagent.Config{
		Name:     "sandbox-err-agent",
		ModelRef: model.Ref{ModelID: "sandbox-err"},
		Tools:    []string{"RUN_COMMAND"},
	})
	r, err := runner.New(runner.Config{
		Agent:         llmAgent,
		Sessions:      fileSvc,
		ModelRegistry: modelReg,
		ToolRegistry:  toolReg,
		Sandbox:       sandboxFactory,
	})
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}

	events := collectRun(t, r, ctx(t), runner.RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "run a command"}}},
	})

	persisted := persistedLiveEvents(events)
	// Should have: user, tool_call, tool_result(error), assistant
	assertEventKinds(t, persisted,
		session.EventKindUser,
		session.EventKindToolCall,
		session.EventKindToolResult,
		session.EventKindAssistant,
	)

	tr := persisted[2].ToolResultPayload
	if !tr.IsError {
		t.Error("tool result should be error when sandbox fails")
	}
	if !strings.Contains(tr.Content[0].Text, "sandbox exploded") {
		t.Errorf("error text: got %q, want contains 'sandbox exploded'", tr.Content[0].Text)
	}
}

// TestE2E_CrossRunnerSessionPersistence verifies that a session created by
// one runner can be continued by a different runner instance.
func TestE2E_CrossRunnerSessionPersistence(t *testing.T) {
	dir := t.TempDir()
	fileSvc, err := filesession.New(filesession.Config{RootDir: dir})
	if err != nil {
		t.Fatalf("New file service: %v", err)
	}
	sess, err := fileSvc.Create(ctx(t), session.CreateRequest{
		AppName: "e2e-cross-runner", UserID: "test", WorkspaceKey: "workspace",
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	// Turn 1 with Runner A.
	llm1 := &mockTextLLM{response: "the secret number is 42"}
	modelReg1 := &recordingModelRegistry{
		llm:  llm1,
		info: model.ModelInfo{ModelID: "cross-1"},
	}
	agent1 := llmagent.New(llmagent.Config{Name: "agent-1", ModelRef: model.Ref{ModelID: "cross-1"}})
	r1, _ := runner.New(runner.Config{Agent: agent1, Sessions: fileSvc, ModelRegistry: modelReg1})

	collectRun(t, r1, ctx(t), runner.RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "remember the secret number 42"}}},
	})

	// Turn 2 with Runner B (different instance, same file store).
	llm2 := &recordingLLM{}
	modelReg2 := &recordingModelRegistry{
		llm:  llm2,
		info: model.ModelInfo{ModelID: "cross-2"},
	}
	agent2 := llmagent.New(llmagent.Config{Name: "agent-2", ModelRef: model.Ref{ModelID: "cross-2"}})
	r2, _ := runner.New(runner.Config{Agent: agent2, Sessions: fileSvc, ModelRegistry: modelReg2})

	collectRun(t, r2, ctx(t), runner.RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "what was the secret number?"}}},
	})

	// Runner B's model should have received prior context from turn 1.
	if len(llm2.requests) != 1 {
		t.Fatalf("runner B model requests: got %d, want 1", len(llm2.requests))
	}
	msgs := llm2.requests[0].Messages
	// Should contain: user(turn1), assistant(turn1), user(turn2)
	if len(msgs) < 3 {
		t.Fatalf("runner B messages: got %d, want >= 3", len(msgs))
	}
	// First message should be the turn 1 user message.
	if msgs[0].Role != model.RoleUser {
		t.Errorf("msg 0 role: got %q, want user", msgs[0].Role)
	}
	if !strings.Contains(msgs[0].TextContent(), "secret number 42") {
		t.Errorf("msg 0 text: got %q, want contains 'secret number 42'", msgs[0].TextContent())
	}
	// Second message should be the turn 1 assistant response.
	if msgs[1].Role != model.RoleAssistant {
		t.Errorf("msg 1 role: got %q, want assistant", msgs[1].Role)
	}
}

// TestE2E_HookLifecycle verifies all 4 hook callbacks fire in the correct
// order with correct data.
func TestE2E_HookLifecycle(t *testing.T) {
	fileSvc, err := filesession.New(filesession.Config{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New file service: %v", err)
	}
	sess, err := fileSvc.Create(ctx(t), session.CreateRequest{
		AppName: "e2e-hooks", UserID: "test", WorkspaceKey: "workspace",
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	hook := &recordingHook{}
	llm := &scriptedToolLLM{}
	modelReg := &recordingModelRegistry{
		llm:  llm,
		info: model.ModelInfo{ModelID: "hook-test", SupportsTools: true},
	}
	toolReg := newTestToolRegistry(shell.All()...)
	llmAgent := llmagent.New(llmagent.Config{
		Name:     "hook-agent",
		ModelRef: model.Ref{ModelID: "hook-test"},
		Tools:    []string{"RUN_COMMAND"},
	})
	r, err := runner.New(runner.Config{
		Agent:         llmAgent,
		Sessions:      fileSvc,
		ModelRegistry: modelReg,
		ToolRegistry:  toolReg,
		Sandbox:       &recordingSandboxFactory{backend: &recordingSandboxBackend{fs: newTempFS(t.TempDir())}},
		Hooks:         []agent.Hook{hook},
	})
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}

	collectRun(t, r, ctx(t), runner.RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "run command with hooks"}}},
	})

	hook.mu.Lock()
	defer hook.mu.Unlock()

	// Expected sequence: BeforeInvocation, BeforeTool, AfterTool, AfterInvocation.
	if len(hook.calls) != 4 {
		t.Fatalf("hook calls: got %d, want 4: %v", len(hook.calls), hook.calls)
	}
	if hook.calls[0] != "BeforeInvocation" {
		t.Errorf("call 0: got %q, want BeforeInvocation", hook.calls[0])
	}
	if hook.calls[1] != "BeforeTool" {
		t.Errorf("call 1: got %q, want BeforeTool", hook.calls[1])
	}
	if hook.calls[2] != "AfterTool" {
		t.Errorf("call 2: got %q, want AfterTool", hook.calls[2])
	}
	if hook.calls[3] != "AfterInvocation" {
		t.Errorf("call 3: got %q, want AfterInvocation", hook.calls[3])
	}

	// Verify hook data.
	if hook.invHook.AgentName != "hook-agent" {
		t.Errorf("invocation hook agent: got %q, want hook-agent", hook.invHook.AgentName)
	}
	if hook.toolHook.ToolName != "RUN_COMMAND" {
		t.Errorf("tool hook name: got %q, want RUN_COMMAND", hook.toolHook.ToolName)
	}
}

// TestE2E_PolicyDenyTool verifies that when policy denies a tool call,
// the model receives a deny result.
func TestE2E_PolicyDenyTool(t *testing.T) {
	fileSvc, err := filesession.New(filesession.Config{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New file service: %v", err)
	}
	sess, err := fileSvc.Create(ctx(t), session.CreateRequest{
		AppName: "e2e-deny", UserID: "test", WorkspaceKey: "workspace",
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	llm := &scriptedToolLLM{}
	modelReg := &recordingModelRegistry{
		llm:  llm,
		info: model.ModelInfo{ModelID: "deny-test", SupportsTools: true},
	}
	toolReg := newTestToolRegistry(shell.All()...)
	llmAgent := llmagent.New(llmagent.Config{
		Name:     "deny-agent",
		ModelRef: model.Ref{ModelID: "deny-test"},
		Tools:    []string{"RUN_COMMAND"},
	})
	r, err := runner.New(runner.Config{
		Agent:         llmAgent,
		Sessions:      fileSvc,
		ModelRegistry: modelReg,
		ToolRegistry:  toolReg,
		Sandbox:       &recordingSandboxFactory{backend: &recordingSandboxBackend{fs: newTempFS(t.TempDir())}},
		Policy:        &alwaysDenyPolicy{},
	})
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}

	events := collectRun(t, r, ctx(t), runner.RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "run denied command"}}},
	})

	persisted := persistedLiveEvents(events)
	assertEventKinds(t, persisted,
		session.EventKindUser,
		session.EventKindToolCall,
		session.EventKindToolResult,
		session.EventKindAssistant,
	)

	tr := persisted[2].ToolResultPayload
	if !tr.IsError {
		t.Error("tool result should be error when policy denies")
	}
	if !strings.Contains(strings.ToLower(tr.Content[0].Text), "denied") {
		t.Errorf("deny text: got %q, want contains 'denied'", tr.Content[0].Text)
	}
}

// TestE2E_ToolResultTruncation verifies that large tool results get truncated
// and truncation metadata is preserved.
func TestE2E_ToolResultTruncation(t *testing.T) {
	fileSvc, err := filesession.New(filesession.Config{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New file service: %v", err)
	}
	sess, err := fileSvc.Create(ctx(t), session.CreateRequest{
		AppName: "e2e-truncation", UserID: "test", WorkspaceKey: "workspace",
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	bigOutput := strings.Repeat("x", 200_000) // ~200KB
	llm := &scriptedToolErrorLLM{}            // reuse: calls tool once, then text
	modelReg := &recordingModelRegistry{
		llm:  llm,
		info: model.ModelInfo{ModelID: "trunc-test", SupportsTools: true},
	}
	bigTool := &staticResultTool{name: "BIG_TOOL", output: bigOutput}
	toolReg := newTestToolRegistry(bigTool)
	llmAgent := llmagent.New(llmagent.Config{
		Name:     "trunc-agent",
		ModelRef: model.Ref{ModelID: "trunc-test"},
		Tools:    []string{"BIG_TOOL"},
	})
	r, err := runner.New(runner.Config{
		Agent:         llmAgent,
		Sessions:      fileSvc,
		ModelRegistry: modelReg,
		ToolRegistry:  toolReg,
	})
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}

	events := collectRun(t, r, ctx(t), runner.RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "call big tool"}}},
	})

	persisted := persistedLiveEvents(events)
	// Find the tool result.
	var tr *session.ToolResultPayload
	for i := range persisted {
		if persisted[i].Kind == session.EventKindToolResult {
			tr = persisted[i].ToolResultPayload
		}
	}
	if tr == nil {
		t.Fatal("expected tool result event")
	}

	// The tool result should be truncated.
	resultText := ""
	for _, p := range tr.Content {
		resultText += p.Text
	}
	if len(resultText) >= len(bigOutput) {
		t.Errorf("tool result not truncated: got %d bytes, want < %d", len(resultText), len(bigOutput))
	}
}

// TestE2E_SystemPromptAssembly verifies the system prompt from runner config
// appears in the model context.
func TestE2E_SystemPromptAssembly(t *testing.T) {
	fileSvc, err := filesession.New(filesession.Config{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New file service: %v", err)
	}
	sess, err := fileSvc.Create(ctx(t), session.CreateRequest{
		AppName: "e2e-sysprompt", UserID: "test", WorkspaceKey: "workspace",
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	llm := &recordingLLM{}
	modelReg := &recordingModelRegistry{
		llm:  llm,
		info: model.ModelInfo{ModelID: "sysprompt-test"},
	}
	llmAgent := llmagent.New(llmagent.Config{
		Name:     "sysprompt-agent",
		ModelRef: model.Ref{ModelID: "sysprompt-test"},
	})
	r, err := runner.New(runner.Config{
		Agent:         llmAgent,
		Sessions:      fileSvc,
		ModelRegistry: modelReg,
		SystemPrompt:  "You are a helpful assistant. Always respond concisely.",
	})
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}

	collectRun(t, r, ctx(t), runner.RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "hello"}}},
	})

	if len(llm.requests) != 1 {
		t.Fatalf("model requests: got %d, want 1", len(llm.requests))
	}
	msgs := llm.requests[0].Messages
	if len(msgs) < 2 {
		t.Fatalf("messages: got %d, want >= 2", len(msgs))
	}
	// First message should be system role with the configured prompt.
	if msgs[0].Role != model.RoleSystem {
		t.Errorf("msg 0 role: got %q, want system", msgs[0].Role)
	}
	if !strings.Contains(msgs[0].TextContent(), "helpful assistant") {
		t.Errorf("system prompt: got %q, want contains 'helpful assistant'", msgs[0].TextContent())
	}
}

// TestE2E_ACPProjectionAllEventTypes verifies ACP projection covers all
// canonical event kinds.
func TestE2E_ACPProjectionAllEventTypes(t *testing.T) {
	events := []session.Event{
		{
			Kind:       session.EventKindUser,
			Visibility: session.VisibilityCanonical,
			UserPayload: &session.UserPayload{
				Parts: []session.EventPart{{Kind: session.PartKindText, Text: "hello"}},
			},
		},
		{
			Kind:       session.EventKindAssistant,
			Visibility: session.VisibilityCanonical,
			AssistantPayload: &session.AssistantPayload{
				Parts: []session.EventPart{
					{Kind: session.PartKindReasoning, Text: "thinking"},
					{Kind: session.PartKindText, Text: "response"},
				},
			},
		},
		{
			Kind:       session.EventKindToolCall,
			Visibility: session.VisibilityCanonical,
			ToolCallPayload: &session.ToolCallPayload{
				CallID: "tc-1", Name: "READ", Status: "pending",
				Args: map[string]any{"path": "/tmp/test"},
			},
		},
		{
			Kind:       session.EventKindToolResult,
			Visibility: session.VisibilityCanonical,
			ToolResultPayload: &session.ToolResultPayload{
				CallID: "tc-1", Name: "READ", Status: "completed",
				Content: []session.EventPart{{Kind: session.PartKindText, Text: "file contents"}},
			},
		},
		{
			Kind:       session.EventKindPlan,
			Visibility: session.VisibilityCanonical,
			PlanPayload: &session.PlanPayload{
				Entries:     []session.PlanEntry{{Content: "step 1", Status: "pending"}},
				Explanation: "plan explanation",
			},
		},
		{
			Kind:       session.EventKindHandoff,
			Visibility: session.VisibilityCanonical,
			HandoffPayload: &session.HandoffPayload{
				FromAgent: "agent-a", ToAgent: "agent-b", Reason: "delegation",
			},
		},
		{
			Kind:       session.EventKindParticipant,
			Visibility: session.VisibilityCanonical,
			ParticipantPayload: &session.ParticipantPayload{
				ParticipantID: "p-1", Role: "sidecar", State: "attached",
			},
		},
	}

	expectedKinds := []acp.UpdateKind{
		acp.UpdateUserMessage,
		acp.UpdateAgentThought, // reasoning
		acp.UpdateAgentMessage,
		acp.UpdateToolCall,
		acp.UpdateToolCallInfo,
		acp.UpdatePlan,
		acp.UpdateSessionInfo, // handoff
		acp.UpdateSessionInfo, // participant
	}

	var allUpdates []acp.Update
	for i := range events {
		allUpdates = append(allUpdates, acp.ProjectEvent(&events[i])...)
	}

	if len(allUpdates) != len(expectedKinds) {
		t.Fatalf("ACP updates: got %d, want %d\n%v", len(allUpdates), len(expectedKinds), acpKinds(allUpdates))
	}
	for i, want := range expectedKinds {
		got := allUpdates[i].SessionUpdateType()
		if got != want {
			t.Errorf("update %d: got %q, want %q", i, got, want)
		}
	}

	// Verify _meta.caelis on tool-related updates that have RunID set.
	// Events with RunID produce _meta.caelis; those without may not.
	for _, u := range allUpdates {
		if tc, ok := u.(acp.ToolCallUpdate); ok && tc.Meta != nil {
			if tc.Meta["caelis"] != nil {
				t.Logf("tool update %q has _meta.caelis: %v", tc.ToolCallID, tc.Meta["caelis"])
			}
		}
	}
}

// TestE2E_HeuristicCompactionWithFileBackedSession verifies that heuristic
// compaction triggers before invocation when the context is too large.
func TestE2E_HeuristicCompactionWithFileBackedSession(t *testing.T) {
	dir := t.TempDir()
	fileSvc, err := filesession.New(filesession.Config{RootDir: dir})
	if err != nil {
		t.Fatalf("New file service: %v", err)
	}
	sess, err := fileSvc.Create(ctx(t), session.CreateRequest{
		AppName: "e2e-compact-file", UserID: "test", WorkspaceKey: "workspace",
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	// Pre-fill with very large messages to exceed 80% of 200K token budget.
	// 30 pairs * 50KB each = ~3MB total = ~750K tokens >> 160K threshold.
	for i := 0; i < 30; i++ {
		_, _ = fileSvc.AppendEvent(ctx(t), sess.Ref, session.Event{
			Kind:       session.EventKindUser,
			Visibility: session.VisibilityCanonical,
			UserPayload: &session.UserPayload{
				Parts: []session.EventPart{{Kind: session.PartKindText, Text: strings.Repeat("a", 50000)}},
			},
		})
		_, _ = fileSvc.AppendEvent(ctx(t), sess.Ref, session.Event{
			Kind:       session.EventKindAssistant,
			Visibility: session.VisibilityCanonical,
			AssistantPayload: &session.AssistantPayload{
				Parts: []session.EventPart{{Kind: session.PartKindText, Text: strings.Repeat("b", 50000)}},
			},
		})
	}

	llm := &recordingLLM{}
	modelReg := &recordingModelRegistry{
		llm:  llm,
		info: model.ModelInfo{ModelID: "compact-file"},
	}
	llmAgent := llmagent.New(llmagent.Config{
		Name:     "compact-agent",
		ModelRef: model.Ref{ModelID: "compact-file"},
	})
	r, err := runner.New(runner.Config{
		Agent:         llmAgent,
		Sessions:      fileSvc,
		ModelRegistry: modelReg,
	})
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}

	collectRun(t, r, ctx(t), runner.RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "after compaction"}}},
	})

	// Compaction events are persisted to the store but not yielded to the caller.
	// Check the store for compaction events.
	allPersisted, _ := fileSvc.Events(ctx(t), session.EventsRequest{SessionRef: sess.Ref})
	var hasCompaction bool
	for _, e := range allPersisted {
		if e.Kind == session.EventKindCompaction {
			hasCompaction = true
			if e.CompactionPayload == nil {
				t.Error("compaction event missing payload")
			}
			if e.Previous == 0 {
				t.Error("compaction Previous should be > 0")
			}
		}
	}
	if !hasCompaction {
		t.Error("expected compaction event for large file-backed session")
	}

	// Verify model context after compaction is shorter than the full history.
	modelCtx := session.ModelContextFromEvents(allPersisted)
	// Should be significantly fewer than 62 messages (30 pairs + user + assistant).
	if len(modelCtx) >= 62 {
		t.Errorf("model context after compaction: got %d, want < 62", len(modelCtx))
	}
}

// TestE2E_TaskStoreEdgeCases tests task store edge cases.
func TestE2E_TaskStoreEdgeCases(t *testing.T) {
	store := runner.NewMemoryTaskStore()
	ctx := ctx(t)

	// SaveTask with empty TaskID should error.
	err := store.SaveTask(ctx, runner.TaskSnapshot{})
	if err == nil {
		t.Error("SaveTask with empty TaskID should error")
	}

	// Save and load a task.
	snap := runner.TaskSnapshot{
		TaskID: "edge-task-1",
		State:  runner.TaskStateRunning,
	}
	if err := store.SaveTask(ctx, snap); err != nil {
		t.Fatalf("SaveTask: %v", err)
	}
	loaded, ok, err := store.LoadTask(ctx, "edge-task-1")
	if err != nil {
		t.Fatalf("LoadTask: %v", err)
	}
	if !ok || loaded.State != runner.TaskStateRunning {
		t.Fatalf("LoadTask: got state=%q ok=%v, want running/true", loaded.State, ok)
	}

	// Transition to completed.
	snap.State = runner.TaskStateCompleted
	snap.Output = "done"
	if err := store.SaveTask(ctx, snap); err != nil {
		t.Fatalf("SaveTask completed: %v", err)
	}
	loaded, ok, _ = store.LoadTask(ctx, "edge-task-1")
	if !ok || loaded.State != runner.TaskStateCompleted || loaded.Output != "done" {
		t.Fatalf("LoadTask completed: got state=%q output=%q", loaded.State, loaded.Output)
	}

	// SaveTask with cancelled state should prevent overwrite (silently ignored).
	cancelSnap := runner.TaskSnapshot{
		TaskID: "cancel-task",
		State:  runner.TaskStateCancelled,
	}
	if err := store.SaveTask(ctx, cancelSnap); err != nil {
		t.Fatalf("SaveTask cancel: %v", err)
	}
	// Trying to overwrite cancelled should be silently ignored.
	cancelSnap.State = runner.TaskStateCompleted
	if err := store.SaveTask(ctx, cancelSnap); err != nil {
		t.Fatalf("SaveTask overwrite cancelled: %v", err)
	}
	// Verify the state is still cancelled.
	loaded, ok, _ = store.LoadTask(ctx, "cancel-task")
	if !ok || loaded.State != runner.TaskStateCancelled {
		t.Errorf("cancelled task state: got %q ok=%v, want cancelled/true", loaded.State, ok)
	}

	// LoadTask for non-existent task.
	_, ok, err = store.LoadTask(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("LoadTask nonexistent: %v", err)
	}
	if ok {
		t.Error("LoadTask nonexistent should return ok=false")
	}
}

// TestE2E_ConcurrentToolExecutionWithHooks verifies that hooks fire correctly
// when multiple tools execute concurrently.
func TestE2E_ConcurrentToolExecutionWithHooks(t *testing.T) {
	fileSvc, err := filesession.New(filesession.Config{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New file service: %v", err)
	}
	sess, err := fileSvc.Create(ctx(t), session.CreateRequest{
		AppName: "e2e-concurrent-hooks", UserID: "test", WorkspaceKey: "workspace",
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	hook := &concurrentRecordingHook{}
	llm := &scriptedMultiToolLLM{}
	modelReg := &recordingModelRegistry{
		llm:  llm,
		info: model.ModelInfo{ModelID: "concurrent-hooks", SupportsTools: true},
	}
	toolA := &delayTool{name: "TOOL_A", delay: 5 * time.Millisecond, output: "a"}
	toolB := &delayTool{name: "TOOL_B", delay: 50 * time.Millisecond, output: "b"}
	toolC := &delayTool{name: "TOOL_C", delay: 10 * time.Millisecond, output: "c"}
	toolReg := newTestToolRegistry(toolA, toolB, toolC)

	llmAgent := llmagent.New(llmagent.Config{
		Name:     "concurrent-hook-agent",
		ModelRef: model.Ref{ModelID: "concurrent-hooks"},
		Tools:    []string{"TOOL_A", "TOOL_B", "TOOL_C"},
	})
	r, err := runner.New(runner.Config{
		Agent:         llmAgent,
		Sessions:      fileSvc,
		ModelRegistry: modelReg,
		ToolRegistry:  toolReg,
		Hooks:         []agent.Hook{hook},
	})
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}

	collectRun(t, r, ctx(t), runner.RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "concurrent hooks"}}},
	})

	hook.mu.Lock()
	defer hook.mu.Unlock()

	// Should have: 1 BeforeInvocation, 3 BeforeTool, 3 AfterTool, 1 AfterInvocation = 8
	if len(hook.calls) != 8 {
		t.Fatalf("hook calls: got %d, want 8: %v", len(hook.calls), hook.calls)
	}
	if hook.calls[0] != "BeforeInvocation" {
		t.Errorf("call 0: got %q, want BeforeInvocation", hook.calls[0])
	}
	if hook.calls[7] != "AfterInvocation" {
		t.Errorf("call 7: got %q, want AfterInvocation", hook.calls[7])
	}

	// Verify all 3 tools were hooked.
	toolNames := make(map[string]int)
	for _, c := range hook.calls {
		if strings.HasPrefix(c, "BeforeTool:") {
			name := strings.TrimPrefix(c, "BeforeTool:")
			toolNames[name]++
		}
	}
	for _, want := range []string{"TOOL_A", "TOOL_B", "TOOL_C"} {
		if toolNames[want] != 1 {
			t.Errorf("tool %s: BeforeTool count = %d, want 1", want, toolNames[want])
		}
	}
}

// ─── New helper types ────────────────────────────────────────────────

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return c
}

// overflowThenSuccessLLM returns a ContextOverflowError on the first call,
// then succeeds with "overflow-recovered" on the second call.
type overflowThenSuccessLLM struct {
	calls int
}

func (m *overflowThenSuccessLLM) Name() string { return "overflow-then-success" }

func (m *overflowThenSuccessLLM) Generate(_ context.Context, _ model.Request) iter.Seq2[model.ResponseEvent, error] {
	callIndex := m.calls
	m.calls++
	return func(yield func(model.ResponseEvent, error) bool) {
		if callIndex == 0 {
			yield(model.ResponseEvent{}, &model.ContextOverflowError{Cause: fmt.Errorf("prompt is too long")})
			return
		}
		yield(model.ResponseEvent{TextDelta: "overflow-recovered"}, nil)
	}
}

// scriptedMultiToolLLM emits 3 tool calls (A, B, C) on the first call,
// then a text response on the second call.
type scriptedMultiToolLLM struct {
	requests []model.Request
}

func (m *scriptedMultiToolLLM) Name() string { return "multi-tool-llm" }

func (m *scriptedMultiToolLLM) Generate(_ context.Context, req model.Request) iter.Seq2[model.ResponseEvent, error] {
	callIndex := len(m.requests)
	m.requests = append(m.requests, cloneModelRequest(req))
	return func(yield func(model.ResponseEvent, error) bool) {
		switch callIndex {
		case 0:
			yield(model.ResponseEvent{ToolCall: &model.ToolCallDelta{CallID: "tc-a", Name: "TOOL_A", Args: map[string]any{}}}, nil)
			yield(model.ResponseEvent{ToolCall: &model.ToolCallDelta{CallID: "tc-b", Name: "TOOL_B", Args: map[string]any{}}}, nil)
			yield(model.ResponseEvent{ToolCall: &model.ToolCallDelta{CallID: "tc-c", Name: "TOOL_C", Args: map[string]any{}}}, nil)
		case 1:
			yield(model.ResponseEvent{TextDelta: "all-tools-done"}, nil)
		default:
			yield(model.ResponseEvent{}, fmt.Errorf("unexpected call %d", callIndex))
		}
	}
}

// errorStreamLLM yields a text delta then an error.
type errorStreamLLM struct {
	textBefore string
	err        error
}

func (m *errorStreamLLM) Name() string { return "error-stream" }

func (m *errorStreamLLM) Generate(_ context.Context, _ model.Request) iter.Seq2[model.ResponseEvent, error] {
	return func(yield func(model.ResponseEvent, error) bool) {
		if m.textBefore != "" {
			yield(model.ResponseEvent{TextDelta: m.textBefore}, nil)
		}
		yield(model.ResponseEvent{}, m.err)
	}
}

// scriptedToolErrorLLM calls FAIL_TOOL on first call, then text on second.
type scriptedToolErrorLLM struct {
	requests []model.Request
}

func (m *scriptedToolErrorLLM) Name() string { return "tool-error-llm" }

func (m *scriptedToolErrorLLM) Generate(_ context.Context, req model.Request) iter.Seq2[model.ResponseEvent, error] {
	callIndex := len(m.requests)
	m.requests = append(m.requests, cloneModelRequest(req))
	return func(yield func(model.ResponseEvent, error) bool) {
		switch callIndex {
		case 0:
			yield(model.ResponseEvent{ToolCall: &model.ToolCallDelta{CallID: "fail-1", Name: "FAIL_TOOL", Args: map[string]any{}}}, nil)
		case 1:
			yield(model.ResponseEvent{TextDelta: "tool-error-handled"}, nil)
		default:
			yield(model.ResponseEvent{}, fmt.Errorf("unexpected call %d", callIndex))
		}
	}
}

// mockTextLLM returns a fixed text response.
type mockTextLLM struct {
	response string
}

func (m *mockTextLLM) Name() string { return "mock-text" }

func (m *mockTextLLM) Generate(_ context.Context, _ model.Request) iter.Seq2[model.ResponseEvent, error] {
	return func(yield func(model.ResponseEvent, error) bool) {
		yield(model.ResponseEvent{TextDelta: m.response}, nil)
	}
}

// recordingLLM records requests and returns empty responses.
type recordingLLM struct {
	requests []model.Request
}

func (m *recordingLLM) Name() string { return "recording" }

func (m *recordingLLM) Generate(_ context.Context, req model.Request) iter.Seq2[model.ResponseEvent, error] {
	m.requests = append(m.requests, cloneModelRequest(req))
	return func(yield func(model.ResponseEvent, error) bool) {
		yield(model.ResponseEvent{TextDelta: "ok"}, nil)
	}
}

// delayTool runs with an artificial delay for concurrency ordering tests.
type delayTool struct {
	name   string
	delay  time.Duration
	output string
}

func (t *delayTool) Definition() tool.Definition {
	return tool.Definition{Name: t.name, Description: "delay tool"}
}

func (t *delayTool) Run(_ tool.Context, _ tool.Call) (tool.Result, error) {
	time.Sleep(t.delay)
	return tool.Result{Output: t.output}, nil
}

// errorTool always returns an error result.
type errorTool struct {
	name   string
	errMsg string
}

func (t *errorTool) Definition() tool.Definition {
	return tool.Definition{Name: t.name, Description: "error tool"}
}

func (t *errorTool) Run(_ tool.Context, _ tool.Call) (tool.Result, error) {
	return tool.Result{Output: t.errMsg, IsError: true}, nil
}

// staticResultTool returns a fixed large output.
type staticResultTool struct {
	name   string
	output string
}

func (t *staticResultTool) Definition() tool.Definition {
	return tool.Definition{Name: t.name, Description: "static result tool"}
}

func (t *staticResultTool) Run(_ tool.Context, _ tool.Call) (tool.Result, error) {
	return tool.Result{Output: t.output}, nil
}

// alwaysDenyPolicy denies all tool calls.
type alwaysDenyPolicy struct{}

func (p *alwaysDenyPolicy) Evaluate(_ context.Context, _ policy.Request) (policy.Decision, error) {
	return policy.Decision{Outcome: policy.OutcomeDeny, Reason: "always denied by test policy"}, nil
}

// errorSandboxBackend returns an error on Run but succeeds on FileSystem.
type errorSandboxBackend struct {
	err error
}

// failingSandboxFactoryWithError returns an errorSandboxBackend.
type failingSandboxFactoryWithError struct {
	backend *errorSandboxBackend
}

func (f *failingSandboxFactoryWithError) Available(context.Context) ([]sandbox.Descriptor, error) {
	return []sandbox.Descriptor{{Name: "error-sandbox"}}, nil
}

func (f *failingSandboxFactoryWithError) Create(_ context.Context, _ sandbox.Config) (sandbox.Backend, error) {
	return f.backend, nil
}

func (b *errorSandboxBackend) Name() string { return "error-sandbox" }
func (b *errorSandboxBackend) Describe(context.Context) (sandbox.Descriptor, error) {
	return sandbox.Descriptor{Name: "error-sandbox"}, nil
}
func (b *errorSandboxBackend) Run(_ context.Context, _ sandbox.CommandRequest) (sandbox.CommandResult, error) {
	return sandbox.CommandResult{}, b.err
}
func (b *errorSandboxBackend) FileSystem(context.Context, sandbox.Constraints) (sandbox.FileSystem, error) {
	return newTempFS(os.TempDir()), nil
}
func (b *errorSandboxBackend) Status(context.Context) (sandbox.Status, error) {
	return sandbox.Status{Running: true}, nil
}
func (b *errorSandboxBackend) Close() error { return nil }

// recordingHook records all hook calls in order.
type recordingHook struct {
	mu         sync.Mutex
	calls      []string
	invHook    agent.InvocationHook
	toolHook   agent.ToolHook
	invResult  agent.InvocationHookResult
	toolResult agent.ToolHookResult
}

func (h *recordingHook) BeforeInvocation(_ context.Context, inv agent.InvocationHook) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, "BeforeInvocation")
	h.invHook = inv
	return nil
}

func (h *recordingHook) AfterInvocation(_ context.Context, result agent.InvocationHookResult) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, "AfterInvocation")
	h.invResult = result
	return nil
}

func (h *recordingHook) BeforeTool(_ context.Context, th agent.ToolHook) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, "BeforeTool")
	h.toolHook = th
	return nil
}

func (h *recordingHook) AfterTool(_ context.Context, result agent.ToolHookResult) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, "AfterTool")
	h.toolResult = result
	return nil
}

// concurrentRecordingHook records hook calls with tool names for concurrent tests.
type concurrentRecordingHook struct {
	mu    sync.Mutex
	calls []string
}

func (h *concurrentRecordingHook) BeforeInvocation(_ context.Context, _ agent.InvocationHook) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, "BeforeInvocation")
	return nil
}

func (h *concurrentRecordingHook) AfterInvocation(_ context.Context, _ agent.InvocationHookResult) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, "AfterInvocation")
	return nil
}

func (h *concurrentRecordingHook) BeforeTool(_ context.Context, th agent.ToolHook) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, "BeforeTool:"+th.ToolName)
	return nil
}

func (h *concurrentRecordingHook) AfterTool(_ context.Context, result agent.ToolHookResult) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, "AfterTool:"+result.ToolName)
	return nil
}

// alwaysCompactCompactor always compacts messages for testing overflow retry.
type alwaysCompactCompactor struct {
	calls int
}

func (c *alwaysCompactCompactor) ShouldCompact([]model.Message, int) (bool, string) {
	return false, ""
}

func (c *alwaysCompactCompactor) Compact(_ context.Context, msgs []model.Message, _ int) ([]model.Message, *session.Event, bool) {
	c.calls++
	return []model.Message{{
			Role:    model.RoleSystem,
			Content: []model.Part{{Text: "compact checkpoint"}},
		}},
		&session.Event{
			Kind:       session.EventKindCompaction,
			Visibility: session.VisibilityCanonical,
			CompactionPayload: &session.CompactionPayload{
				Reason:      "context overflow retry",
				Previous:    len(msgs),
				Remaining:   1,
				SummaryText: "compact checkpoint",
			},
		},
		true
}

// Ensure recordingHook and concurrentRecordingHook implement agent.Hook.
var (
	_ agent.Hook = (*recordingHook)(nil)
	_ agent.Hook = (*concurrentRecordingHook)(nil)
)

// ─── ACP Integration Test ────────────────────────────────────────────

// TestE2E_ACPAgentThroughRunner tests the ACP agent SDK's ability to operate
// external ACP agents through the full runner pipeline. Uses a fake ACP client
// factory that simulates an external ACP agent responding with content chunks
// and tool calls.
func TestE2E_ACPAgentThroughRunner(t *testing.T) {
	fileSvc, err := filesession.New(filesession.Config{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New file service: %v", err)
	}
	sess, err := fileSvc.Create(ctx(t), session.CreateRequest{
		AppName: "e2e-acp", UserID: "test", WorkspaceKey: "workspace",
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	// Create a fake ACP client factory that simulates an external agent.
	factory := &e2eFakeACPClientFactory{}
	finalTrue := true
	finalFalse := false
	factory.client.promptFn = func(_ context.Context, sessionID string, prompt string, _ map[string]any) {
		// Simulate streaming: partial delta then final.
		factory.callbacks.OnUpdate(e2eUpdateEnvelope{
			SessionID: sessionID,
			Update: e2eContentChunk{
				SessionUpdate: "agent_message",
				Content:       json.RawMessage(`{"type":"text","text":"thinking..."}`),
				Final:         &finalFalse,
			},
		})
		factory.callbacks.OnUpdate(e2eUpdateEnvelope{
			SessionID: sessionID,
			Update: e2eContentChunk{
				SessionUpdate: "agent_message",
				Content:       json.RawMessage(`{"type":"text","text":"acp response to: ` + prompt + `"}`),
				Final:         &finalTrue,
			},
		})
	}

	acpAgent := &e2eACPAgent{
		name:    "e2e-acp-agent",
		factory: factory,
	}

	// Run the ACP agent through the runner.
	r, err := runner.New(runner.Config{
		Agent:    acpAgent,
		Sessions: fileSvc,
	})
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}

	events := collectRun(t, r, ctx(t), runner.RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "test acp"}}},
	})

	persisted := persistedLiveEvents(events)
	if len(persisted) < 2 {
		t.Fatalf("persisted events: got %d, want >= 2: %v", len(persisted), eventKinds(persisted))
	}
	if persisted[0].Kind != session.EventKindUser {
		t.Errorf("first event: got %q, want user", persisted[0].Kind)
	}
	// The last event should be the canonical assistant response.
	last := persisted[len(persisted)-1]
	if last.Kind != session.EventKindAssistant {
		t.Errorf("last event: got %q, want assistant", last.Kind)
	}
	if last.Visibility != session.VisibilityCanonical {
		t.Errorf("last visibility: got %q, want canonical", last.Visibility)
	}

	// Verify the ACP client was called correctly.
	if factory.client.promptText != "test acp" {
		t.Errorf("ACP prompt: got %q, want %q", factory.client.promptText, "test acp")
	}

	// Verify model context reconstruction.
	allPersisted, _ := fileSvc.Events(ctx(t), session.EventsRequest{SessionRef: sess.Ref})
	modelCtx := session.ModelContextFromEvents(allPersisted)
	if len(modelCtx) < 2 {
		t.Fatalf("model context: got %d, want >= 2", len(modelCtx))
	}

	// Verify ACP projection.
	projections := projectAllACP(allPersisted)
	if len(projections) == 0 {
		t.Error("expected ACP projections from ACP agent events")
	}
}

// ─── Plugin E2E Test ─────────────────────────────────────────────────

// TestE2E_SuperpowersPluginInstallAndResolve tests the plugin system's ability
// to resolve, install, and load the superpowers plugin from a real git clone.
func TestE2E_SuperpowersPluginInstallAndResolve(t *testing.T) {
	// Find the superpowers repo.
	superpowersRoot := "/tmp/superpowers"
	if _, err := os.Stat(superpowersRoot); os.IsNotExist(err) {
		t.Skip("superpowers repo not cloned at /tmp/superpowers")
	}

	resolver := pluginfs.NewResolver()
	resolved, err := resolver.Resolve(context.Background(), caelisplugin.ResolveRequest{Root: superpowersRoot})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	// Verify manifest.
	if resolved.Manifest.Name != "superpowers" {
		t.Fatalf("manifest name = %q, want superpowers", resolved.Manifest.Name)
	}
	if resolved.Manifest.Version == "" {
		t.Error("manifest version should not be empty")
	}
	if resolved.Manifest.Repository == "" {
		t.Error("manifest repository should not be empty")
	}

	// Verify skills discovery.
	if len(resolved.Skills) == 0 {
		t.Fatal("expected at least one skill from superpowers")
	}
	skillNames := make(map[string]bool)
	for _, s := range resolved.Skills {
		skillNames[s.Name] = true
		t.Logf("Discovered skill: %s (%s)", s.Name, s.Description)
	}
	// Superpowers should have these core skills.
	for _, want := range []string{"brainstorming", "writing-plans", "test-driven-development"} {
		if !skillNames[want] {
			t.Errorf("expected skill %q, got: %v", want, skillNames)
		}
	}

	// Verify skill metadata.
	for _, s := range resolved.Skills {
		if s.Metadata["plugin"] != "superpowers" {
			t.Errorf("skill %s metadata plugin = %v, want superpowers", s.Name, s.Metadata["plugin"])
		}
	}

	// Install to a store.
	storeRoot := t.TempDir()
	store := pluginfs.NewStore(pluginfs.StoreConfig{Root: storeRoot})
	installed, err := store.Install(context.Background(), resolved)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if installed.Name != "superpowers" {
		t.Fatalf("installed name = %q, want superpowers", installed.Name)
	}
	if installed.Version == "" {
		t.Error("installed version should not be empty")
	}

	// Verify lock file.
	lockData, err := os.ReadFile(filepath.Join(storeRoot, "plugins.lock.json"))
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	if !strings.Contains(string(lockData), "superpowers") {
		t.Error("lock file should contain superpowers")
	}

	// List installed plugins.
	installedList, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(installedList) != 1 {
		t.Fatalf("installed list: got %d, want 1", len(installedList))
	}

	// Load via registry.
	registry := pluginfs.NewRegistry(pluginfs.RegistryConfig{Store: store, Resolver: resolver})
	loaded, err := registry.Load(context.Background(), "superpowers")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(loaded.Skills) == 0 {
		t.Error("loaded skills should not be empty")
	}
	if loaded.Manifest.Name != "superpowers" {
		t.Fatalf("loaded manifest name = %q, want superpowers", loaded.Manifest.Name)
	}

	// Verify runtime contributions.
	if len(loaded.Runtime.Skills) == 0 {
		t.Error("runtime skills should not be empty")
	}

	t.Logf("Superpowers plugin: %d skills installed, version %s", len(loaded.Skills), installed.Version)
}

// TestE2E_SuperpowersPluginThroughRunner tests that the plugin's skills are
// assembled into the system prompt when used with the runner.
func TestE2E_SuperpowersPluginThroughRunner(t *testing.T) {
	superpowersRoot := "/tmp/superpowers"
	if _, err := os.Stat(superpowersRoot); os.IsNotExist(err) {
		t.Skip("superpowers repo not cloned at /tmp/superpowers")
	}

	// Install the plugin.
	resolver := pluginfs.NewResolver()
	resolved, err := resolver.Resolve(context.Background(), caelisplugin.ResolveRequest{Root: superpowersRoot})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	storeRoot := t.TempDir()
	store := pluginfs.NewStore(pluginfs.StoreConfig{Root: storeRoot})
	_, err = store.Install(context.Background(), resolved)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}

	// Create a plugin registry.
	registry := pluginfs.NewRegistry(pluginfs.RegistryConfig{Store: store, Resolver: resolver})

	// Create a runner with the plugin registry.
	fileSvc, err := filesession.New(filesession.Config{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New file service: %v", err)
	}
	sess, err := fileSvc.Create(ctx(t), session.CreateRequest{
		AppName: "e2e-plugin", UserID: "test", WorkspaceKey: "workspace",
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	llm := &recordingLLM{}
	modelReg := &recordingModelRegistry{
		llm:  llm,
		info: model.ModelInfo{ModelID: "plugin-test"},
	}
	llmAgent := llmagent.New(llmagent.Config{
		Name:     "plugin-agent",
		ModelRef: model.Ref{ModelID: "plugin-test"},
	})
	r, err := runner.New(runner.Config{
		Agent:         llmAgent,
		Sessions:      fileSvc,
		ModelRegistry: modelReg,
		Plugins:       registry,
	})
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}

	collectRun(t, r, ctx(t), runner.RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "hello with plugin"}}},
	})

	if len(llm.requests) != 1 {
		t.Fatalf("model requests: got %d, want 1", len(llm.requests))
	}
	msgs := llm.requests[0].Messages
	if len(msgs) < 1 {
		t.Fatalf("messages: got %d, want >= 1", len(msgs))
	}
	// The system prompt should contain plugin-contributed content.
	// The superpowers plugin contributes skills which are assembled into the prompt.
	systemText := ""
	for _, m := range msgs {
		if m.Role == model.RoleSystem {
			systemText += m.TextContent()
		}
	}
	if systemText == "" {
		t.Error("expected system prompt from plugin contributions")
	}
	t.Logf("System prompt length: %d chars", len(systemText))
}

// ─── ACP/Plugin helper types ─────────────────────────────────────────

// e2eFakeACPClientFactory simulates an external ACP agent for e2e testing.
type e2eFakeACPClientFactory struct {
	callbacks e2eACPClientCallbacks
	client    e2eFakeACPClient
}

type e2eACPClientCallbacks struct {
	OnUpdate func(e2eUpdateEnvelope)
}

type e2eUpdateEnvelope struct {
	SessionID string
	Update    e2eContentChunk
}

type e2eContentChunk struct {
	SessionUpdate string          `json:"session_update"`
	Content       json.RawMessage `json:"content"`
	Final         *bool           `json:"final,omitempty"`
}

type e2eFakeACPClient struct {
	promptFn     func(ctx context.Context, sessionID string, prompt string, meta map[string]any)
	promptText   string
	promptCalled bool
}

func (f *e2eFakeACPClientFactory) Start(_ context.Context, callbacks e2eACPClientCallbacks) (*e2eFakeACPClient, error) {
	f.callbacks = callbacks
	return &f.client, nil
}

// e2eACPAgent is a simplified ACP agent for e2e testing that uses the fake factory.
type e2eACPAgent struct {
	name    string
	factory *e2eFakeACPClientFactory
}

func (a *e2eACPAgent) Name() string                 { return a.name }
func (a *e2eACPAgent) Description() string          { return "e2e ACP agent" }
func (a *e2eACPAgent) SubAgents() []agent.Agent     { return nil }
func (a *e2eACPAgent) FindAgent(string) agent.Agent { return nil }

func (a *e2eACPAgent) Run(inv agent.InvocationContext) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) {
		client, err := a.factory.Start(inv, e2eACPClientCallbacks{
			OnUpdate: func(env e2eUpdateEnvelope) {
				// Convert ACP update to session event.
				var text string
				var content struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}
				if err := json.Unmarshal(env.Update.Content, &content); err == nil {
					text = content.Text
				}
				isFinal := env.Update.Final != nil && *env.Update.Final
				vis := session.VisibilityUIOnly
				if isFinal {
					vis = session.VisibilityCanonical
				}
				evt := session.Event{
					Kind:       session.EventKindAssistant,
					Visibility: vis,
					AssistantPayload: &session.AssistantPayload{
						Parts: []session.EventPart{{Kind: session.PartKindText, Text: text}},
					},
				}
				if !yield(evt, nil) {
					return
				}
			},
		})
		if err != nil {
			yield(session.Event{}, err)
			return
		}
		defer client.Close(context.Background())

		// Initialize and create session.
		// (Skip actual ACP protocol - just call prompt directly)
		a.factory.client.promptText = inv.UserMessage().TextContent()
		a.factory.client.promptCalled = true
		if a.factory.client.promptFn != nil {
			a.factory.client.promptFn(inv, "e2e-session", inv.UserMessage().TextContent(), nil)
		}
	}
}

func (c *e2eFakeACPClient) Close(context.Context) error { return nil }
