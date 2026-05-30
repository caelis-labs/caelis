package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/core/config"
	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/plugin"
	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/session"
	appresources "github.com/OnslaughtSnail/caelis/internal/app/resources"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
)

func TestNewRequiresEngine(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New without engine error = nil, want error")
	}
}

func TestServicesApplyRuntimeDefaults(t *testing.T) {
	engine := &recordingEngine{}
	svc, err := New(Config{
		Runtime: config.Runtime{
			AppName:      "caelis-app",
			UserID:       "tester",
			WorkspaceKey: "repo",
			WorkspaceCWD: "/tmp/repo",
		},
		Engine: engine,
		Agents: []AgentDescriptor{{
			ID:      "reviewer",
			Name:    "Reviewer",
			Kind:    AgentKindExternalACP,
			Command: "reviewer-acp",
			Args:    []string{"--stdio"},
			Meta:    map[string]string{"scope": "workspace"},
		}},
		Invokers: map[string]AgentInvoker{
			"reviewer": AgentInvokerFunc(func(_ context.Context, req AgentInvokeRequest) (AgentInvokeResult, error) {
				return AgentInvokeResult{
					StopReason: "end_turn",
					Events: []session.Event{{
						Type: session.EventAssistant,
						Message: &model.Message{
							Role:  model.RoleAssistant,
							Parts: []model.Part{model.NewTextPart("agent result for " + req.Input)},
						},
					}},
				}, nil
			}),
		},
		Resources: appresources.Catalog{
			Prompts: []plugin.PromptFragment{{
				ID:    "agents.workspace",
				Text:  "workspace rule",
				Paths: []string{"AGENTS.md"},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	active, err := svc.Sessions().Start(context.Background(), StartSessionRequest{Title: "scratch"})
	if err != nil {
		t.Fatal(err)
	}
	if active.AppName != "caelis-app" || active.UserID != "tester" {
		t.Fatalf("session ref = %#v, want app/user defaults", active.Ref)
	}
	if engine.start.Workspace.Key != "repo" || engine.start.Workspace.CWD != "/tmp/repo" {
		t.Fatalf("workspace = %#v, want runtime defaults", engine.start.Workspace)
	}

	meta := map[string]any{"source": "test"}
	parts := []model.ContentPart{{Type: model.ContentPartText, Text: " ping "}}
	_, err = svc.Turns().Begin(context.Background(), BeginTurnRequest{
		SessionRef:   session.Ref{SessionID: active.SessionID},
		Input:        "  ping  ",
		ContentParts: parts,
		Model:        "gpt-test",
		Surface:      "tui",
		Meta:         meta,
	})
	if err != nil {
		t.Fatal(err)
	}
	parts[0].Text = "changed"
	meta["source"] = "changed"

	if got := engine.turn.SessionRef; got.AppName != "caelis-app" || got.UserID != "tester" || got.WorkspaceKey != "repo" {
		t.Fatalf("turn ref = %#v, want default app/user/workspace", got)
	}
	if engine.turn.Input != "  ping  " {
		t.Fatalf("turn input = %q, want preserved text", engine.turn.Input)
	}
	if got := engine.turn.ContentParts[0].Text; got != "ping" {
		t.Fatalf("content part text = %q, want cloned normalized text", got)
	}
	if got := engine.turn.Meta["source"]; got != "test" {
		t.Fatalf("turn meta source = %v, want cloned value", got)
	}

	agents, err := svc.Agents().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].ID != "reviewer" || agents[0].Kind != AgentKindExternalACP {
		t.Fatalf("agents = %#v, want reviewer external ACP", agents)
	}
	agents[0].Args[0] = "changed"
	again, err := svc.Agents().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if again[0].Args[0] != "--stdio" {
		t.Fatalf("agent list was not cloned: %#v", again[0].Args)
	}
	invoked, err := svc.Agents().Invoke(context.Background(), AgentInvokeRequest{
		AgentID:    "reviewer",
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "inspect",
	})
	if err != nil {
		t.Fatal(err)
	}
	if invoked.StopReason != "end_turn" || len(invoked.Events) != 1 {
		t.Fatalf("invoke result = %#v, want one event", invoked)
	}
	if len(engine.events) != 1 || session.EventText(engine.events[0]) != "agent result for inspect" {
		t.Fatalf("recorded events = %#v, want agent result", engine.events)
	}
	catalog, err := svc.Resources().Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Prompts) != 1 || catalog.Prompts[0].ID != "agents.workspace" {
		t.Fatalf("resources = %#v, want workspace prompt", catalog)
	}
	catalog.Prompts[0].Paths[0] = "changed"
	catalog, err = svc.Resources().Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Prompts[0].Paths[0] != "AGENTS.md" {
		t.Fatalf("resource catalog was not cloned: %#v", catalog.Prompts[0].Paths)
	}
}

func TestAgentServiceRegistersCustomSettingsBackedAgent(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	engine := &recordingEngine{}
	var factoryAgent AgentDescriptor
	svc, err := New(Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "tester",
			WorkspaceKey: "repo",
		},
		Engine:   engine,
		Settings: manager,
		InvokerFactory: func(agent AgentDescriptor) (AgentInvoker, error) {
			factoryAgent = agent
			return AgentInvokerFunc(func(_ context.Context, req AgentInvokeRequest) (AgentInvokeResult, error) {
				return AgentInvokeResult{
					Events: []session.Event{{
						Type: session.EventAssistant,
						Message: &model.Message{
							Role:  model.RoleAssistant,
							Parts: []model.Part{model.NewTextPart("custom result for " + req.Input)},
						},
					}},
				}, nil
			}), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Agents().RegisterCustom(ctx, AgentDescriptor{Name: "status", Command: "bad"}); err == nil {
		t.Fatal("RegisterCustom(status) error = nil, want reserved slash command conflict")
	}
	registered, err := svc.Agents().RegisterCustom(ctx, AgentDescriptor{
		Name:        " Helper ",
		Description: "review code",
		Command:     "helper-acp",
		Args:        []string{"--stdio"},
		Env:         map[string]string{"HELPER_TOKEN": "secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if registered.ID != "helper" || registered.Name != "helper" || registered.Env["HELPER_TOKEN"] != "secret" {
		t.Fatalf("registered = %#v, want normalized helper", registered)
	}
	agents, err := svc.Agents().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].ID != "helper" || agents[0].Command != "helper-acp" {
		t.Fatalf("agents = %#v, want settings-backed helper", agents)
	}
	result, err := svc.Agents().Invoke(ctx, AgentInvokeRequest{
		AgentID:    "helper",
		SessionRef: session.Ref{SessionID: "sess-agent"},
		Input:      "inspect",
	})
	if err != nil {
		t.Fatal(err)
	}
	if factoryAgent.ID != "helper" || factoryAgent.Env["HELPER_TOKEN"] != "secret" {
		t.Fatalf("factory agent = %#v, want helper descriptor with env", factoryAgent)
	}
	if len(result.Events) != 1 || session.EventText(result.Events[0]) != "custom result for inspect" {
		t.Fatalf("invoke result = %#v, want custom agent response", result)
	}
	if len(engine.events) != 1 || engine.events[0].Scope == nil || engine.events[0].Scope.Participant.ID != "helper" {
		t.Fatalf("recorded events = %#v, want helper participant event", engine.events)
	}
	if err := svc.Agents().Remove(ctx, "helper"); err != nil {
		t.Fatal(err)
	}
	if agents, err := svc.Agents().List(ctx); err != nil || len(agents) != 0 {
		t.Fatalf("agents after remove = %#v err=%v, want none", agents, err)
	}
}

func TestAgentServiceRegistersBuiltinAgent(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester"},
		Engine:   &recordingEngine{},
		Settings: manager,
		BuiltinAgents: []AgentDescriptor{{
			ID:          "copilot",
			Name:        "copilot",
			Kind:        AgentKindExternalACP,
			Description: "GitHub Copilot ACP agent",
			Command:     "copilot",
			Args:        []string{"--acp", "--stdio"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	builtins, err := svc.Agents().ListBuiltins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(builtins) != 1 || builtins[0].ID != "copilot" || builtins[0].Description == "" {
		t.Fatalf("builtins = %#v, want copilot", builtins)
	}
	registered, err := svc.Agents().RegisterBuiltin(ctx, "copilot")
	if err != nil {
		t.Fatal(err)
	}
	if registered.ID != "copilot" || registered.Command != "copilot" || registered.Args[1] != "--stdio" {
		t.Fatalf("registered = %#v, want copilot command", registered)
	}
	agents, err := svc.Agents().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].ID != "copilot" || agents[0].Description != "GitHub Copilot ACP agent" {
		t.Fatalf("agents = %#v, want registered builtin copilot", agents)
	}
	if _, err := svc.Agents().RegisterBuiltin(ctx, "missing"); err == nil {
		t.Fatal("RegisterBuiltin(missing) error = nil, want unknown builtin error")
	}
}

func TestAgentServiceInstallsBuiltinACPAgentThroughInstaller(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	installer := &recordingAgentInstaller{}
	svc, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester"},
		Engine:   &recordingEngine{},
		Settings: manager,
		BuiltinAgents: []AgentDescriptor{{
			ID:          "codex",
			Name:        "codex",
			Kind:        AgentKindExternalACP,
			Description: "OpenAI Codex ACP agent",
			Command:     "npx",
			Args:        []string{"-y", "@zed-industries/codex-acp"},
		}},
		AgentInstaller: installer,
	})
	if err != nil {
		t.Fatal(err)
	}
	options, err := svc.Agents().ListInstallableBuiltins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 1 || options[0].Value != "codex" {
		t.Fatalf("installable options = %#v, want codex", options)
	}
	registered, err := svc.Agents().RegisterBuiltinWithOptions(ctx, "codex", RegisterBuiltinAgentOptions{Install: true})
	if err != nil {
		t.Fatal(err)
	}
	if !installer.called || installer.agent.Name != "codex" {
		t.Fatalf("installer called=%v agent=%#v, want codex", installer.called, installer.agent)
	}
	if registered.Command != "/installed/codex-acp" || len(registered.Args) != 0 {
		t.Fatalf("registered = %#v, want installed command without args", registered)
	}
	if agents := manager.ListACPAgents(); len(agents) != 1 || agents[0].Command != "/installed/codex-acp" || len(agents[0].Args) != 0 {
		t.Fatalf("settings agents = %#v, want installed codex", agents)
	}
}

func TestAgentServiceInvokeControllerRecordsControllerScopedEvents(t *testing.T) {
	ctx := context.Background()
	engine := &recordingEngine{}
	svc, err := New(Config{
		Runtime: config.Runtime{AppName: "caelis", UserID: "tester", WorkspaceKey: "repo"},
		Engine:  engine,
		Agents: []AgentDescriptor{{
			ID:      "reviewer",
			Name:    "reviewer",
			Kind:    AgentKindExternalACP,
			Command: "reviewer-acp",
		}},
		Invokers: map[string]AgentInvoker{
			"reviewer": AgentInvokerFunc(func(_ context.Context, req AgentInvokeRequest) (AgentInvokeResult, error) {
				if req.Controller.Kind != session.ControllerACP || req.Controller.ID != "reviewer" {
					t.Fatalf("invoke controller = %#v, want reviewer ACP controller", req.Controller)
				}
				return AgentInvokeResult{
					Events: []session.Event{{
						Type: session.EventAssistant,
						Message: &model.Message{
							Role:  model.RoleAssistant,
							Parts: []model.Part{model.NewTextPart("controller result")},
						},
					}},
				}, nil
			}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.Agents().Invoke(ctx, AgentInvokeRequest{
		AgentID:    "reviewer",
		SessionRef: session.Ref{SessionID: "sess-controller"},
		Controller: session.ControllerBinding{
			Kind: session.ControllerACP,
			ID:   "reviewer",
		},
		Input: "inspect",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 || result.Events[0].Scope == nil || result.Events[0].Scope.Controller.ID != "reviewer" {
		t.Fatalf("result events = %#v, want controller-scoped event", result.Events)
	}
	if len(engine.events) != 1 || engine.events[0].Actor.Kind != session.ActorController || engine.events[0].Scope == nil || engine.events[0].Scope.Controller.ID != "reviewer" {
		t.Fatalf("recorded events = %#v, want controller-scoped event", engine.events)
	}
}

func TestAgentServiceInvokeControllerIncludesConfigIntent(t *testing.T) {
	ctx := context.Background()
	controller := session.ControllerBinding{
		Kind:            session.ControllerACP,
		ID:              "reviewer",
		AgentName:       "reviewer",
		EpochID:         "controller-1",
		RemoteSessionID: "remote-reviewer",
	}
	state := session.State{
		StateControllerConfigRef: "controller-1",
		StateControllerModel:     "remote-model",
		StateControllerReasoning: "high",
		StateControllerMode:      "manual",
	}
	engine := &recordingEngine{
		state: state,
		snapshot: session.Snapshot{
			Session: session.Session{
				Ref:        session.Ref{AppName: "caelis", UserID: "tester", SessionID: "sess-controller", WorkspaceKey: "repo"},
				Controller: controller,
			},
			State: state,
		},
	}
	svc, err := New(Config{
		Runtime: config.Runtime{AppName: "caelis", UserID: "tester", WorkspaceKey: "repo"},
		Engine:  engine,
		Invokers: map[string]AgentInvoker{
			"reviewer": AgentInvokerFunc(func(_ context.Context, req AgentInvokeRequest) (AgentInvokeResult, error) {
				if req.ControllerModel != "remote-model" || req.ControllerReasoningEffort != "high" || req.ControllerMode != "manual" {
					t.Fatalf("controller config intent = model:%q reasoning:%q mode:%q, want remote-model/high/manual", req.ControllerModel, req.ControllerReasoningEffort, req.ControllerMode)
				}
				return AgentInvokeResult{}, nil
			}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Agents().Invoke(ctx, AgentInvokeRequest{
		AgentID:    "reviewer",
		SessionRef: session.Ref{SessionID: "sess-controller"},
		Controller: controller,
		Input:      "inspect",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestControllerServicePersistsACPControllerConfigIntent(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := manager.UpsertModel(ctx, appsettings.ModelConfig{
		Alias:           "remote-model",
		Provider:        "openai-compatible",
		Model:           "gpt-test",
		BaseURL:         "https://api.example.test/v1",
		ReasoningMode:   "fixed",
		ReasoningLevels: []string{"low", "high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	controller := session.ControllerBinding{
		Kind:            session.ControllerACP,
		ID:              "reviewer",
		AgentName:       "reviewer",
		EpochID:         "controller-1",
		RemoteSessionID: "remote-reviewer",
	}
	engine := &recordingEngine{
		state: session.State{},
		snapshot: session.Snapshot{
			Session: session.Session{
				Ref:        session.Ref{AppName: "caelis", UserID: "tester", SessionID: "sess-controller", WorkspaceKey: "repo"},
				Controller: controller,
			},
			State: session.State{},
		},
	}
	svc, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester", WorkspaceKey: "repo"},
		Engine:   engine,
		Settings: manager,
	})
	if err != nil {
		t.Fatal(err)
	}
	status, ok, err := svc.Controllers().Status(ctx, session.Ref{SessionID: "sess-controller"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status.Agent != "reviewer" || status.RemoteSessionID != "remote-reviewer" {
		t.Fatalf("controller status = %#v ok=%v, want active reviewer status", status, ok)
	}
	if len(status.ModelOptions) != 1 || status.ModelOptions[0].Value != "remote-model" {
		t.Fatalf("controller model options = %#v, want configured model option", status.ModelOptions)
	}
	status, err = svc.Controllers().SetModel(ctx, session.Ref{SessionID: "sess-controller"}, cfg.ID, "high")
	if err != nil {
		t.Fatal(err)
	}
	if status.Model != "remote-model" || status.ReasoningEffort != "high" || len(status.EffortOptions) != 2 {
		t.Fatalf("status after SetModel = %#v, want model/reasoning intent", status)
	}
	if engine.state[StateControllerConfigRef] != "controller-1" || engine.state[StateControllerModel] != "remote-model" || engine.state[StateControllerReasoning] != "high" {
		t.Fatalf("session state after SetModel = %#v, want controller config intent", engine.state)
	}
	status, err = svc.Controllers().SetMode(ctx, session.Ref{SessionID: "sess-controller"}, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if status.Mode != "manual" || engine.state[StateControllerMode] != "manual" {
		t.Fatalf("status after SetMode = %#v state=%#v, want manual controller mode", status, engine.state)
	}
}

func TestCompactionRecordsCoreCheckpointEvent(t *testing.T) {
	engine := &recordingEngine{
		snapshot: session.Snapshot{
			Session: session.Session{
				Ref: session.Ref{
					AppName:      "caelis",
					UserID:       "tester",
					SessionID:    "sess-compact",
					WorkspaceKey: "repo",
				},
			},
			Events: []session.Event{
				{
					ID:   "evt-1",
					Type: session.EventUser,
					Message: &model.Message{
						Role:  model.RoleUser,
						Parts: []model.Part{model.NewTextPart("first user request")},
					},
				},
				{
					ID:   "evt-2",
					Type: session.EventAssistant,
					Message: &model.Message{
						Role:  model.RoleAssistant,
						Parts: []model.Part{model.NewTextPart("first assistant answer")},
					},
				},
			},
		},
	}
	svc, err := New(Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "tester",
			WorkspaceKey: "repo",
		},
		Engine: engine,
	})
	if err != nil {
		t.Fatal(err)
	}

	event, err := svc.Compaction().Compact(context.Background(), CompactSessionRequest{
		SessionRef: session.Ref{SessionID: "sess-compact"},
		Trigger:    "manual",
	})
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if event.Type != session.EventCompact || event.Message == nil {
		t.Fatalf("compact event = %#v, want compact model-visible event", event)
	}
	text := event.Message.TextContent()
	if !strings.Contains(text, "CONTEXT CHECKPOINT") || !strings.Contains(text, "first user request") || !strings.Contains(text, "first assistant answer") {
		t.Fatalf("compact text = %q, want checkpoint with source summary", text)
	}
	meta, ok := event.Meta[compactMetaKey].(map[string]any)
	if !ok {
		t.Fatalf("compact meta = %#v, want compact metadata map", event.Meta)
	}
	if meta["trigger"] != "manual" || meta["source_event_count"] != 2 || meta["summarized_through_id"] != "evt-2" {
		t.Fatalf("compact meta = %#v, want manual source summary through evt-2", meta)
	}
	if len(engine.events) != 1 || engine.events[0].Type != session.EventCompact {
		t.Fatalf("recorded events = %#v, want one compact event", engine.events)
	}
}

func TestViewServiceProjectsLoadedSession(t *testing.T) {
	engine := &recordingEngine{snapshot: session.Snapshot{
		Session: session.Session{
			Ref:   session.Ref{AppName: "caelis-app", UserID: "tester", SessionID: "sess-1", WorkspaceKey: "repo"},
			Title: "scratch",
		},
		Events: []session.Event{{
			ID:   "evt-1",
			Type: session.EventAssistant,
			Message: &model.Message{
				Role:  model.RoleAssistant,
				Parts: []model.Part{model.NewTextPart("pong")},
			},
		}},
	}}
	svc, err := New(Config{
		Runtime: config.Runtime{AppName: "caelis-app", UserID: "tester", WorkspaceKey: "repo"},
		Engine:  engine,
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := svc.Views().Session(context.Background(), session.Ref{SessionID: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}
	if engine.loadRef.AppName != "caelis-app" || engine.loadRef.UserID != "tester" || engine.loadRef.WorkspaceKey != "repo" {
		t.Fatalf("load ref = %#v, want runtime defaults", engine.loadRef)
	}
	if view.Ref.SessionID != "sess-1" || len(view.Transcript) != 1 || view.Transcript[0].Text != "pong" {
		t.Fatalf("view = %#v, want assistant transcript", view)
	}
}

func TestStatusServiceViewProjectsSharedAppState(t *testing.T) {
	ctx := context.Background()
	updatedAt := time.Date(2026, 5, 30, 10, 30, 0, 0, time.UTC)
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := manager.UpsertModel(ctx, appsettings.ModelConfig{
		Provider:               "openai-compatible",
		Model:                  "gpt-test",
		BaseURL:                "https://api.example.test/v1",
		DefaultReasoningEffort: "low",
		ReasoningMode:          "fixed",
		ReasoningLevels:        []string{"low", "high"},
		ContextWindowTokens:    128000,
		MaxOutputTokens:        4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := &recordingEngine{snapshot: session.Snapshot{
		Session: session.Session{
			Ref:       session.Ref{AppName: "caelis-app", UserID: "tester", SessionID: "sess-1", WorkspaceKey: "repo"},
			Workspace: session.Workspace{Key: "repo", CWD: "/tmp/repo"},
			Title:     "scratch",
			UpdatedAt: updatedAt,
			Participants: []session.ParticipantBinding{{
				ID:        "agent-1",
				Kind:      session.ParticipantACP,
				Role:      session.ParticipantSidecar,
				AgentName: "reviewer",
				Label:     "Reviewer",
			}},
		},
		State: session.State{
			StateCurrentModelID:         cfg.ID,
			StateCurrentReasoningEffort: "high",
			StateSessionMode:            coreruntime.SessionModeManual,
		},
		Events: []session.Event{{
			ID:   "evt-1",
			Type: session.EventAssistant,
			Message: &model.Message{
				Role:  model.RoleAssistant,
				Parts: []model.Part{model.NewTextPart("pong")},
			},
		}, {
			ID:   "evt-2",
			Type: session.EventApproval,
			Approval: &session.ApprovalEvent{
				ID:     "approval-1",
				Status: session.ApprovalPending,
			},
		}},
	}}
	svc, err := New(Config{
		Runtime: config.Runtime{
			AppName:      "caelis-app",
			UserID:       "tester",
			WorkspaceKey: "repo",
			WorkspaceCWD: "/tmp/repo",
			Model:        "fallback-model",
			Store:        config.Store{Backend: "sqlite", URI: "/tmp/caelis-services.sqlite"},
			Sandbox: config.Sandbox{
				Backend:       "host",
				Network:       "disabled",
				ReadableRoots: []string{"/tmp/repo"},
				WritableRoots: []string{"/tmp/repo"},
			},
		},
		Engine:   engine,
		Settings: manager,
		Agents: []AgentDescriptor{{
			ID:          "reviewer",
			Name:        "Reviewer",
			Kind:        AgentKindExternalACP,
			Command:     "reviewer-acp",
			Args:        []string{"--stdio"},
			Description: "reviews changes",
			Meta:        map[string]string{"scope": "workspace"},
		}},
		Resources: appresources.Catalog{
			Tools: []plugin.FactoryAlias{{
				Name: "run_command",
				Uses: "builtin.run_command",
			}},
			Prompts: []plugin.PromptFragment{{
				ID:   "agents.workspace",
				Text: "workspace rule",
			}},
			AgentFiles: []appresources.AgentFile{{
				ID:   "agents.workspace",
				Path: "AGENTS.md",
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	status, err := svc.Status().View(ctx, StatusRequest{SessionRef: session.Ref{SessionID: "sess-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if status.Runtime.AppName != "caelis-app" || status.Runtime.StoreBackend != "sqlite" || status.Runtime.SandboxBackend != "host" {
		t.Fatalf("runtime status = %#v, want app/store/sandbox defaults", status.Runtime)
	}
	if status.Runtime.StoreURI != "/tmp/caelis-services.sqlite" {
		t.Fatalf("runtime store uri = %q, want configured store URI", status.Runtime.StoreURI)
	}
	if status.Runtime.SandboxReadableRootCount != 1 || status.Runtime.SandboxWritableRootCount != 1 {
		t.Fatalf("runtime sandbox root counts = %#v, want 1/1", status.Runtime)
	}
	if status.Session == nil || status.Session.Ref.SessionID != "sess-1" || status.Session.Title != "scratch" {
		t.Fatalf("session status = %#v, want projected session", status.Session)
	}
	if status.Session.Status != "waiting_approval" || status.Session.TranscriptCount != 2 || status.Session.PendingApprovalCount != 1 || status.Session.ParticipantCount != 1 {
		t.Fatalf("session counters = %#v, want transcript/approval/participant projection", status.Session)
	}
	if !status.Model.Configured || status.Model.Count != 1 || status.Model.Current == nil || status.Model.Current.ID != cfg.ID {
		t.Fatalf("model status = %#v, want selected current model", status.Model)
	}
	if status.Model.ReasoningEffort != "high" || len(status.Model.Choices) != 1 || !status.Model.Choices[0].Default {
		t.Fatalf("model choices = %#v reasoning=%q, want default choice and session reasoning", status.Model.Choices, status.Model.ReasoningEffort)
	}
	if status.Mode.Current.ID != coreruntime.SessionModeManual || len(status.Mode.Choices) != 2 {
		t.Fatalf("mode status = %#v, want manual with choices", status.Mode)
	}
	if status.Agents.Count != 1 || status.Agents.ExternalACPCount != 1 || status.Agents.Items[0].Args[0] != "--stdio" {
		t.Fatalf("agent status = %#v, want one external ACP agent", status.Agents)
	}
	if status.Resources.Tools != 1 || status.Resources.Prompts != 1 || status.Resources.AgentFiles != 1 {
		t.Fatalf("resource status = %#v, want tool/prompt/agent file counts", status.Resources)
	}

	status.Agents.Items[0].Args[0] = "changed"
	status.Agents.Items[0].Meta["scope"] = "changed"
	again, err := svc.Status().View(ctx, StatusRequest{SessionRef: session.Ref{SessionID: "sess-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if again.Agents.Items[0].Args[0] != "--stdio" || again.Agents.Items[0].Meta["scope"] != "workspace" {
		t.Fatalf("agent status was not cloned: %#v", again.Agents.Items[0])
	}
}

func TestSandboxServiceHostLifecycleIsNoop(t *testing.T) {
	svc, err := New(Config{
		Runtime: config.Runtime{
			Sandbox: config.Sandbox{Backend: "host"},
		},
		Engine: &recordingEngine{},
		Sandbox: fakeSandboxRuntime{
			descriptor: sandbox.Descriptor{
				Backend: sandbox.BackendHost,
				DefaultConstraints: sandbox.Constraints{
					Route:   sandbox.RouteHost,
					Backend: sandbox.BackendHost,
				},
			},
			status: sandbox.Status{
				RequestedBackend: sandbox.BackendHost,
				ResolvedBackend:  sandbox.BackendHost,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func(context.Context) (SandboxStatus, error){
		"status":  svc.Sandbox().Status,
		"prepare": svc.Sandbox().Prepare,
		"repair":  svc.Sandbox().Repair,
		"reset":   svc.Sandbox().Reset,
	} {
		status, err := call(context.Background())
		if err != nil {
			t.Fatalf("%s error = %v", name, err)
		}
		if status.RequestedBackend != "host" || status.ResolvedBackend != "host" || status.Route != "host" {
			t.Fatalf("%s status = %#v, want host route", name, status)
		}
	}
}

func TestSessionServiceListAppliesWorkspaceDefaults(t *testing.T) {
	engine := &recordingEngine{page: session.SessionPage{
		Sessions: []session.SessionSummary{{
			Session: session.Session{
				Ref:       session.Ref{AppName: "caelis-app", UserID: "tester", SessionID: "sess-1", WorkspaceKey: "repo"},
				Workspace: session.Workspace{Key: "repo", CWD: "/tmp/repo"},
				Title:     "scratch",
			},
			EventCount: 3,
		}},
	}}
	svc, err := New(Config{
		Runtime: config.Runtime{AppName: "caelis-app", UserID: "tester", WorkspaceKey: "repo", WorkspaceCWD: "/tmp/repo"},
		Engine:  engine,
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := svc.Sessions().List(context.Background(), ListSessionsRequest{Search: " scratch ", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if engine.list.Ref.AppName != "caelis-app" || engine.list.Ref.UserID != "tester" || engine.list.Ref.WorkspaceKey != "repo" {
		t.Fatalf("list ref = %#v, want runtime defaults", engine.list.Ref)
	}
	if engine.list.WorkspaceCWD != "/tmp/repo" || engine.list.Search != "scratch" || engine.list.Limit != 20 {
		t.Fatalf("list query = %#v, want workspace/search/limit", engine.list)
	}
	if len(page.Sessions) != 1 || page.Sessions[0].Session.SessionID != "sess-1" || page.Sessions[0].EventCount != 3 {
		t.Fatalf("page = %#v, want returned session summary", page)
	}
}

func TestModelServicePersistsCatalogAndSessionModelSelection(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	engine := &recordingEngine{snapshot: session.Snapshot{
		Session: session.Session{Ref: session.Ref{AppName: "caelis-app", UserID: "tester", SessionID: "sess-1", WorkspaceKey: "repo"}},
		State:   session.State{},
	}}
	svc, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis-app", UserID: "tester", WorkspaceKey: "repo"},
		Engine:   engine,
		Settings: manager,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := svc.Models().Connect(ctx, appsettings.ModelConfig{
		Provider:        "openai-compatible",
		Model:           "gpt-test",
		BaseURL:         "https://api.example.test/v1",
		ReasoningMode:   "fixed",
		ReasoningLevels: []string{"low", "high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	choices, err := svc.Models().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(choices) != 1 || choices[0].ID != cfg.ID || !choices[0].Default {
		t.Fatalf("model choices = %#v, want connected default", choices)
	}
	if _, err := svc.Models().Use(ctx, session.Ref{SessionID: "sess-1"}, cfg.Alias, "high"); err != nil {
		t.Fatal(err)
	}
	if engine.state[StateCurrentModelID] != cfg.ID || engine.state[StateCurrentReasoningEffort] != "high" {
		t.Fatalf("session state = %#v, want selected model and reasoning", engine.state)
	}
	if _, err := svc.Models().Use(ctx, session.Ref{SessionID: "sess-1"}, cfg.Alias, "max"); err == nil {
		t.Fatal("Use unsupported reasoning error = nil, want error")
	}
	current, ok, err := svc.Models().Current(ctx, session.Ref{SessionID: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || current.ID != cfg.ID {
		t.Fatalf("current model = %#v, %v, want selected model", current, ok)
	}
	profile, ok, err := svc.Models().RuntimeProfile(ctx, session.Ref{SessionID: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || profile.Model != "gpt-test" || profile.BaseURL != "https://api.example.test/v1" || profile.ReasoningEffort != "high" {
		t.Fatalf("runtime profile = %#v, %v, want selected model profile with reasoning override", profile, ok)
	}
	if _, err := svc.Turns().Begin(ctx, BeginTurnRequest{SessionRef: session.Ref{SessionID: "sess-1"}, Input: "ping"}); err != nil {
		t.Fatal(err)
	}
	if engine.turn.Model != cfg.ID || engine.turn.Reasoning.Effort != "high" {
		t.Fatalf("turn request = %#v, want selected model and reasoning override", engine.turn)
	}
	if err := svc.Models().ClearSession(ctx, session.Ref{SessionID: "sess-1"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := engine.state[StateCurrentModelID]; ok {
		t.Fatalf("session state after clear = %#v, want no model override", engine.state)
	}
}

func TestModelServiceCatalogSupportsConnectDefaults(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis-app", UserID: "tester", WorkspaceKey: "repo"},
		Engine:   &recordingEngine{},
		Settings: manager,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Models().Connect(ctx, appsettings.ModelConfig{
		Provider: "minimax",
		Model:    "MiniMax-M2.7-highspeed",
		BaseURL:  "https://api.minimaxi.com/anthropic",
	}); err != nil {
		t.Fatal(err)
	}
	configured, err := svc.Models().ConfiguredProviderModels(ctx, "minimax")
	if err != nil {
		t.Fatal(err)
	}
	if len(configured) != 1 || configured[0] != "MiniMax-M2.7-highspeed" {
		t.Fatalf("configured models = %#v, want saved minimax model", configured)
	}
	catalog := svc.Models().ListCatalogModels("deepseek")
	if len(catalog) == 0 || catalog[0] != "deepseek-v4-flash" {
		t.Fatalf("deepseek catalog = %#v, want sorted built-in models", catalog)
	}
	caps, ok := svc.Models().LookupCapabilities("deepseek", "deepseek-v4-pro")
	if !ok || caps.ContextWindowTokens != 1048576 || caps.DefaultMaxOutputTokens != 32768 || !caps.SupportsReasoning {
		t.Fatalf("deepseek caps = %#v, %v, want app-service catalog capabilities", caps, ok)
	}
	levels := svc.Models().ReasoningLevels("deepseek", "deepseek-v4-pro")
	if len(levels) != 3 || levels[0] != "none" || levels[1] != "high" || levels[2] != "max" {
		t.Fatalf("deepseek reasoning levels = %#v, want none/high/max", levels)
	}
	if levels := svc.Models().ReasoningLevels("codefree", "GLM-4.7"); len(levels) != 0 {
		t.Fatalf("codefree reasoning levels = %#v, want none", levels)
	}
}

func TestModelServiceCodeFreeAuthDelegatesToConfiguredAuthenticator(t *testing.T) {
	ctx := context.Background()
	auth := &recordingCodeFreeAuth{
		ensureResult: CodeFreeAuthResult{CredentialPath: "/tmp/codefree.json", UserID: "user-1", HasRefreshToken: true},
		modelResult:  CodeFreeAuthResult{CredentialPath: "/tmp/codefree.json", UserID: "user-1", LoginStarted: true},
		refreshResult: CodeFreeAuthResult{
			CredentialPath:  "/tmp/codefree.json",
			UserID:          "user-2",
			HasRefreshToken: true,
		},
	}
	svc, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis-app", UserID: "tester", WorkspaceKey: "repo"},
		Engine:   &recordingEngine{},
		CodeFree: auth,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := CodeFreeAuthRequest{BaseURL: "https://www.srdcloud.cn", OpenBrowser: true, CallbackTimeout: time.Second}
	result, err := svc.Models().EnsureCodeFreeAuth(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if result.UserID != "user-1" || !auth.ensureReq.OpenBrowser {
		t.Fatalf("ensure result=%#v req=%#v, want delegated auth request", result, auth.ensureReq)
	}
	result, err = svc.Models().EnsureCodeFreeModelSelectionAuth(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.LoginStarted || auth.modelReq.BaseURL != req.BaseURL {
		t.Fatalf("model selection result=%#v req=%#v, want delegated model auth", result, auth.modelReq)
	}
	result, err = svc.Models().RefreshCodeFreeAuth(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if result.UserID != "user-2" || auth.refreshReq.CallbackTimeout != time.Second {
		t.Fatalf("refresh result=%#v req=%#v, want delegated refresh", result, auth.refreshReq)
	}
}

func TestModeServicePersistsSessionModeAndTurnsUseIt(t *testing.T) {
	engine := &recordingEngine{snapshot: session.Snapshot{
		Session: session.Session{Ref: session.Ref{AppName: "caelis-app", UserID: "tester", SessionID: "sess-1", WorkspaceKey: "repo"}},
		State:   session.State{},
	}}
	svc, err := New(Config{
		Runtime: config.Runtime{AppName: "caelis-app", UserID: "tester", WorkspaceKey: "repo"},
		Engine:  engine,
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := svc.Modes().Current(context.Background(), session.Ref{SessionID: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}
	if current.ID != coreruntime.SessionModeAutoReview {
		t.Fatalf("default mode = %#v, want auto-review", current)
	}
	manual, err := svc.Modes().Set(context.Background(), session.Ref{SessionID: "sess-1"}, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if manual.ID != coreruntime.SessionModeManual || engine.state[StateSessionMode] != coreruntime.SessionModeManual {
		t.Fatalf("manual mode = %#v state=%#v, want persisted manual", manual, engine.state)
	}
	if _, err := svc.Turns().Begin(context.Background(), BeginTurnRequest{SessionRef: session.Ref{SessionID: "sess-1"}, Input: "ping"}); err != nil {
		t.Fatal(err)
	}
	if engine.turn.Mode != coreruntime.SessionModeManual {
		t.Fatalf("turn mode = %q, want manual", engine.turn.Mode)
	}
	if _, err := svc.Modes().Set(context.Background(), session.Ref{SessionID: "sess-1"}, "unknown"); err == nil {
		t.Fatal("Set(unknown) error = nil, want validation error")
	}
}

type recordingEngine struct {
	start    session.StartRequest
	list     session.ListQuery
	page     session.SessionPage
	turn     coreruntime.TurnRequest
	events   []session.Event
	loadRef  session.Ref
	state    session.State
	snapshot session.Snapshot
}

type fakeSandboxRuntime struct {
	descriptor sandbox.Descriptor
	status     sandbox.Status
}

func (r fakeSandboxRuntime) Descriptor() sandbox.Descriptor {
	return sandbox.CloneDescriptor(r.descriptor)
}

func (r fakeSandboxRuntime) Status() sandbox.Status {
	return sandbox.CloneStatus(r.status)
}

func (fakeSandboxRuntime) FileSystem() sandbox.FileSystem {
	return nil
}

func (fakeSandboxRuntime) Run(context.Context, sandbox.CommandRequest) (sandbox.CommandResult, error) {
	return sandbox.CommandResult{}, errors.New("not implemented")
}

func (fakeSandboxRuntime) Start(context.Context, sandbox.CommandRequest) (sandbox.Session, error) {
	return nil, errors.New("not implemented")
}

func (fakeSandboxRuntime) Open(context.Context, sandbox.SessionRef) (sandbox.Session, error) {
	return nil, errors.New("not implemented")
}

func (fakeSandboxRuntime) Close() error {
	return nil
}

func (e *recordingEngine) StartSession(_ context.Context, req session.StartRequest) (session.Session, error) {
	e.start = req
	return session.Session{
		Ref: session.Ref{
			AppName:      req.AppName,
			UserID:       req.UserID,
			SessionID:    "sess-1",
			WorkspaceKey: req.Workspace.Key,
		},
		Workspace: req.Workspace,
		Title:     req.Title,
	}, nil
}

func (e *recordingEngine) ListSessions(_ context.Context, query session.ListQuery) (session.SessionPage, error) {
	e.list = query
	return session.CloneSessionPage(e.page), nil
}

func (e *recordingEngine) LoadSession(_ context.Context, ref session.Ref) (session.Snapshot, error) {
	e.loadRef = ref
	return e.snapshot, nil
}

func (e *recordingEngine) RecordEvents(_ context.Context, _ session.Ref, events []session.Event) (session.Cursor, error) {
	e.events = cloneTestEvents(events)
	return "1", nil
}

func (e *recordingEngine) UpdateSessionState(_ context.Context, _ session.Ref, patch session.StatePatch) error {
	if patch == nil {
		return nil
	}
	next, err := patch(cloneState(e.state))
	if err != nil {
		return err
	}
	e.state = cloneState(next)
	e.snapshot.State = cloneState(next)
	return nil
}

func (e *recordingEngine) BeginTurn(_ context.Context, req coreruntime.TurnRequest) (coreruntime.Turn, error) {
	e.turn = req
	events := make(chan coreruntime.EventEnvelope)
	close(events)
	return staticTurn{events: events}, nil
}

func cloneTestEvents(in []session.Event) []session.Event {
	out := make([]session.Event, 0, len(in))
	for _, event := range in {
		out = append(out, session.CloneEvent(event))
	}
	return out
}

type recordingCodeFreeAuth struct {
	ensureReq     CodeFreeAuthRequest
	modelReq      CodeFreeAuthRequest
	refreshReq    CodeFreeAuthRequest
	ensureResult  CodeFreeAuthResult
	modelResult   CodeFreeAuthResult
	refreshResult CodeFreeAuthResult
}

func (a *recordingCodeFreeAuth) EnsureAuth(_ context.Context, req CodeFreeAuthRequest) (CodeFreeAuthResult, error) {
	a.ensureReq = req
	return a.ensureResult, nil
}

func (a *recordingCodeFreeAuth) EnsureModelSelectionAuth(_ context.Context, req CodeFreeAuthRequest) (CodeFreeAuthResult, error) {
	a.modelReq = req
	return a.modelResult, nil
}

func (a *recordingCodeFreeAuth) Refresh(_ context.Context, req CodeFreeAuthRequest) (CodeFreeAuthResult, error) {
	a.refreshReq = req
	return a.refreshResult, nil
}

type recordingAgentInstaller struct {
	called bool
	agent  AgentDescriptor
}

func (i *recordingAgentInstaller) InstallBuiltinACPAgent(_ context.Context, agent AgentDescriptor) (AgentDescriptor, error) {
	i.called = true
	i.agent = agent
	agent.Command = "/installed/" + agent.Name + "-acp"
	agent.Args = nil
	return agent, nil
}

func (i *recordingAgentInstaller) InstallableBuiltinACPAgentOptions(_ context.Context, builtins []AgentDescriptor) ([]AgentInstallOption, error) {
	out := make([]AgentInstallOption, 0, len(builtins))
	for _, agent := range builtins {
		out = append(out, AgentInstallOption{
			Value:   agent.Name,
			Display: agent.Name + " (npm install)",
			Detail:  "npm install " + agent.Name,
		})
	}
	return out, nil
}

func (e *recordingEngine) Interrupt(context.Context, session.Ref) error {
	return nil
}

func (e *recordingEngine) Replay(context.Context, coreruntime.ReplayRequest) (<-chan coreruntime.EventEnvelope, error) {
	events := make(chan coreruntime.EventEnvelope)
	close(events)
	return events, nil
}

type staticTurn struct {
	events <-chan coreruntime.EventEnvelope
}

func (t staticTurn) ID() string {
	return "turn"
}

func (t staticTurn) RunID() string {
	return "run"
}

func (t staticTurn) SessionRef() session.Ref {
	return session.Ref{SessionID: "session"}
}

func (t staticTurn) StartedAt() time.Time {
	return time.Time{}
}

func (t staticTurn) Events() <-chan coreruntime.EventEnvelope {
	return t.events
}

func (t staticTurn) Submit(context.Context, coreruntime.Submission) error {
	return nil
}

func (t staticTurn) Cancel() coreruntime.CancelResult {
	return coreruntime.CancelResult{Status: coreruntime.CancelCancelled}
}

func (t staticTurn) Close() error {
	return nil
}
