package local

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/core/config"
	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/plugin"
	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/core/tool"
	acpexternal "github.com/OnslaughtSnail/caelis/internal/adapters/acpagent/external"
	sandboxhost "github.com/OnslaughtSnail/caelis/internal/adapters/sandbox/host"
	storememory "github.com/OnslaughtSnail/caelis/internal/adapters/store/memory"
	toolfilesystem "github.com/OnslaughtSnail/caelis/internal/adapters/tools/filesystem"
	toolshell "github.com/OnslaughtSnail/caelis/internal/adapters/tools/shell"
	toolspawn "github.com/OnslaughtSnail/caelis/internal/adapters/tools/spawn"
	tooltask "github.com/OnslaughtSnail/caelis/internal/adapters/tools/task"
	"github.com/OnslaughtSnail/caelis/internal/app/services"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
	"github.com/OnslaughtSnail/caelis/internal/engine/approval"
	"github.com/OnslaughtSnail/caelis/protocol/acp/jsonrpc"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestNewRequiresProvider(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New without provider error = nil, want error")
	}
}

func TestStackServicesListProviderModelsThroughRegistry(t *testing.T) {
	ctx := context.Background()
	var captured plugin.ModelProviderConfig
	stack, err := NewWithContext(ctx, Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "tester",
			WorkspaceKey: "repo",
			WorkspaceCWD: t.TempDir(),
		},
		Model: config.ModelProfile{
			Provider: "catalog-test",
			Model:    "seed",
			BaseURL:  "https://runtime.example.test/v1",
		},
		Contributions: []plugin.Contribution{contributionFunc(func(_ context.Context, reg plugin.Registry) error {
			return reg.RegisterModelProvider("catalog-test", func(_ context.Context, cfg plugin.ModelProviderConfig) (model.Provider, error) {
				captured = cfg
				return catalogTestProvider{models: []model.ModelInfo{{ID: "remote-live", Provider: "catalog-test"}}}, nil
			})
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	models, err := stack.Services().Models().ProviderModels(ctx, appsettings.ModelConfig{
		Provider: "catalog-test",
		BaseURL:  "https://models.example.test/v1",
		Token:    "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0] != "remote-live" {
		t.Fatalf("provider models = %#v, want registry-backed remote model", models)
	}
	if captured.Provider != "catalog-test" || captured.Endpoint != "https://models.example.test/v1" || captured.Token != "secret" {
		t.Fatalf("provider factory cfg = %#v, want discovery config", captured)
	}
}

func TestStackRunsTurnThroughServices(t *testing.T) {
	provider := &capturingProvider{message: model.Message{
		Role:  model.RoleAssistant,
		Parts: []model.Part{model.NewTextPart("pong")},
	}}
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "tester",
			WorkspaceKey: "repo",
			WorkspaceCWD: t.TempDir(),
		},
		Provider: provider,
		ExternalACPAgents: []acpexternal.Config{{
			AgentID:   "reviewer",
			AgentName: "Reviewer",
			Command:   "reviewer-acp",
			Args:      []string{"--stdio"},
		}},
		ToolList: []tool.Tool{
			tool.NamedTool{Def: tool.Definition{Name: "noop", Description: "no operation"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{Title: "scratch"})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := stack.Services().Turns().Begin(context.Background(), services.BeginTurnRequest{
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "ping",
		Model:      "gpt-test",
		Surface:    "tui",
	})
	if err != nil {
		t.Fatal(err)
	}

	var events []session.Event
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatal(env.Err)
		}
		events = append(events, env.Event)
	}
	if len(events) != 2 {
		t.Fatalf("turn events = %d, want user and assistant", len(events))
	}
	if got := session.EventText(events[1]); got != "pong" {
		t.Fatalf("assistant text = %q, want pong", got)
	}
	if provider.request.Model != "gpt-test" {
		t.Fatalf("provider model = %q, want gpt-test", provider.request.Model)
	}
	agents, err := stack.Services().Agents().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].ID != "reviewer" || agents[0].Kind != services.AgentKindExternalACP {
		t.Fatalf("agents = %#v, want reviewer ACP agent", agents)
	}
	if len(provider.request.Tools) != 1 || provider.request.Tools[0].Name != "noop" {
		t.Fatalf("provider tools = %#v, want noop tool", provider.request.Tools)
	}

	snapshot, err := stack.Services().Sessions().Load(context.Background(), session.Ref{SessionID: active.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 2 {
		t.Fatalf("snapshot events = %d, want 2", len(snapshot.Events))
	}
	if got := session.EventText(snapshot.Events[0]); got != "ping" {
		t.Fatalf("stored user text = %q, want ping", got)
	}
}

func TestStackRegistersDefaultSelfACPAgent(t *testing.T) {
	storeDir := t.TempDir()
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "tester",
			WorkspaceKey: "repo",
			WorkspaceCWD: "/repo",
			Store:        config.Store{Backend: "jsonl", URI: storeDir},
			Meta:         map[string]any{"permission_mode": "manual"},
		},
		Model: config.ModelProfile{
			Alias:               "main",
			Provider:            "openai_compatible",
			Model:               "gpt-test",
			BaseURL:             "https://api.example.test/v1",
			Token:               "secret-token",
			AuthType:            "bearer",
			ContextWindowTokens: 128000,
			MaxOutputTokens:     4096,
			Meta:                map[string]any{"cli_api": "openai"},
		},
		Provider: &capturingProvider{message: model.Message{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("unused")},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	agents, err := stack.Services().Agents().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].ID != "self" || agents[0].Name != "self" || agents[0].Command == "" {
		t.Fatalf("agents = %#v, want default self ACP agent", agents)
	}
	args := agents[0].Args
	for _, want := range []string{
		"acp",
		"-app", "caelis",
		"-user", "tester",
		"-store-dir", storeDir,
		"-workspace-key", "repo",
		"-workspace-cwd", "/repo",
		"-permission-mode", "manual",
		"-model-alias", "main",
		"-provider", "openai_compatible",
		"-api", "openai",
		"-model", "gpt-test",
		"-base-url", "https://api.example.test/v1",
		"-token-env", selfModelTokenEnv,
		"-auth-type", "bearer",
		"-context-window", "128000",
		"-max-output-tokens", "4096",
	} {
		if !stringSliceHas(args, want) {
			t.Fatalf("self args = %#v, want %q", args, want)
		}
	}
	if stringSliceHas(args, "secret-token") {
		t.Fatalf("self args leaked token: %#v", args)
	}
	if got := agents[0].Env[selfModelTokenEnv]; got != "secret-token" {
		t.Fatalf("self env token = %q, want literal token", got)
	}
}

func TestStackDoesNotOverrideConfiguredSelfACPAgent(t *testing.T) {
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName: "caelis",
			UserID:  "tester",
			Store:   config.Store{Backend: "jsonl", URI: t.TempDir()},
		},
		Provider: &capturingProvider{message: model.Message{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("unused")},
		}},
		ExternalACPAgents: []acpexternal.Config{{
			AgentID:   "self",
			AgentName: "self",
			Command:   "custom-self-acp",
			Args:      []string{"--stdio"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	agents, err := stack.Services().Agents().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].ID != "self" || agents[0].Command != "custom-self-acp" || !stringSliceHas(agents[0].Args, "--stdio") {
		t.Fatalf("agents = %#v, want configured self ACP agent only", agents)
	}
}

func TestStackInvokesExternalACPAgentThroughServices(t *testing.T) {
	provider := &capturingProvider{message: model.Message{
		Role:  model.RoleAssistant,
		Parts: []model.Part{model.NewTextPart("local baseline")},
	}}
	stack, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester", WorkspaceCWD: t.TempDir()},
		Provider: provider,
		ExternalACPAgents: []acpexternal.Config{{
			AgentID:   "helper",
			AgentName: "Helper",
			Command:   os.Args[0],
			Args:      []string{"-test.run=TestExternalACPHelperProcess", "--"},
			Env:       []string{"CAELIS_TEST_EXTERNAL_ACP_HELPER=1"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := stack.Services().Agents().Invoke(context.Background(), services.AgentInvokeRequest{
		AgentID:    "helper",
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "delegate",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 || session.EventText(result.Events[0]) != "external helper response" {
		t.Fatalf("invoke result = %#v, want helper response", result)
	}
	snapshot, err := stack.Services().Sessions().Load(context.Background(), session.Ref{SessionID: active.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 1 || session.EventText(snapshot.Events[0]) != "external helper response" {
		t.Fatalf("stored events = %#v, want helper response", snapshot.Events)
	}
	if snapshot.Events[0].Scope == nil || snapshot.Events[0].Scope.Participant.ID != "helper" || snapshot.Events[0].Scope.ACP.SessionID == "" {
		t.Fatalf("stored event scope = %#v, want participant and remote ACP session", snapshot.Events[0].Scope)
	}
}

func TestStackInvokesExternalACPAgentAsControllerThroughServices(t *testing.T) {
	provider := &capturingProvider{message: model.Message{
		Role:  model.RoleAssistant,
		Parts: []model.Part{model.NewTextPart("local baseline")},
	}}
	stack, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester", WorkspaceCWD: t.TempDir()},
		Provider: provider,
		ExternalACPAgents: []acpexternal.Config{{
			AgentID:   "helper",
			AgentName: "Helper",
			Command:   os.Args[0],
			Args:      []string{"-test.run=TestExternalACPHelperProcess", "--"},
			Env:       []string{"CAELIS_TEST_EXTERNAL_ACP_HELPER=1"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := stack.Services().Agents().Invoke(context.Background(), services.AgentInvokeRequest{
		AgentID:    "helper",
		SessionRef: session.Ref{SessionID: active.SessionID},
		Controller: session.ControllerBinding{
			Kind: session.ControllerACP,
			ID:   "helper",
		},
		Input: "delegate",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 || session.EventText(result.Events[0]) != "external helper response" {
		t.Fatalf("invoke result = %#v, want helper response", result)
	}
	snapshot, err := stack.Services().Sessions().Load(context.Background(), session.Ref{SessionID: active.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 1 || snapshot.Events[0].Scope == nil || snapshot.Events[0].Scope.Controller.ID != "helper" {
		t.Fatalf("stored events = %#v, want helper controller event", snapshot.Events)
	}
	if snapshot.Events[0].Actor.Kind != session.ActorController {
		t.Fatalf("stored event actor = %#v, want controller", snapshot.Events[0].Actor)
	}
}

func TestStackRegistersCoreSpawnToolForACPAgents(t *testing.T) {
	provider := &capturingProvider{message: model.Message{
		Role:  model.RoleAssistant,
		Parts: []model.Part{model.NewTextPart("ok")},
	}}
	rt, err := sandboxhost.New(context.Background(), sandbox.Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	stack, err := New(Config{
		Runtime:      config.Runtime{AppName: "caelis", UserID: "tester", WorkspaceCWD: t.TempDir()},
		Provider:     provider,
		Sandbox:      rt,
		BuiltinTools: true,
		ExternalACPAgents: []acpexternal.Config{{
			AgentID:     "helper",
			AgentName:   "helper",
			Description: "test helper",
			Command:     os.Args[0],
			Args:        []string{"-test.run=TestExternalACPHelperProcess", "--"},
			Env:         []string{"CAELIS_TEST_EXTERNAL_ACP_HELPER=1"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := stack.Services().Turns().Begin(context.Background(), services.BeginTurnRequest{
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "list tools",
	})
	if err != nil {
		t.Fatal(err)
	}
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatal(env.Err)
		}
	}
	if !capturedTool(provider.request.Tools, toolspawn.ToolName) {
		t.Fatalf("provider tools = %#v, missing SPAWN", provider.request.Tools)
	}
	var spawnSpec *model.ToolSpec
	for idx := range provider.request.Tools {
		if provider.request.Tools[idx].Name == toolspawn.ToolName {
			spawnSpec = &provider.request.Tools[idx]
			break
		}
	}
	if spawnSpec == nil || spawnSpec.Kind != model.ToolSpecFunction {
		t.Fatalf("spawn spec = %#v, want function tool", spawnSpec)
	}
	props, _ := spawnSpec.InputSchema["properties"].(map[string]any)
	agentProp, _ := props["agent"].(map[string]any)
	if !schemaEnumHas(agentProp["enum"], "helper") {
		t.Fatalf("spawn agent schema = %#v, want helper enum", agentProp)
	}
}

func TestStackRunsCoreSpawnToolThroughExternalACPAgent(t *testing.T) {
	rawInput, err := json.Marshal(map[string]any{"agent": "helper", "prompt": "delegate"})
	if err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{responses: []model.Message{
		{
			Role: model.RoleAssistant,
			Parts: []model.Part{{
				Kind: model.PartToolUse,
				ToolUse: &model.ToolCall{
					ID:    "spawn-call",
					Name:  toolspawn.ToolName,
					Input: rawInput,
				},
			}},
		},
		{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("done")},
		},
	}}
	rt, err := sandboxhost.New(context.Background(), sandbox.Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	stack, err := New(Config{
		Runtime:      config.Runtime{AppName: "caelis", UserID: "tester", WorkspaceCWD: t.TempDir()},
		Provider:     provider,
		Sandbox:      rt,
		BuiltinTools: true,
		ExternalACPAgents: []acpexternal.Config{{
			AgentID:   "helper",
			AgentName: "helper",
			Command:   os.Args[0],
			Args:      []string{"-test.run=TestExternalACPHelperProcess", "--"},
			Env:       []string{"CAELIS_TEST_EXTERNAL_ACP_HELPER=1"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := stack.Services().Turns().Begin(context.Background(), services.BeginTurnRequest{
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "spawn helper",
	})
	if err != nil {
		t.Fatal(err)
	}
	var events []session.Event
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatal(env.Err)
		}
		events = append(events, env.Event)
	}
	var childEvent, spawnResult *session.Event
	for idx := range events {
		event := &events[idx]
		if event.Scope != nil && event.Scope.Participant.Kind == session.ParticipantSubagent {
			childEvent = event
		}
		if event.Type == session.EventToolResult && event.Tool != nil && event.Tool.Name == toolspawn.ToolName {
			spawnResult = event
		}
	}
	if childEvent == nil || session.EventText(*childEvent) != "external helper response" {
		t.Fatalf("events = %#v, want canonical subagent child response", events)
	}
	if childEvent.Scope.Participant.ID != "spawn-call" || childEvent.Scope.Participant.DelegationID != "spawn-call" || childEvent.Scope.Participant.ParentTurnID == "" {
		t.Fatalf("child participant = %#v, want spawn-call delegation", childEvent.Scope.Participant)
	}
	if childEvent.Scope.Source != "spawn" {
		t.Fatalf("child scope source = %q, want spawn", childEvent.Scope.Source)
	}
	if spawnResult == nil || spawnResult.Tool.Output["task_id"] != "spawn-call" || spawnResult.Tool.Output["final_message"] != "external helper response" {
		t.Fatalf("spawn result = %#v, want model-visible final message", spawnResult)
	}
	snapshot, err := stack.Services().Sessions().Load(context.Background(), session.Ref{SessionID: active.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != len(events) {
		t.Fatalf("stored events = %d, live events = %d", len(snapshot.Events), len(events))
	}
}

func TestStackRunsAsyncSpawnThroughTaskWait(t *testing.T) {
	rawSpawnInput, err := json.Marshal(map[string]any{"agent": "helper", "prompt": "delegate", "yield_time_ms": 1})
	if err != nil {
		t.Fatal(err)
	}
	rawTaskWaitInput, err := json.Marshal(map[string]any{"action": "wait", "task_id": "spawn-call", "yield_time_ms": 2000})
	if err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{responses: []model.Message{
		{
			Role: model.RoleAssistant,
			Parts: []model.Part{{
				Kind: model.PartToolUse,
				ToolUse: &model.ToolCall{
					ID:    "spawn-call",
					Name:  toolspawn.ToolName,
					Input: rawSpawnInput,
				},
			}},
		},
		{
			Role: model.RoleAssistant,
			Parts: []model.Part{{
				Kind: model.PartToolUse,
				ToolUse: &model.ToolCall{
					ID:    "task-wait",
					Name:  tooltask.ToolName,
					Input: rawTaskWaitInput,
				},
			}},
		},
		{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("done")},
		},
	}}
	rt, err := sandboxhost.New(context.Background(), sandbox.Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	stack, err := New(Config{
		Runtime:      config.Runtime{AppName: "caelis", UserID: "tester", WorkspaceCWD: t.TempDir()},
		Provider:     provider,
		Sandbox:      rt,
		BuiltinTools: true,
		ExternalACPAgents: []acpexternal.Config{{
			AgentID:   "helper",
			AgentName: "helper",
			Command:   os.Args[0],
			Args:      []string{"-test.run=TestExternalACPHelperProcess", "--"},
			Env:       []string{"CAELIS_TEST_EXTERNAL_ACP_HELPER=1", "CAELIS_TEST_EXTERNAL_ACP_DELAY_MS=150"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := stack.Services().Turns().Begin(context.Background(), services.BeginTurnRequest{
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "spawn helper async",
	})
	if err != nil {
		t.Fatal(err)
	}
	var liveEvents []session.Event
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatal(env.Err)
		}
		liveEvents = append(liveEvents, env.Event)
	}
	spawnResult := findToolResult(liveEvents, toolspawn.ToolName)
	if spawnResult == nil || spawnResult.Tool.Output["state"] != "running" || spawnResult.Tool.Output["task_id"] != "spawn-call" {
		t.Fatalf("spawn result = %#v, want running task result", spawnResult)
	}
	taskResult := findToolResult(liveEvents, tooltask.ToolName)
	if taskResult == nil {
		t.Fatalf("events = %#v, missing TASK wait result", liveEvents)
	}
	if stdout, _ := taskResult.Tool.Output["stdout"].(string); !strings.Contains(stdout, "external helper response") {
		t.Fatalf("TASK wait output = %#v, want external helper response", taskResult.Tool.Output)
	}
	if runtimeTask := nestedRuntimeTask(taskResult.Tool.Meta); runtimeTask["task_kind"] != "subagent" || runtimeTask["agent"] != "helper" {
		t.Fatalf("TASK wait meta = %#v, want subagent helper task", taskResult.Tool.Meta)
	}
	snapshot, err := stack.Services().Sessions().Load(context.Background(), session.Ref{SessionID: active.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	childEvent := findSubagentEvent(snapshot.Events, "spawn-call")
	if childEvent == nil || session.EventText(*childEvent) != "external helper response" {
		t.Fatalf("stored events = %#v, want canonical async subagent response", snapshot.Events)
	}
}

func TestStackRestoresCompletedAsyncSpawnTaskFromJournal(t *testing.T) {
	rawSpawnInput, err := json.Marshal(map[string]any{"agent": "helper", "prompt": "delegate", "yield_time_ms": 1})
	if err != nil {
		t.Fatal(err)
	}
	rawTaskWaitInput, err := json.Marshal(map[string]any{"action": "wait", "task_id": "spawn-call", "yield_time_ms": 2000})
	if err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{responses: []model.Message{
		{
			Role: model.RoleAssistant,
			Parts: []model.Part{{
				Kind: model.PartToolUse,
				ToolUse: &model.ToolCall{
					ID:    "spawn-call",
					Name:  toolspawn.ToolName,
					Input: rawSpawnInput,
				},
			}},
		},
		{
			Role: model.RoleAssistant,
			Parts: []model.Part{{
				Kind: model.PartToolUse,
				ToolUse: &model.ToolCall{
					ID:    "task-wait",
					Name:  tooltask.ToolName,
					Input: rawTaskWaitInput,
				},
			}},
		},
		{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("done")},
		},
	}}
	rt, err := sandboxhost.New(context.Background(), sandbox.Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	storeRoot := t.TempDir()
	runtimeCfg := config.Runtime{
		AppName:      "caelis",
		UserID:       "tester",
		WorkspaceCWD: t.TempDir(),
		Store:        config.Store{Backend: "jsonl", URI: storeRoot},
	}
	agents := []acpexternal.Config{{
		AgentID:   "helper",
		AgentName: "helper",
		Command:   os.Args[0],
		Args:      []string{"-test.run=TestExternalACPHelperProcess", "--"},
		Env:       []string{"CAELIS_TEST_EXTERNAL_ACP_HELPER=1", "CAELIS_TEST_EXTERNAL_ACP_DELAY_MS=100"},
	}}
	stack, err := New(Config{
		Runtime:           runtimeCfg,
		Provider:          provider,
		Sandbox:           rt,
		BuiltinTools:      true,
		ExternalACPAgents: agents,
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := stack.Services().Turns().Begin(context.Background(), services.BeginTurnRequest{
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "spawn helper async",
	})
	if err != nil {
		t.Fatal(err)
	}
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatal(env.Err)
		}
	}

	reopened, err := New(Config{
		Runtime:           runtimeCfg,
		Provider:          &scriptedProvider{},
		ExternalACPAgents: agents,
	})
	if err != nil {
		t.Fatal(err)
	}
	list, err := reopened.Services().Tasks().List(context.Background(), services.ListTasksRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	task, ok := findTaskView(list.Tasks, "spawn-call")
	if !ok {
		t.Fatalf("restored tasks = %#v, want spawn-call", list.Tasks)
	}
	if task.Kind != "subagent" || task.Agent != "helper" || task.State != string(sandbox.SessionCompleted) || task.Running {
		t.Fatalf("restored task = %#v, want completed helper subagent", task)
	}
	output, err := reopened.Services().Tasks().Tail(context.Background(), services.TaskOutputRequest{TaskID: "spawn-call"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.Stdout, "external helper response") || output.Task.Kind != "subagent" {
		t.Fatalf("restored output = %#v, want durable helper response", output)
	}
}

func TestExternalACPHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_TEST_EXTERNAL_ACP_HELPER") != "1" {
		return
	}
	conn := jsonrpc.New(os.Stdin, os.Stdout)
	err := conn.Serve(context.Background(), func(ctx context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
		switch msg.Method {
		case schema.MethodInitialize:
			return schema.InitializeResponse{
				ProtocolVersion: schema.CurrentProtocolVersion,
				AgentCapabilities: schema.AgentCapabilities{
					PromptCapabilities: schema.PromptCapabilities{Image: true},
				},
				AgentInfo: &schema.Implementation{Name: "helper", Version: "test"},
			}, nil
		case schema.MethodSessionNew:
			return schema.NewSessionResponse{SessionID: "remote-helper-session"}, nil
		case schema.MethodSessionPrompt:
			var req schema.PromptRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			if delayMS, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("CAELIS_TEST_EXTERNAL_ACP_DELAY_MS"))); delayMS > 0 {
				time.Sleep(time.Duration(delayMS) * time.Millisecond)
			}
			_ = conn.Notify(schema.MethodSessionUpdate, schema.SessionNotification{
				SessionID: req.SessionID,
				Update: schema.ContentChunk{
					SessionUpdate: schema.UpdateAgentMessage,
					Content:       schema.TextContent{Type: "text", Text: "external helper response"},
				},
			})
			return schema.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
		default:
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
		}
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestStackRunsConcreteShellToolThroughSandbox(t *testing.T) {
	rt, err := sandboxhost.New(context.Background(), sandbox.Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runTool, err := toolshell.NewRunCommandTool(rt)
	if err != nil {
		t.Fatal(err)
	}
	command := "printf hello"
	if runtime.GOOS == "windows" {
		command = "echo hello"
	}
	rawInput, err := json.Marshal(map[string]any{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{responses: []model.Message{
		{
			Role: model.RoleAssistant,
			Parts: []model.Part{{
				Kind: model.PartToolUse,
				ToolUse: &model.ToolCall{
					ID:    "call-1",
					Name:  toolshell.RunCommandToolName,
					Input: rawInput,
				},
			}},
		},
		{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("done")},
		},
	}}
	stack, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester"},
		Provider: provider,
		Sandbox:  rt,
		ToolList: []tool.Tool{runTool},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stack.Sandbox() == nil {
		t.Fatal("stack sandbox = nil, want configured runtime")
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := stack.Services().Turns().Begin(context.Background(), services.BeginTurnRequest{
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "run command",
	})
	if err != nil {
		t.Fatal(err)
	}

	var events []session.Event
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatal(env.Err)
		}
		events = append(events, env.Event)
	}
	if len(events) != 5 {
		t.Fatalf("turn events = %d, want user, assistant, tool_call, tool_result, assistant", len(events))
	}
	if events[3].Type != session.EventToolResult || !strings.Contains(session.EventText(events[3]), "hello") {
		t.Fatalf("tool result event = %#v, want shell stdout", events[3])
	}
	if got := session.EventText(events[4]); got != "done" {
		t.Fatalf("final assistant text = %q, want done", got)
	}
}

func TestStackRegistersCoreFilesystemBuiltinTools(t *testing.T) {
	workspace := t.TempDir()
	provider := &capturingProvider{message: model.Message{
		Role:  model.RoleAssistant,
		Parts: []model.Part{model.NewTextPart("ok")},
	}}
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "tester",
			WorkspaceCWD: workspace,
			Sandbox:      config.Sandbox{Backend: "host"},
		},
		Provider:     provider,
		BuiltinTools: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := stack.Services().Turns().Begin(context.Background(), services.BeginTurnRequest{
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "inspect files",
	})
	if err != nil {
		t.Fatal(err)
	}
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatal(env.Err)
		}
	}
	for _, name := range []string{
		toolfilesystem.ReadFileToolName,
		toolfilesystem.ListDirectoryToolName,
		toolfilesystem.GlobFilesToolName,
		toolfilesystem.SearchFilesToolName,
		toolfilesystem.WriteFileToolName,
		toolfilesystem.PatchFileToolName,
		"update_plan",
		toolshell.RunCommandToolName,
		tooltask.ToolName,
	} {
		if !capturedTool(provider.request.Tools, name) {
			t.Fatalf("provider tools = %#v, missing %s", provider.request.Tools, name)
		}
	}
}

func TestStackManualApprovalRejectsBuiltinMutatingFilesystemTools(t *testing.T) {
	workspace := t.TempDir()
	rawInput, err := json.Marshal(map[string]any{
		"path":    "blocked.txt",
		"content": "blocked\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{responses: []model.Message{
		{
			Role: model.RoleAssistant,
			Parts: []model.Part{{
				Kind: model.PartToolUse,
				ToolUse: &model.ToolCall{
					ID:    "call-1",
					Name:  toolfilesystem.WriteFileToolName,
					Input: rawInput,
				},
			}},
		},
		{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("done")},
		},
	}}
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "tester",
			WorkspaceCWD: workspace,
			Sandbox:      config.Sandbox{Backend: "host"},
		},
		Provider:     provider,
		BuiltinTools: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.Services().Modes().Set(context.Background(), session.Ref{SessionID: active.SessionID}, coreruntime.SessionModeManual); err != nil {
		t.Fatal(err)
	}
	turn, err := stack.Services().Turns().Begin(context.Background(), services.BeginTurnRequest{
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "write file",
	})
	if err != nil {
		t.Fatal(err)
	}

	var events []session.Event
	var sawPending bool
	var sawRejected bool
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatal(env.Err)
		}
		event := session.CloneEvent(env.Event)
		events = append(events, event)
		if event.Approval == nil {
			continue
		}
		switch event.Approval.Status {
		case session.ApprovalPending:
			sawPending = true
			if event.Approval.Tool == nil || event.Approval.Tool.Name != toolfilesystem.WriteFileToolName {
				t.Fatalf("pending approval = %#v, want write_file tool", event.Approval)
			}
			if err := turn.Submit(context.Background(), coreruntime.Submission{
				Kind: coreruntime.SubmissionApproval,
				Approval: &coreruntime.ApprovalDecision{
					Approved: false,
					Outcome:  approval.OptionRejectOnce,
					OptionID: approval.OptionRejectOnce,
					Reason:   "blocked by test",
				},
			}); err != nil {
				t.Fatal(err)
			}
		case session.ApprovalRejected:
			sawRejected = true
		}
	}
	if !sawPending || !sawRejected {
		t.Fatalf("events = %#v, want pending and rejected approval", events)
	}
	if _, err := os.Stat(filepath.Join(workspace, "blocked.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("blocked file stat error = %v, want not exists", err)
	}
}

func TestStackAutoReviewDeniesBuiltinMutatingFilesystemTool(t *testing.T) {
	workspace := t.TempDir()
	rawInput, err := json.Marshal(map[string]any{
		"path":    "blocked.txt",
		"content": "blocked\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{responses: []model.Message{
		{
			Role: model.RoleAssistant,
			Parts: []model.Part{{
				Kind: model.PartToolUse,
				ToolUse: &model.ToolCall{
					ID:    "call-1",
					Name:  toolfilesystem.WriteFileToolName,
					Input: rawInput,
				},
			}},
		},
		{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart(`{"risk_level":"high","user_authorization":"unknown","outcome":"deny","rationale":"not authorized"}`)},
			Usage: &model.Usage{InputTokens: 8, OutputTokens: 2, TotalTokens: 10},
		},
		{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("stopped")},
		},
	}}
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "tester",
			WorkspaceCWD: workspace,
			Sandbox:      config.Sandbox{Backend: "host"},
		},
		Provider:     provider,
		BuiltinTools: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := stack.Services().Turns().Begin(context.Background(), services.BeginTurnRequest{
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "inspect files",
	})
	if err != nil {
		t.Fatal(err)
	}

	var sawPending bool
	var sawRejected bool
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatal(env.Err)
		}
		event := session.CloneEvent(env.Event)
		if event.Approval == nil {
			continue
		}
		if event.Approval.Status == session.ApprovalPending {
			sawPending = true
		}
		if event.Approval.Status == session.ApprovalRejected {
			sawRejected = true
			if !strings.Contains(event.Approval.Reason, "not authorized") {
				t.Fatalf("rejected approval = %#v, want model rationale", event.Approval)
			}
			usage, _ := event.Meta["usage"].(map[string]any)
			review, _ := event.Meta["approval_review"].(map[string]any)
			if event.Meta["usage_category"] != "auto_review" || usage["total_tokens"] != 10 || review["risk_level"] != "high" {
				t.Fatalf("rejected approval meta = %#v, want review usage and risk metadata", event.Meta)
			}
		}
	}
	if sawPending || !sawRejected {
		t.Fatalf("auto-review pending=%v rejected=%v, want direct model rejection", sawPending, sawRejected)
	}
	if _, err := os.Stat(filepath.Join(workspace, "blocked.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("blocked file stat error = %v, want not exists", err)
	}
	if len(provider.requests) < 2 || provider.requests[1].Meta["caelis.purpose"] != "approval_review" {
		t.Fatalf("provider requests = %#v, want model approval review call", provider.requests)
	}
}

func TestStackAutoReviewAsksBeforeEscalatedShellCommand(t *testing.T) {
	workspace := t.TempDir()
	rawInput, err := json.Marshal(map[string]any{
		"command":             "echo blocked > escalated.txt",
		"sandbox_permissions": "require_escalated",
		"justification":       "write git control metadata",
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{responses: []model.Message{
		{
			Role: model.RoleAssistant,
			Parts: []model.Part{{
				Kind: model.PartToolUse,
				ToolUse: &model.ToolCall{
					ID:    "call-shell",
					Name:  toolshell.RunCommandToolName,
					Input: rawInput,
				},
			}},
		},
		{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("stopped")},
		},
	}}
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "tester",
			WorkspaceCWD: workspace,
			Sandbox:      config.Sandbox{Backend: "host"},
		},
		Provider:     provider,
		BuiltinTools: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := stack.Services().Turns().Begin(context.Background(), services.BeginTurnRequest{
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "commit changes",
	})
	if err != nil {
		t.Fatal(err)
	}

	var sawPending bool
	var sawRejected bool
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatal(env.Err)
		}
		event := session.CloneEvent(env.Event)
		if event.Approval == nil {
			continue
		}
		switch event.Approval.Status {
		case session.ApprovalPending:
			sawPending = true
			if event.Approval.Tool == nil || event.Approval.Tool.Name != toolshell.RunCommandToolName {
				t.Fatalf("pending approval = %#v, want run_command tool", event.Approval)
			}
			if !strings.Contains(event.Approval.Reason, "write git control metadata") {
				t.Fatalf("pending reason = %q, want justification", event.Approval.Reason)
			}
			if err := turn.Submit(context.Background(), coreruntime.Submission{
				Kind: coreruntime.SubmissionApproval,
				Approval: &coreruntime.ApprovalDecision{
					Approved: false,
					Outcome:  approval.OptionRejectOnce,
					OptionID: approval.OptionRejectOnce,
					Reason:   "blocked by test",
				},
			}); err != nil {
				t.Fatal(err)
			}
		case session.ApprovalRejected:
			sawRejected = true
		}
	}
	if !sawPending || !sawRejected {
		t.Fatalf("approval pending=%v rejected=%v, want both", sawPending, sawRejected)
	}
	if _, err := os.Stat(filepath.Join(workspace, "escalated.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("escalated file stat error = %v, want not exists", err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("provider requests = %d, want turn request plus final follow-up without approval-review model call", len(provider.requests))
	}
}

func TestStackAutoReviewBlocksDangerousShellCommand(t *testing.T) {
	workspace := t.TempDir()
	rawInput, err := json.Marshal(map[string]any{"command": "git reset --hard HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{responses: []model.Message{
		{
			Role: model.RoleAssistant,
			Parts: []model.Part{{
				Kind: model.PartToolUse,
				ToolUse: &model.ToolCall{
					ID:    "call-shell-danger",
					Name:  toolshell.RunCommandToolName,
					Input: rawInput,
				},
			}},
		},
		{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("stopped")},
		},
	}}
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "tester",
			WorkspaceCWD: workspace,
			Sandbox:      config.Sandbox{Backend: "host"},
		},
		Provider:     provider,
		BuiltinTools: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := stack.Services().Turns().Begin(context.Background(), services.BeginTurnRequest{
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "reset repo",
	})
	if err != nil {
		t.Fatal(err)
	}

	var sawRejected bool
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatal(env.Err)
		}
		event := session.CloneEvent(env.Event)
		if event.Approval == nil || event.Approval.Status != session.ApprovalRejected {
			continue
		}
		sawRejected = true
		if !strings.Contains(event.Approval.Reason, "dangerous command") || event.Meta["dangerous_command"] != true {
			t.Fatalf("rejected approval = %#v meta=%#v, want dangerous command denial", event.Approval, event.Meta)
		}
	}
	if !sawRejected {
		t.Fatal("dangerous shell command was not rejected")
	}
	for _, req := range provider.requests {
		if req.Meta["caelis.purpose"] == "approval_review" {
			t.Fatalf("provider requests = %#v, dangerous command should not reach model approval review", provider.requests)
		}
	}
}

func TestStackManualSessionModeAsksApprovalForReadOnlyTools(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "visible.txt"), []byte("visible\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rawInput, err := json.Marshal(map[string]any{"path": "visible.txt"})
	if err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{responses: []model.Message{
		{
			Role: model.RoleAssistant,
			Parts: []model.Part{{
				Kind: model.PartToolUse,
				ToolUse: &model.ToolCall{
					ID:    "call-read",
					Name:  toolfilesystem.ReadFileToolName,
					Input: rawInput,
				},
			}},
		},
		{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("done")},
		},
	}}
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "tester",
			WorkspaceCWD: workspace,
			Sandbox:      config.Sandbox{Backend: "host"},
		},
		Provider:     provider,
		BuiltinTools: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.Services().Modes().Set(context.Background(), session.Ref{SessionID: active.SessionID}, coreruntime.SessionModeManual); err != nil {
		t.Fatal(err)
	}
	turn, err := stack.Services().Turns().Begin(context.Background(), services.BeginTurnRequest{
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "read file",
	})
	if err != nil {
		t.Fatal(err)
	}

	var sawReadApproval bool
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatal(env.Err)
		}
		event := session.CloneEvent(env.Event)
		if event.Approval == nil || event.Approval.Status != session.ApprovalPending {
			continue
		}
		sawReadApproval = true
		if event.Approval.Tool == nil || event.Approval.Tool.Name != toolfilesystem.ReadFileToolName {
			t.Fatalf("pending approval = %#v, want read_file tool", event.Approval)
		}
		if err := turn.Submit(context.Background(), coreruntime.Submission{
			Kind: coreruntime.SubmissionApproval,
			Approval: &coreruntime.ApprovalDecision{
				Approved: false,
				Outcome:  approval.OptionRejectOnce,
				OptionID: approval.OptionRejectOnce,
				Reason:   "manual review rejected",
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if !sawReadApproval {
		t.Fatal("manual mode did not request approval for read_file")
	}
}

func TestStackBuildsConfiguredOpenAIProviderAndJSONLStore(t *testing.T) {
	var captured struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"gpt-test",
			"choices":[{"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	storeDir := t.TempDir()
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName: "caelis",
			UserID:  "tester",
			Model:   "gpt-test",
			Store:   config.Store{Backend: "jsonl", URI: storeDir},
		},
		Model: config.ModelProfile{
			Provider: "openai_compatible",
			BaseURL:  server.URL + "/v1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := stack.Services().Turns().Begin(context.Background(), services.BeginTurnRequest{
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatal(env.Err)
		}
	}
	if captured.Model != "gpt-test" || len(captured.Messages) < 2 || captured.Messages[0].Role != "system" || captured.Messages[len(captured.Messages)-1].Role != "user" {
		t.Fatalf("captured request = %#v", captured)
	}

	reloaded, err := New(Config{
		Runtime: config.Runtime{
			AppName: "caelis",
			UserID:  "tester",
			Model:   "gpt-test",
			Store:   config.Store{Backend: "jsonl", URI: storeDir},
		},
		Model: config.ModelProfile{
			Provider: "openai_compatible",
			BaseURL:  server.URL + "/v1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reloaded.Services().Sessions().Load(context.Background(), session.Ref{SessionID: active.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 2 || session.EventText(snapshot.Events[1]) != "pong" {
		t.Fatalf("reloaded events = %#v, want persisted ping/pong", snapshot.Events)
	}
}

func TestStackRoutesConfiguredSettingsModelAtTurnTime(t *testing.T) {
	var captured []struct {
		Model string `json:"model"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		var req struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		captured = append(captured, req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"gpt-settings",
			"choices":[{"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	manager, err := appsettings.NewManager(context.Background(), nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := manager.UpsertModel(context.Background(), appsettings.ModelConfig{
		Provider: "openai_compatible",
		Model:    "gpt-settings",
		BaseURL:  server.URL + "/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	stack, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester"},
		Settings: manager,
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.Services().Models().Use(context.Background(), session.Ref{SessionID: active.SessionID}, cfg.ID, ""); err != nil {
		t.Fatal(err)
	}
	turn, err := stack.Services().Turns().Begin(context.Background(), services.BeginTurnRequest{
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatal(env.Err)
		}
	}
	if len(captured) != 1 || captured[0].Model != "gpt-settings" {
		t.Fatalf("captured requests = %#v, want routed raw model", captured)
	}
}

func TestStackBuildsConfiguredSQLiteStore(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	provider := &capturingProvider{message: model.Message{
		Role:  model.RoleAssistant,
		Parts: []model.Part{model.NewTextPart("pong")},
	}}
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName: "caelis",
			UserID:  "tester",
			Store:   config.Store{Backend: "sqlite", URI: dbPath},
		},
		Provider: provider,
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := stack.Services().Turns().Begin(context.Background(), services.BeginTurnRequest{
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatal(env.Err)
		}
	}
	if closer, ok := stack.Store().(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil {
			t.Fatal(err)
		}
	}

	reloaded, err := New(Config{
		Runtime: config.Runtime{
			AppName: "caelis",
			UserID:  "tester",
			Store:   config.Store{Backend: "sqlite", URI: dbPath},
		},
		Provider: &capturingProvider{message: model.Message{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("unused")},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok := reloaded.Store().(interface{ Close() error }); ok {
		t.Cleanup(func() { _ = closer.Close() })
	}
	snapshot, err := reloaded.Services().Sessions().Load(context.Background(), session.Ref{SessionID: active.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 2 || session.EventText(snapshot.Events[0]) != "ping" || session.EventText(snapshot.Events[1]) != "pong" {
		t.Fatalf("reloaded events = %#v, want persisted ping/pong", snapshot.Events)
	}
}

func TestStackDiscoversPluginAndWorkspaceResources(t *testing.T) {
	workspace := t.TempDir()
	pluginDir := filepath.Join(t.TempDir(), "plugin")
	if err := os.MkdirAll(filepath.Join(pluginDir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("workspace rule"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "prompts", "review.md"), []byte("review prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{
		"id":"reviewer",
		"name":"Reviewer",
		"prompts":[{"id":"reviewer.system","priority":50,"paths":["prompts/review.md"]}]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := &capturingProvider{message: model.Message{
		Role:  model.RoleAssistant,
		Parts: []model.Part{model.NewTextPart("done")},
	}}
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "tester",
			WorkspaceCWD: workspace,
			Plugins: []config.Plugin{{
				Source:  pluginDir,
				Enabled: true,
			}},
		},
		Provider:     provider,
		SystemPrompt: "session override",
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := stack.Services().Resources().Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !hasPrompt(catalog.Prompts, "reviewer.system") {
		t.Fatalf("prompts = %#v, missing plugin prompt", catalog.Prompts)
	}
	if !hasPrompt(catalog.Prompts, "agents.workspace") {
		t.Fatalf("prompts = %#v, missing workspace AGENTS prompt", catalog.Prompts)
	}
	if len(catalog.Plugins) != 1 || catalog.Plugins[0].Manifest.ID != "reviewer" {
		t.Fatalf("plugins = %#v, want reviewer plugin", catalog.Plugins)
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := stack.Services().Turns().Begin(context.Background(), services.BeginTurnRequest{
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatal(env.Err)
		}
	}
	joined := strings.Join(provider.request.Instructions, "\n\n")
	for _, want := range []string{"review prompt", "workspace rule", "session override", "<environment_context>", "You are caelis"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("instructions = %q, missing %q", joined, want)
		}
	}
}

func TestStackAppliesSkillPolicyToPromptAssembly(t *testing.T) {
	workspace := t.TempDir()
	skillDir := filepath.Join(workspace, ".agents", "skills", "review")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: review\ndescription: Review code\n---\n# Review\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := appsettings.NewManager(context.Background(), nil, appsettings.Document{
		Skills: appsettings.SkillPolicy{LoadingMode: appsettings.SkillLoadingModeDisabled},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &capturingProvider{message: model.Message{
		Role:  model.RoleAssistant,
		Parts: []model.Part{model.NewTextPart("done")},
	}}
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "tester",
			WorkspaceCWD: workspace,
		},
		Provider: provider,
		Settings: manager,
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := stack.Services().Turns().Begin(context.Background(), services.BeginTurnRequest{
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatal(env.Err)
		}
	}
	joined := strings.Join(provider.request.Instructions, "\n\n")
	if strings.Contains(joined, "## Skills") || strings.Contains(joined, "Review code") {
		t.Fatalf("instructions = %q, want skill metadata disabled", joined)
	}
}

func TestStackAddsSkillRootsToSandboxConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := filepath.Join(t.TempDir(), "workspace")
	factory := &capturingSandboxFactory{}
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "tester",
			WorkspaceCWD: workspace,
			Sandbox: config.Sandbox{
				Backend:       "capture",
				WritableRoots: []string{"/configured-write"},
			},
		},
		Provider: &capturingProvider{message: model.Message{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("unused")},
		}},
		Contributions: []plugin.Contribution{
			contributionFunc(func(_ context.Context, registry plugin.Registry) error {
				return registry.RegisterSandboxBackend("capture", factory)
			}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stack.Sandbox() == nil {
		t.Fatal("stack sandbox = nil, want configured capture runtime")
	}
	for _, root := range []string{
		"/configured-write",
		filepath.Join(home, ".caelis", "skills", ".system"),
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(home, ".caelis", "skills"),
		filepath.Join(workspace, "skills"),
		filepath.Join(workspace, ".agents", "skills"),
	} {
		if !slices.Contains(factory.cfg.WritableRoots, root) {
			t.Fatalf("sandbox writable roots = %#v, want %q", factory.cfg.WritableRoots, root)
		}
	}
}

func TestStackInvokesPluginDeclaredACPAgent(t *testing.T) {
	pluginDir := filepath.Join(t.TempDir(), "plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(map[string]any{
		"id":   "helper-plugin",
		"name": "Helper Plugin",
		"acp_agents": []map[string]any{{
			"name":    "plugin-helper",
			"command": executable,
			"args":    []string{"-test.run=TestExternalACPHelperProcess", "--"},
			"env":     map[string]string{"CAELIS_TEST_EXTERNAL_ACP_HELPER": "1"},
			"roles":   []string{"participant"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName: "caelis",
			UserID:  "tester",
			Plugins: []config.Plugin{{
				Source:  pluginDir,
				Enabled: true,
			}},
		},
		Provider: &capturingProvider{message: model.Message{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("unused")},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	agents, err := stack.Services().Agents().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].ID != "plugin-helper" || agents[0].WorkDir != pluginDir {
		t.Fatalf("agents = %#v, want plugin-helper with plugin workdir", agents)
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := stack.Services().Agents().Invoke(context.Background(), services.AgentInvokeRequest{
		AgentID:    "plugin-helper",
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "delegate",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 || session.EventText(result.Events[0]) != "external helper response" {
		t.Fatalf("invoke result = %#v, want helper response", result)
	}
	snapshot, err := stack.Services().Sessions().Load(context.Background(), session.Ref{SessionID: active.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 1 || snapshot.Events[0].Scope == nil || snapshot.Events[0].Scope.Participant.ID != "plugin-helper" {
		t.Fatalf("stored events = %#v, want plugin helper participant event", snapshot.Events)
	}
}

func TestStackLoadsSettingsBackedACPAgent(t *testing.T) {
	ctx := context.Background()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{
		Agents: []plugin.ACPAgentDescriptor{{
			Name:    "settings-helper",
			Command: executable,
			Args:    []string{"-test.run=TestExternalACPHelperProcess", "--"},
			Env:     map[string]string{"CAELIS_TEST_EXTERNAL_ACP_HELPER": "1"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName: "caelis",
			UserID:  "tester",
		},
		Provider: &capturingProvider{message: model.Message{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("unused")},
		}},
		Settings: manager,
	})
	if err != nil {
		t.Fatal(err)
	}
	agents, err := stack.Services().Agents().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].ID != "settings-helper" || agents[0].Env["CAELIS_TEST_EXTERNAL_ACP_HELPER"] != "1" {
		t.Fatalf("agents = %#v, want settings-backed helper", agents)
	}
	active, err := stack.Services().Sessions().Start(ctx, services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := stack.Services().Agents().Invoke(ctx, services.AgentInvokeRequest{
		AgentID:    "settings-helper",
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "delegate",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 || session.EventText(result.Events[0]) != "external helper response" {
		t.Fatalf("invoke result = %#v, want helper response", result)
	}
}

func TestStackExposesBuiltinACPAgentCatalog(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName: "caelis",
			UserID:  "tester",
		},
		Provider: &capturingProvider{message: model.Message{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("unused")},
		}},
		Settings: manager,
	})
	if err != nil {
		t.Fatal(err)
	}
	builtins, err := stack.Services().Agents().ListBuiltins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !agentDescriptorsHave(builtins, "codex") || !agentDescriptorsHave(builtins, "copilot") {
		t.Fatalf("builtins = %#v, want codex and copilot catalog entries", builtins)
	}
	registered, err := stack.Services().Agents().RegisterBuiltin(ctx, "copilot")
	if err != nil {
		t.Fatal(err)
	}
	if registered.ID != "copilot" || registered.Command != "copilot" {
		t.Fatalf("registered = %#v, want copilot builtin descriptor", registered)
	}
	if agents := manager.ListACPAgents(); len(agents) != 1 || agents[0].Name != "copilot" {
		t.Fatalf("settings agents = %#v, want persisted builtin copilot", agents)
	}
}

func TestStackInstallsBuiltinACPAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake npm script uses POSIX shell")
	}
	ctx := context.Background()
	storeDir := t.TempDir()
	binDir := t.TempDir()
	writeExecutableForLocalTest(t, binDir, "npm", `#!/bin/sh
set -eu
prefix=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--prefix" ]; then
    shift
    prefix="$1"
  fi
  shift || true
done
mkdir -p "$prefix/node_modules/.bin"
cat > "$prefix/node_modules/.bin/codex-acp" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod +x "$prefix/node_modules/.bin/codex-acp"
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName: "caelis",
			UserID:  "tester",
			Store:   config.Store{Backend: "jsonl", URI: storeDir},
		},
		Provider: &capturingProvider{message: model.Message{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("unused")},
		}},
		Settings: manager,
	})
	if err != nil {
		t.Fatal(err)
	}
	options, err := stack.Services().Agents().ListInstallableBuiltins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !agentInstallOptionsHave(options, "codex") || agentInstallOptionsHave(options, "copilot") {
		t.Fatalf("installable options = %#v, want codex but not copilot", options)
	}
	registered, err := stack.Services().Agents().RegisterBuiltinWithOptions(ctx, "codex", services.RegisterBuiltinAgentOptions{Install: true})
	if err != nil {
		t.Fatal(err)
	}
	wantCommand := filepath.Join(storeDir, "acp-agents", "npm", "node_modules", ".bin", "codex-acp")
	if registered.Command != wantCommand || len(registered.Args) != 0 {
		t.Fatalf("registered = %#v, want installed command %q without args", registered, wantCommand)
	}
	if agents := manager.ListACPAgents(); len(agents) != 1 || agents[0].Name != "codex" || agents[0].Command != wantCommand || len(agents[0].Args) != 0 {
		t.Fatalf("settings agents = %#v, want installed codex", agents)
	}
}

func TestStackAppliesPluginContributionStoreFactory(t *testing.T) {
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName: "caelis",
			UserID:  "tester",
			Store:   config.Store{Backend: "contrib-memory"},
		},
		Provider: &capturingProvider{message: model.Message{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("unused")},
		}},
		Contributions: []plugin.Contribution{
			contributionFunc(func(_ context.Context, registry plugin.Registry) error {
				return registry.RegisterStore("contrib-memory", func(context.Context, plugin.StoreConfig) (session.Store, error) {
					return storememory.New(), nil
				})
			}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stack.Store().(*storememory.Store); !ok {
		t.Fatalf("store = %T, want contributed memory store", stack.Store())
	}
}

func TestStackAppliesManifestDeclaredStoreAlias(t *testing.T) {
	pluginDir := filepath.Join(t.TempDir(), "plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{
		"id":"storage-plugin",
		"stores":[{"name":"plugin-memory","uses":"memory"}]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName: "caelis",
			UserID:  "tester",
			Store:   config.Store{Backend: "plugin-memory"},
			Plugins: []config.Plugin{{
				Source:  pluginDir,
				Enabled: true,
			}},
		},
		Provider: &capturingProvider{message: model.Message{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("unused")},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stack.Store().(*storememory.Store); !ok {
		t.Fatalf("store = %T, want plugin-declared memory alias", stack.Store())
	}
	catalog, err := stack.Services().Resources().Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Stores) != 1 || catalog.Stores[0].Name != "plugin-memory" || catalog.Stores[0].Uses != "memory" {
		t.Fatalf("catalog stores = %#v, want plugin-memory alias", catalog.Stores)
	}
}

func TestConfiguredStackRunsBuiltinShellTool(t *testing.T) {
	var calls atomic.Int32
	command := "printf hello"
	if runtime.GOOS == "windows" {
		command = "echo hello"
	}
	rawInput, err := json.Marshal(map[string]any{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var captured struct {
			Messages []struct {
				Role       string `json:"role"`
				ToolCallID string `json:"tool_call_id"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		call := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch call {
		case 1:
			if !capturedOpenAITool(captured.Tools, toolshell.RunCommandToolName) {
				t.Fatalf("tools = %#v, want run_command", captured.Tools)
			}
			_, _ = w.Write([]byte(`{
				"model":"gpt-test",
				"choices":[{
					"message":{
						"role":"assistant",
						"tool_calls":[{
							"id":"call-1",
							"type":"function",
							"function":{"name":"run_command","arguments":` + strconvQuote(string(rawInput)) + `}
						}]
					},
					"finish_reason":"tool_calls"
				}]
			}`))
		case 2:
			if len(captured.Messages) == 0 || captured.Messages[len(captured.Messages)-1].Role != "tool" || captured.Messages[len(captured.Messages)-1].ToolCallID != "call-1" {
				t.Fatalf("second request messages = %#v, want final tool result message", captured.Messages)
			}
			_, _ = w.Write([]byte(`{
				"model":"gpt-test",
				"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]
			}`))
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	}))
	defer server.Close()

	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "tester",
			Model:        "gpt-test",
			WorkspaceCWD: t.TempDir(),
			Sandbox:      config.Sandbox{Backend: "host"},
		},
		Model: config.ModelProfile{
			Provider: "openai_compatible",
			BaseURL:  server.URL,
		},
		BuiltinTools: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := stack.Services().Turns().Begin(context.Background(), services.BeginTurnRequest{
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "run command",
	})
	if err != nil {
		t.Fatal(err)
	}
	var events []session.Event
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatal(env.Err)
		}
		events = append(events, env.Event)
	}
	if len(events) != 5 || events[3].Type != session.EventToolResult || !strings.Contains(session.EventText(events[3]), "hello") {
		t.Fatalf("events = %#v, want shell tool result", events)
	}
	if got := session.EventText(events[4]); got != "done" {
		t.Fatalf("final assistant text = %q, want done", got)
	}
}

func strconvQuote(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func capturedOpenAITool(tools []struct {
	Function struct {
		Name string `json:"name"`
	} `json:"function"`
}, name string) bool {
	for _, item := range tools {
		if item.Function.Name == name {
			return true
		}
	}
	return false
}

func hasPrompt(prompts []plugin.PromptFragment, id string) bool {
	for _, prompt := range prompts {
		if prompt.ID == id {
			return true
		}
	}
	return false
}

func agentDescriptorsHave(agents []services.AgentDescriptor, id string) bool {
	for _, agent := range agents {
		if strings.EqualFold(strings.TrimSpace(agent.ID), id) || strings.EqualFold(strings.TrimSpace(agent.Name), id) {
			return true
		}
	}
	return false
}

func agentInstallOptionsHave(options []services.AgentInstallOption, value string) bool {
	for _, option := range options {
		if strings.EqualFold(strings.TrimSpace(option.Value), value) {
			return true
		}
	}
	return false
}

func stringSliceHas(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func writeExecutableForLocalTest(t *testing.T, dir string, name string, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("os.WriteFile(%s) error = %v", path, err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("os.Chmod(%s) error = %v", path, err)
	}
	return path
}

func capturedTool(tools []model.ToolSpec, name string) bool {
	for _, item := range tools {
		if item.Name == name {
			return true
		}
	}
	return false
}

func findToolResult(events []session.Event, name string) *session.Event {
	for idx := range events {
		event := &events[idx]
		if event.Type == session.EventToolResult && event.Tool != nil && event.Tool.Name == name {
			return event
		}
	}
	return nil
}

func findSubagentEvent(events []session.Event, delegationID string) *session.Event {
	for idx := range events {
		event := &events[idx]
		if event.Scope == nil || event.Scope.Participant.Kind != session.ParticipantSubagent {
			continue
		}
		if event.Scope.Participant.DelegationID == delegationID {
			return event
		}
	}
	return nil
}

func findTaskView(items []appviewmodel.TaskItem, id string) (appviewmodel.TaskItem, bool) {
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return appviewmodel.TaskItem{}, false
}

func nestedRuntimeTask(meta map[string]any) map[string]any {
	caelis, _ := meta["caelis"].(map[string]any)
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	taskMeta, _ := runtimeMeta["task"].(map[string]any)
	return taskMeta
}

type capturingProvider struct {
	request model.Request
	message model.Message
}

type capturingSandboxFactory struct {
	cfg sandbox.Config
}

func (f *capturingSandboxFactory) NewRuntime(ctx context.Context, cfg sandbox.Config) (sandbox.Runtime, error) {
	f.cfg = sandbox.Config{
		CWD:                 cfg.CWD,
		RequestedBackend:    cfg.RequestedBackend,
		BackendCandidates:   append([]sandbox.Backend(nil), cfg.BackendCandidates...),
		FallbackInstallHint: cfg.FallbackInstallHint,
		HelperPath:          cfg.HelperPath,
		StateDir:            cfg.StateDir,
		ReadableRoots:       append([]string(nil), cfg.ReadableRoots...),
		WritableRoots:       append([]string(nil), cfg.WritableRoots...),
		ReadOnlySubpaths:    append([]string(nil), cfg.ReadOnlySubpaths...),
	}
	return sandboxhost.New(ctx, cfg)
}

func (p *capturingProvider) ID() string {
	return "test-provider"
}

func (p *capturingProvider) Models(context.Context) ([]model.ModelInfo, error) {
	return []model.ModelInfo{{ID: "gpt-test", Provider: "test-provider"}}, nil
}

func (p *capturingProvider) Stream(_ context.Context, req model.Request) (model.Stream, error) {
	p.request = model.Request{
		Model:        req.Model,
		Messages:     cloneMessages(req.Messages),
		Tools:        req.Tools,
		Instructions: append([]string(nil), req.Instructions...),
		Stream:       req.Stream,
	}
	return &model.StaticStream{Events: []model.StreamEvent{{
		Type: model.StreamTurnDone,
		Response: &model.Response{
			Status:  model.ResponseCompleted,
			Message: model.CloneMessage(p.message),
		},
	}}}, nil
}

func cloneMessages(in []model.Message) []model.Message {
	if len(in) == 0 {
		return nil
	}
	out := make([]model.Message, 0, len(in))
	for _, message := range in {
		out = append(out, model.CloneMessage(message))
	}
	return out
}

type scriptedProvider struct {
	requests  []model.Request
	responses []model.Message
}

type catalogTestProvider struct {
	models []model.ModelInfo
}

func (p catalogTestProvider) ID() string {
	return "catalog-test"
}

func (p catalogTestProvider) Models(context.Context) ([]model.ModelInfo, error) {
	return append([]model.ModelInfo(nil), p.models...), nil
}

func (catalogTestProvider) Stream(context.Context, model.Request) (model.Stream, error) {
	return &model.StaticStream{}, nil
}

type contributionFunc func(context.Context, plugin.Registry) error

func (f contributionFunc) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: "test"}
}

func (f contributionFunc) Register(ctx context.Context, registry plugin.Registry) error {
	return f(ctx, registry)
}

func (p *scriptedProvider) ID() string {
	return "scripted"
}

func (p *scriptedProvider) Models(context.Context) ([]model.ModelInfo, error) {
	return []model.ModelInfo{{ID: "scripted", Provider: "scripted"}}, nil
}

func (p *scriptedProvider) Stream(_ context.Context, req model.Request) (model.Stream, error) {
	p.requests = append(p.requests, model.Request{
		Model:    req.Model,
		Messages: cloneMessages(req.Messages),
		Tools:    req.Tools,
		Stream:   req.Stream,
		Output:   req.Output,
		Meta:     maps.Clone(req.Meta),
	})
	response := model.Message{
		Role:  model.RoleAssistant,
		Parts: []model.Part{model.NewTextPart("default")},
	}
	if len(p.responses) > 0 {
		response = model.CloneMessage(p.responses[0])
		p.responses = p.responses[1:]
	}
	return &model.StaticStream{Events: []model.StreamEvent{{
		Type: model.StreamTurnDone,
		Response: &model.Response{
			Status:  model.ResponseCompleted,
			Message: response,
		},
	}}}, nil
}

func schemaEnumHas(raw any, want string) bool {
	want = strings.TrimSpace(want)
	switch values := raw.(type) {
	case []any:
		for _, value := range values {
			if text, ok := value.(string); ok && text == want {
				return true
			}
		}
	case []string:
		for _, value := range values {
			if value == want {
				return true
			}
		}
	}
	return false
}
