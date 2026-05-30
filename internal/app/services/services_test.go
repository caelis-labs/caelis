package services

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
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

func TestSettingsServiceViewAndRuntimeMutation(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, appsettings.NewFileStore(t.TempDir()), appsettings.Document{
		Compaction: appsettings.CompactionPolicy{
			Prompt:         " keep durable facts ",
			MaxSourceChars: 512,
			Auto: appsettings.AutoCompactionPolicy{
				Mode:           "on",
				WatermarkRatio: 0.8,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester"},
		Engine:   &recordingEngine{},
		Settings: manager,
	})
	if err != nil {
		t.Fatal(err)
	}

	view, err := svc.Settings().SetRuntime(ctx, config.Runtime{
		AppName:      " caelis-app ",
		UserID:       " user-1 ",
		WorkspaceKey: " repo ",
		WorkspaceCWD: " /repo ",
		Model:        " alpha ",
		Store: config.Store{
			Backend: " SQLITE ",
			URI:     " /tmp/sessions.db ",
		},
		Sandbox: config.Sandbox{
			Backend:       " HOST ",
			Network:       " OFF ",
			HelperPath:    " /helper ",
			ReadableRoots: []string{" /read "},
			WritableRoots: []string{" /write "},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Runtime.AppName != "caelis-app" || view.Runtime.UserID != "user-1" || view.Runtime.Model != "alpha" {
		t.Fatalf("runtime view = %#v, want normalized runtime fields", view.Runtime)
	}
	if view.Store.Backend != "sqlite" || view.Store.URI != "/tmp/sessions.db" {
		t.Fatalf("store view = %#v, want normalized store settings", view.Store)
	}
	if view.Sandbox.Backend != "host" || view.Sandbox.Network != "off" || view.Sandbox.HelperPath != "/helper" {
		t.Fatalf("sandbox view = %#v, want normalized sandbox settings", view.Sandbox)
	}
	if view.Sandbox.ReadableRoots[0] != "/read" || view.Sandbox.WritableRoots[0] != "/write" {
		t.Fatalf("sandbox roots = %#v/%#v, want trimmed roots", view.Sandbox.ReadableRoots, view.Sandbox.WritableRoots)
	}
	if view.Compaction.Prompt != "keep durable facts" || view.Compaction.AutoMode != "enabled" || view.Compaction.AutoWatermarkRatio != 0.8 {
		t.Fatalf("compaction view = %#v, want normalized compaction settings", view.Compaction)
	}

	view.Sandbox.ReadableRoots[0] = "mutated"
	again, err := svc.Settings().View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again.Sandbox.ReadableRoots[0] != "/read" {
		t.Fatalf("settings view was not cloned: %#v", again.Sandbox.ReadableRoots)
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

func TestAgentServiceRemoveDisablesStaticAgent(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester"},
		Engine:   &recordingEngine{},
		Settings: manager,
		Agents: []AgentDescriptor{{
			ID:          "plugin-helper",
			Name:        "helper",
			Kind:        AgentKindExternalACP,
			Description: "plugin helper",
			Command:     "helper-acp",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if agents, err := svc.Agents().List(ctx); err != nil || len(agents) != 1 || agents[0].ID != "plugin-helper" {
		t.Fatalf("agents before remove = %#v err=%v, want plugin-helper", agents, err)
	}
	if err := svc.Agents().Remove(ctx, "helper"); err != nil {
		t.Fatal(err)
	}
	if agents, err := svc.Agents().List(ctx); err != nil || len(agents) != 0 {
		t.Fatalf("agents after static remove = %#v err=%v, want none", agents, err)
	}
	if disabled := manager.ListDisabledACPAgents(); len(disabled) != 1 || disabled[0] != "helper" {
		t.Fatalf("disabled agents = %#v, want helper", disabled)
	}
	registered, err := svc.Agents().RegisterCustom(ctx, AgentDescriptor{Name: "helper", Command: "helper-next"})
	if err != nil {
		t.Fatal(err)
	}
	if registered.Command != "helper-next" {
		t.Fatalf("registered = %#v, want helper-next", registered)
	}
	if disabled := manager.ListDisabledACPAgents(); len(disabled) != 0 {
		t.Fatalf("disabled agents after register = %#v, want none", disabled)
	}
	agents, err := svc.Agents().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].Command != "helper-next" {
		t.Fatalf("agents after re-register = %#v, want settings-backed helper", agents)
	}
}

func TestAgentServiceRemoveStaticAgentByIDDisablesNameAlias(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{
		Agents: []plugin.ACPAgentDescriptor{{
			Name:    "helper",
			Command: "helper-override",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester"},
		Engine:   &recordingEngine{},
		Settings: manager,
		Agents: []AgentDescriptor{{
			ID:      "plugin-helper",
			Name:    "helper",
			Kind:    AgentKindExternalACP,
			Command: "helper-acp",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Agents().Remove(ctx, "plugin-helper"); err != nil {
		t.Fatal(err)
	}
	if agents, err := svc.Agents().List(ctx); err != nil || len(agents) != 0 {
		t.Fatalf("agents after remove by id = %#v err=%v, want none", agents, err)
	}
	if disabled := manager.ListDisabledACPAgents(); !slices.Contains(disabled, "helper") || !slices.Contains(disabled, "plugin-helper") {
		t.Fatalf("disabled agents = %#v, want helper and plugin-helper", disabled)
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

func TestCompactionUsesConfiguredModelProvider(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := manager.UpsertModel(ctx, appsettings.ModelConfig{
		Provider: "openai-compatible",
		Model:    "gpt-compact",
		Alias:    "compact",
		BaseURL:  "https://api.example.test/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := &recordingEngine{
		snapshot: session.Snapshot{
			Session: session.Session{
				Ref: session.Ref{
					AppName:      "caelis",
					UserID:       "tester",
					SessionID:    "sess-compact-model",
					WorkspaceKey: "repo",
				},
			},
			State: session.State{},
			Events: []session.Event{
				{
					ID:   "evt-1",
					Type: session.EventUser,
					Message: &model.Message{
						Role:  model.RoleUser,
						Parts: []model.Part{model.NewTextPart("Project objective: migrate model-backed compaction")},
					},
				},
				{
					ID:   "evt-2",
					Type: session.EventAssistant,
					Message: &model.Message{
						Role:  model.RoleAssistant,
						Parts: []model.Part{model.NewTextPart("Implemented the service path")},
					},
				},
			},
		},
	}
	provider := &compactSummaryProvider{
		response: "CONTEXT CHECKPOINT\n\nObjective: model compacted summary\nNext action: continue migration",
		usage:    &model.Usage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120},
	}
	var captured appsettings.ModelConfig
	svc, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester", WorkspaceKey: "repo"},
		Engine:   engine,
		Settings: manager,
		ModelProvider: func(_ context.Context, cfg appsettings.ModelConfig) (model.Provider, error) {
			captured = cfg
			return provider, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	event, err := svc.Compaction().Compact(ctx, CompactSessionRequest{
		SessionRef: session.Ref{SessionID: "sess-compact-model"},
		Trigger:    "manual",
	})
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if captured.ID != cfg.ID || captured.Model != "gpt-compact" {
		t.Fatalf("captured config = %#v, want current compact model", captured)
	}
	if provider.request.Model != "gpt-compact" || len(provider.request.Messages) != 1 {
		t.Fatalf("provider request = %#v, want compact model request", provider.request)
	}
	if prompt := provider.request.Messages[0].TextContent(); !strings.Contains(prompt, "Project objective: migrate model-backed compaction") || !strings.Contains(prompt, "Return only the checkpoint text") {
		t.Fatalf("compact prompt = %q, want source facts and output contract", prompt)
	}
	if got := event.Message.TextContent(); got != provider.response {
		t.Fatalf("compact text = %q, want model checkpoint", got)
	}
	if event.Message.Usage == nil || event.Message.Usage.TotalTokens != 120 {
		t.Fatalf("compact message usage = %#v, want provider usage", event.Message.Usage)
	}
	if usageCategory, _ := event.Meta["usage_category"].(string); usageCategory != "compact" {
		t.Fatalf("compact usage category = %#v, want compact", event.Meta["usage_category"])
	}
	topUsage, ok := event.Meta["usage"].(map[string]any)
	if !ok || topUsage["total_tokens"] != 120 {
		t.Fatalf("compact top-level usage = %#v, want provider usage", event.Meta["usage"])
	}
	meta, ok := event.Meta[compactMetaKey].(map[string]any)
	if !ok {
		t.Fatalf("compact meta = %#v, want compact metadata map", event.Meta)
	}
	if meta["generator"] != "app-services/model" || meta["model"] != "gpt-compact" || meta["model_provider"] != "openai-compatible" {
		t.Fatalf("compact meta = %#v, want model generator metadata", meta)
	}
	usage, ok := meta["usage"].(map[string]any)
	if !ok || usage["total_tokens"] != 120 {
		t.Fatalf("compact usage = %#v, want provider usage metadata", meta["usage"])
	}
	if len(engine.events) != 1 || engine.events[0].Message.TextContent() != provider.response {
		t.Fatalf("recorded compact events = %#v, want model checkpoint", engine.events)
	}
}

func TestCompactionUsesConfiguredPromptPolicy(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpsertModel(ctx, appsettings.ModelConfig{
		Provider: "openai-compatible",
		Model:    "gpt-policy",
		BaseURL:  "https://api.example.test/v1",
	}); err != nil {
		t.Fatal(err)
	}
	engine := &recordingEngine{snapshot: session.Snapshot{
		Session: session.Session{Ref: session.Ref{AppName: "caelis", UserID: "tester", SessionID: "sess-policy", WorkspaceKey: "repo"}},
		Events: []session.Event{{
			ID:   "evt-policy",
			Type: session.EventUser,
			Message: &model.Message{
				Role:  model.RoleUser,
				Parts: []model.Part{model.NewTextPart("policy source fact " + strings.Repeat("x", 120) + " END_MARKER")},
			},
		}},
	}}
	provider := &compactSummaryProvider{response: "CONTEXT CHECKPOINT\n\nPolicy summary"}
	svc, err := New(Config{
		Runtime:       config.Runtime{AppName: "caelis", UserID: "tester", WorkspaceKey: "repo"},
		Engine:        engine,
		Settings:      manager,
		ModelProvider: func(context.Context, appsettings.ModelConfig) (model.Provider, error) { return provider, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := svc.Compaction().SetPolicy(ctx, CompactPromptPolicy{
		Prompt:         "MIGRATION_POLICY_MARKER: write only durable facts.",
		MaxSourceChars: 80,
	})
	if err != nil {
		t.Fatal(err)
	}
	if policy.Source != "settings" || policy.MaxSourceChars != 80 {
		t.Fatalf("policy = %#v, want settings policy", policy)
	}

	event, err := svc.Compaction().Compact(ctx, CompactSessionRequest{SessionRef: session.Ref{SessionID: "sess-policy"}})
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	prompt := provider.request.Messages[0].TextContent()
	if !strings.Contains(prompt, "MIGRATION_POLICY_MARKER") || strings.Contains(prompt, "Preserve durable objective") {
		t.Fatalf("compact prompt = %q, want configured prompt policy without default instructions", prompt)
	}
	if strings.Contains(prompt, "END_MARKER") {
		t.Fatalf("compact prompt = %q, want max_source_chars to bound source text", prompt)
	}
	meta, ok := event.Meta[compactMetaKey].(map[string]any)
	if !ok {
		t.Fatalf("compact meta = %#v, want compact metadata map", event.Meta)
	}
	if meta["prompt_policy"] != "settings" || meta["max_source_chars"] != 80 {
		t.Fatalf("compact meta = %#v, want prompt policy metadata", meta)
	}
}

func TestTurnServiceAutoCompactsBeforeWatermarkTurn(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpsertModel(ctx, appsettings.ModelConfig{
		Provider:            "openai-compatible",
		Model:               "gpt-compact-auto",
		ContextWindowTokens: 120,
		MaxOutputTokens:     10,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetCompactionPolicy(ctx, appsettings.CompactionPolicy{
		Auto: appsettings.AutoCompactionPolicy{
			Mode:           "enabled",
			WatermarkRatio: 0.2,
		},
	}); err != nil {
		t.Fatal(err)
	}
	engine := &recordingEngine{snapshot: session.Snapshot{
		Session: session.Session{Ref: session.Ref{AppName: "caelis", UserID: "tester", SessionID: "sess-auto-compact", WorkspaceKey: "repo"}},
		Events: []session.Event{{
			ID:   "evt-history",
			Type: session.EventUser,
			Message: &model.Message{
				Role:  model.RoleUser,
				Parts: []model.Part{model.NewTextPart(strings.Repeat("long history fact ", 120))},
			},
		}},
	}}
	svc, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester", WorkspaceKey: "repo"},
		Engine:   engine,
		Settings: manager,
	})
	if err != nil {
		t.Fatal(err)
	}

	turn, err := svc.Turns().Begin(ctx, BeginTurnRequest{
		SessionRef: session.Ref{SessionID: "sess-auto-compact"},
		Input:      "continue after checkpoint",
	})
	if err != nil {
		t.Fatal(err)
	}
	live := collectRuntimeTurnEvents(t, turn)
	if len(engine.events) != 1 || engine.events[0].Type != session.EventCompact {
		t.Fatalf("recorded events = %#v, want automatic compact event", engine.events)
	}
	meta, ok := engine.events[0].Meta[compactMetaKey].(map[string]any)
	if !ok || meta["trigger"] != "context_watermark" {
		t.Fatalf("compact meta = %#v, want context_watermark trigger", engine.events[0].Meta)
	}
	if len(live) != 1 || live[0].Type != session.EventCompact {
		t.Fatalf("live events = %#v, want prefixed compact event", live)
	}
	if engine.turn.Input != "continue after checkpoint" {
		t.Fatalf("turn input = %q, want original user turn after compaction", engine.turn.Input)
	}
}

func TestTurnServiceAutoCompactionSkipsFreshCheckpoint(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpsertModel(ctx, appsettings.ModelConfig{
		Provider:            "openai-compatible",
		Model:               "gpt-compact-auto",
		ContextWindowTokens: 120,
		MaxOutputTokens:     10,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetCompactionPolicy(ctx, appsettings.CompactionPolicy{
		Auto: appsettings.AutoCompactionPolicy{
			Mode:           "enabled",
			WatermarkRatio: 0.01,
		},
	}); err != nil {
		t.Fatal(err)
	}
	compactMessage := model.Message{
		Role:  model.RoleUser,
		Parts: []model.Part{model.NewTextPart(strings.Repeat("CONTEXT CHECKPOINT\nsummary ", 120))},
		Meta:  map[string]any{"caelis_compact_checkpoint": true},
	}
	engine := &recordingEngine{snapshot: session.Snapshot{
		Session: session.Session{Ref: session.Ref{AppName: "caelis", UserID: "tester", SessionID: "sess-fresh-compact", WorkspaceKey: "repo"}},
		Events: []session.Event{{
			ID:      "evt-compact",
			Type:    session.EventCompact,
			Message: &compactMessage,
			Meta:    map[string]any{compactMetaKey: map[string]any{"contract_version": 1}},
		}},
	}}
	svc, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester", WorkspaceKey: "repo"},
		Engine:   engine,
		Settings: manager,
	})
	if err != nil {
		t.Fatal(err)
	}

	turn, err := svc.Turns().Begin(ctx, BeginTurnRequest{
		SessionRef: session.Ref{SessionID: "sess-fresh-compact"},
		Input:      "continue",
	})
	if err != nil {
		t.Fatal(err)
	}
	if events := collectRuntimeTurnEvents(t, turn); len(events) != 0 {
		t.Fatalf("live events = %#v, want no redundant compaction prefix", events)
	}
	if len(engine.events) != 0 {
		t.Fatalf("recorded events = %#v, want no redundant compact event", engine.events)
	}
}

func TestCompactionFallsBackWhenModelProviderReturnsNoCheckpoint(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpsertModel(ctx, appsettings.ModelConfig{
		Provider: "openai-compatible",
		Model:    "gpt-empty",
	}); err != nil {
		t.Fatal(err)
	}
	engine := &recordingEngine{snapshot: session.Snapshot{
		Session: session.Session{Ref: session.Ref{AppName: "caelis", UserID: "tester", SessionID: "sess-empty", WorkspaceKey: "repo"}},
		Events: []session.Event{{
			ID:   "evt-1",
			Type: session.EventUser,
			Message: &model.Message{
				Role:  model.RoleUser,
				Parts: []model.Part{model.NewTextPart("fallback source fact")},
			},
		}},
	}}
	svc, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester", WorkspaceKey: "repo"},
		Engine:   engine,
		Settings: manager,
		ModelProvider: func(context.Context, appsettings.ModelConfig) (model.Provider, error) {
			return emptyCompactProvider{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	event, err := svc.Compaction().Compact(ctx, CompactSessionRequest{SessionRef: session.Ref{SessionID: "sess-empty"}})
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if text := event.Message.TextContent(); !strings.Contains(text, "fallback source fact") {
		t.Fatalf("compact text = %q, want deterministic fallback summary", text)
	}
	meta, ok := event.Meta[compactMetaKey].(map[string]any)
	if !ok || meta["generator"] != "app-services/manual" {
		t.Fatalf("compact meta = %#v, want manual fallback generator", event.Meta)
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

func TestApprovalServiceProjectsActionsAndSubmitsDecision(t *testing.T) {
	engine := &recordingEngine{snapshot: session.Snapshot{
		Session: session.Session{
			Ref: session.Ref{AppName: "caelis-app", UserID: "tester", SessionID: "sess-approve", WorkspaceKey: "repo"},
		},
		Events: []session.Event{{
			ID:   "evt-approval",
			Type: session.EventApproval,
			Approval: &session.ApprovalEvent{
				ID:     "approval-1",
				Status: session.ApprovalPending,
				Tool: &session.ToolEvent{
					ID:   "tool-1",
					Name: "write_file",
					Input: map[string]any{
						"path": "/repo/file.txt",
					},
				},
				Options: []session.ApprovalOption{
					{ID: "allow_once", Name: "Allow once", Kind: "allow"},
					{ID: "reject_once", Name: "Reject once", Kind: "reject"},
				},
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
	pending, err := svc.Approvals().Pending(context.Background(), session.Ref{SessionID: "sess-approve"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != "approval-1" || pending[0].Tool != "write_file" {
		t.Fatalf("pending approvals = %#v, want one write_file approval", pending)
	}
	if len(pending[0].Actions) != 2 || !pending[0].Actions[0].Approved || !pending[0].Actions[0].Primary || pending[0].Actions[1].Approved {
		t.Fatalf("approval actions = %#v, want allow primary and reject secondary", pending[0].Actions)
	}
	pending[0].Actions[0].Name = "mutated"
	again, err := svc.Approvals().Pending(context.Background(), session.Ref{SessionID: "sess-approve"})
	if err != nil {
		t.Fatal(err)
	}
	if again[0].Actions[0].Name != "Allow once" {
		t.Fatalf("pending approvals were not cloned: %#v", again[0].Actions)
	}

	turn := &recordingRuntimeTurn{ref: session.Ref{SessionID: "sess-approve"}}
	decision, err := svc.Approvals().Submit(context.Background(), turn, ApprovalDecisionRequest{
		Approval: again[0],
		OptionID: "allow_once",
		Reason:   "user approved",
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != "selected" || decision.OptionID != "allow_once" || !decision.Approved {
		t.Fatalf("decision = %#v, want selected allow_once approval", decision)
	}
	if turn.submission.Kind != coreruntime.SubmissionApproval || turn.submission.Approval == nil || !turn.submission.Approval.Approved || turn.submission.Approval.OptionID != "allow_once" {
		t.Fatalf("submitted approval = %#v, want runtime approval submission", turn.submission)
	}
	if _, err := svc.Approvals().Decision(ApprovalDecisionRequest{Outcome: "selected"}); err == nil {
		t.Fatal("Decision(selected without option) error = nil, want validation error")
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
				Usage: &model.Usage{
					InputTokens:         10,
					CachedInputTokens:   2,
					OutputTokens:        3,
					ReasoningTokens:     1,
					TotalTokens:         14,
					ContextWindowTokens: 128000,
				},
			},
		}, {
			ID:   "evt-review-usage",
			Type: session.EventNotice,
			Meta: map[string]any{
				"usage_category": "auto_review",
				"usage": map[string]any{
					"prompt_tokens":     5,
					"completion_tokens": 1,
					"reasoning_tokens":  1,
					"total_tokens":      6,
				},
			},
		}, {
			ID:   "evt-subagent-usage",
			Type: session.EventAssistant,
			Scope: &session.EventScope{
				Participant: session.ParticipantBinding{
					Kind: session.ParticipantSubagent,
				},
			},
			Message: &model.Message{
				Role:  model.RoleAssistant,
				Usage: &model.Usage{InputTokens: 7, OutputTokens: 2, TotalTokens: 9, ContextWindowTokens: 64000},
			},
		}, {
			ID:   "evt-compact-usage",
			Type: session.EventCompact,
			Message: &model.Message{
				Role:  model.RoleUser,
				Parts: []model.Part{model.NewTextPart("CONTEXT CHECKPOINT\nsummary")},
				Usage: &model.Usage{InputTokens: 4, OutputTokens: 1, TotalTokens: 5, ContextWindowTokens: 256000},
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
	if status.Usage.Main.InputTokens != 10 || status.Usage.Main.CachedInputTokens != 2 || status.Usage.Main.OutputTokens != 3 || status.Usage.Main.ReasoningTokens != 1 || status.Usage.Main.TotalTokens != 14 {
		t.Fatalf("main usage = %#v, want assistant usage", status.Usage.Main)
	}
	if status.Usage.AutoReview.InputTokens != 5 || status.Usage.AutoReview.OutputTokens != 1 || status.Usage.AutoReview.ReasoningTokens != 1 || status.Usage.AutoReview.TotalTokens != 6 {
		t.Fatalf("auto-review usage = %#v, want review usage", status.Usage.AutoReview)
	}
	if status.Usage.Subagents.InputTokens != 7 || status.Usage.Subagents.OutputTokens != 2 || status.Usage.Subagents.TotalTokens != 9 {
		t.Fatalf("subagent usage = %#v, want participant usage", status.Usage.Subagents)
	}
	if status.Usage.Compaction.InputTokens != 4 || status.Usage.Compaction.OutputTokens != 1 || status.Usage.Compaction.TotalTokens != 5 {
		t.Fatalf("compaction usage = %#v, want compact usage", status.Usage.Compaction)
	}
	if status.Usage.Total.InputTokens != 26 || status.Usage.Total.CachedInputTokens != 2 || status.Usage.Total.OutputTokens != 7 || status.Usage.Total.ReasoningTokens != 2 || status.Usage.Total.TotalTokens != 34 || status.Usage.Total.ContextWindowTokens != 256000 {
		t.Fatalf("total usage = %#v, want accumulated usage with max context window", status.Usage.Total)
	}
	budget := status.Usage.ContextBudget
	if budget.Source != contextBudgetSourceEstimated || !budget.PostCompact || budget.LastCompactEventID != "evt-compact-usage" || budget.AsOfEventID != "evt-2" {
		t.Fatalf("context budget identity = %#v, want estimated post-compact budget through evt-2", budget)
	}
	if budget.ContextWindowTokens != 128000 || budget.MaxOutputTokens != 4096 || budget.EffectiveInputBudget != 123904 {
		t.Fatalf("context budget limits = %#v, want model context window minus max output", budget)
	}
	if budget.MessageCount != 1 || budget.EstimatedHistoryTokens <= 0 || budget.EstimatedPrefixTokens <= 0 {
		t.Fatalf("context budget estimate = %#v, want compact checkpoint history plus prompt prefix", budget)
	}
	if budget.EstimatedInputTokens != budget.EstimatedHistoryTokens+budget.EstimatedPrefixTokens {
		t.Fatalf("context input estimate = %#v, want history plus prefix", budget)
	}
	if budget.EstimatedRemainingTokens != budget.EffectiveInputBudget-budget.EstimatedInputTokens || budget.EstimatedOverBudgetTokens != 0 {
		t.Fatalf("context remaining budget = %#v, want effective minus estimated input", budget)
	}
	directBudget, err := svc.Compaction().ContextBudget(ctx, ContextBudgetRequest{SessionRef: session.Ref{SessionID: "sess-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if directBudget != budget {
		t.Fatalf("direct context budget = %#v, want status budget %#v", directBudget, budget)
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

func TestTaskServiceListsAndControlsSandboxTasks(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	taskSession := &recordingTaskSession{
		snapshot: sandbox.SessionSnapshot{
			Ref:           sandbox.SessionRef{ID: "task-1", Backend: sandbox.BackendHost},
			Command:       "cat",
			Dir:           "/repo",
			State:         sandbox.SessionRunning,
			Running:       true,
			SupportsInput: true,
			StartedAt:     now,
			UpdatedAt:     now,
			Terminal:      sandbox.TerminalRef{ID: "term-1", SessionID: "task-1"},
		},
		stdout: "hello world\n",
		stderr: "warn\n",
	}
	svc, err := New(Config{
		Runtime: config.Runtime{AppName: "caelis", UserID: "tester"},
		Engine:  &recordingEngine{},
		Sandbox: &recordingTaskRuntime{sessions: map[string]*recordingTaskSession{"task-1": taskSession}},
	})
	if err != nil {
		t.Fatal(err)
	}

	list, err := svc.Tasks().List(context.Background(), ListTasksRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !list.Supported || list.Count != 1 || list.Tasks[0].ID != "task-1" || list.Tasks[0].TerminalID != "term-1" {
		t.Fatalf("task list = %#v, want one supported task", list)
	}
	list.Tasks[0].Command = "mutated"
	list, err = svc.Tasks().List(context.Background(), ListTasksRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if list.Tasks[0].Command != "cat" {
		t.Fatalf("task list was not rebuilt from snapshots: %#v", list.Tasks[0])
	}

	tail, err := svc.Tasks().Tail(context.Background(), TaskOutputRequest{TaskID: " task-1 ", StdoutCursor: 6})
	if err != nil {
		t.Fatal(err)
	}
	if tail.Stdout != "world\n" || tail.Stderr != "warn\n" || tail.StdoutCursor != int64(len(taskSession.stdout)) {
		t.Fatalf("tail = %#v, want cursor-based output", tail)
	}

	wrote, err := svc.Tasks().Write(context.Background(), TaskWriteRequest{TaskOutputRequest: TaskOutputRequest{TaskID: "task-1"}, Input: "ping\n", YieldTimeMS: -1})
	if err != nil {
		t.Fatal(err)
	}
	if taskSession.wrote != "ping\n" || !wrote.Task.Running {
		t.Fatalf("write result = %#v wrote=%q, want running task with stdin", wrote, taskSession.wrote)
	}

	cancelled, err := svc.Tasks().Cancel(context.Background(), TaskCancelRequest{TaskOutputRequest: TaskOutputRequest{TaskID: "task-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Task.State != string(sandbox.SessionCancelled) || cancelled.Task.Running {
		t.Fatalf("cancelled task = %#v, want cancelled non-running task", cancelled.Task)
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

func TestSessionServiceListCanOmitWorkspaceFilters(t *testing.T) {
	engine := &recordingEngine{}
	svc, err := New(Config{
		Runtime: config.Runtime{AppName: "caelis-app", UserID: "tester", WorkspaceKey: "repo", WorkspaceCWD: "/tmp/repo"},
		Engine:  engine,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Sessions().List(context.Background(), ListSessionsRequest{AllWorkspaces: true, Limit: 20}); err != nil {
		t.Fatal(err)
	}
	if engine.list.Ref.AppName != "caelis-app" || engine.list.Ref.UserID != "tester" {
		t.Fatalf("list ref = %#v, want runtime app/user defaults", engine.list.Ref)
	}
	if engine.list.Ref.WorkspaceKey != "" || engine.list.WorkspaceCWD != "" {
		t.Fatalf("list query = %#v, want no workspace filters", engine.list)
	}
}

func TestSessionServiceListDerivesMissingTitlesFromCanonicalEvents(t *testing.T) {
	engine := &recordingEngine{
		page: session.SessionPage{
			Sessions: []session.SessionSummary{{
				Session: session.Session{
					Ref:       session.Ref{AppName: "caelis-app", UserID: "tester", SessionID: "sess-derived", WorkspaceKey: "repo"},
					Workspace: session.Workspace{Key: "repo", CWD: "/tmp/repo"},
				},
			}},
		},
		snapshot: session.Snapshot{
			Session: session.Session{Ref: session.Ref{AppName: "caelis-app", UserID: "tester", SessionID: "sess-derived", WorkspaceKey: "repo"}},
			Events: []session.Event{
				{
					Type:       session.EventUser,
					Visibility: session.VisibilityUIOnly,
					Message:    &model.Message{Role: model.RoleUser, Parts: []model.Part{model.NewTextPart("ignore transient")}},
				},
				{
					Type:    session.EventAssistant,
					Message: &model.Message{Role: model.RoleAssistant, Parts: []model.Part{model.NewTextPart("assistant fallback")}},
				},
				{
					Type:    session.EventUser,
					Message: &model.Message{Role: model.RoleUser, Parts: []model.Part{model.NewTextPart("  migrate\ncanonical   session titles  ")}},
				},
			},
		},
	}
	svc, err := New(Config{
		Runtime: config.Runtime{AppName: "caelis-app", UserID: "tester", WorkspaceKey: "repo", WorkspaceCWD: "/tmp/repo"},
		Engine:  engine,
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := svc.Sessions().List(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sessions) != 1 || page.Sessions[0].Session.Title != "migrate canonical session titles" {
		t.Fatalf("page = %#v, want title derived from first durable user event", page)
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

func TestModelServiceProviderModelsMergesConfiguredAndRemoteModels(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpsertModel(ctx, appsettings.ModelConfig{
		Provider: "openai-compatible",
		Model:    "gpt-configured",
		BaseURL:  "https://api.example.test/v1",
	}); err != nil {
		t.Fatal(err)
	}
	var captured appsettings.ModelConfig
	svc, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis-app", UserID: "tester", WorkspaceKey: "repo"},
		Engine:   &recordingEngine{},
		Settings: manager,
		ModelProvider: func(_ context.Context, cfg appsettings.ModelConfig) (model.Provider, error) {
			captured = cfg
			return catalogProvider{models: []model.ModelInfo{
				{ID: "gpt-remote", Provider: "openai-compatible"},
				{ID: "gpt-configured", Provider: "openai-compatible"},
				{Name: "gpt-named", Provider: "openai-compatible"},
			}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	models, err := svc.Models().ProviderModels(ctx, appsettings.ModelConfig{
		Provider: "openai-compatible",
		BaseURL:  "https://api.example.test/v1",
		Token:    "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(models, []string{"gpt-configured", "gpt-named", "gpt-remote"}) {
		t.Fatalf("provider models = %#v, want configured and remote models", models)
	}
	if captured.Provider != "openai-compatible" || captured.BaseURL != "https://api.example.test/v1" || captured.Token != "secret" {
		t.Fatalf("provider factory cfg = %#v, want connect candidate config", captured)
	}
}

func TestModelServiceSelectionViewProjectsProvidersAndCandidates(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := manager.UpsertModel(ctx, appsettings.ModelConfig{
		Provider: "openai-compatible",
		Model:    "gpt-configured",
		BaseURL:  "https://api.example.test/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpsertModel(ctx, appsettings.ModelConfig{
		Provider: "minimax",
		Model:    "MiniMax-M2.7-highspeed",
		BaseURL:  "https://api.minimaxi.com/anthropic",
	}); err != nil {
		t.Fatal(err)
	}
	var captured appsettings.ModelConfig
	svc, err := New(Config{
		Runtime: config.Runtime{AppName: "caelis-app", UserID: "tester", WorkspaceKey: "repo"},
		Engine: &recordingEngine{snapshot: session.Snapshot{
			Session: session.Session{Ref: session.Ref{AppName: "caelis-app", UserID: "tester", SessionID: "sess-model", WorkspaceKey: "repo"}},
			State:   session.State{StateCurrentModelID: cfg.ID},
		}},
		Settings: manager,
		Resources: appresources.Catalog{
			ModelProviders: []plugin.FactoryAlias{{
				Name:        "reviewer-openai",
				Uses:        "openai-compatible",
				Description: "plugin OpenAI profile",
			}},
		},
		ModelProvider: func(_ context.Context, cfg appsettings.ModelConfig) (model.Provider, error) {
			captured = cfg
			return catalogProvider{models: []model.ModelInfo{
				{ID: "gpt-remote", Provider: "openai-compatible"},
				{ID: "gpt-configured", Provider: "openai-compatible"},
			}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := svc.Models().Selection(ctx, ModelSelectionRequest{
		SessionRef: session.Ref{SessionID: "sess-model"},
		Provider:   "openai-compatible",
		Discovery: appsettings.ModelConfig{
			BaseURL: "https://api.example.test/v1",
			Token:   "secret",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Current == nil || view.Current.ID != cfg.ID || len(view.Configured) != 2 {
		t.Fatalf("selection current/configured = %#v/%#v, want selected model and two configured models", view.Current, view.Configured)
	}
	provider, ok := findModelProviderOption(view.Providers, "openai-compatible")
	if !ok || !provider.Builtin || !provider.Configured || provider.ConfiguredModelCount != 1 || provider.CatalogModelCount == 0 || !provider.RemoteDiscovery {
		t.Fatalf("openai-compatible provider option = %#v ok=%v, want builtin/configured/remote provider", provider, ok)
	}
	pluginProvider, ok := findModelProviderOption(view.Providers, "reviewer-openai")
	if !ok || !pluginProvider.Plugin || pluginProvider.Uses != "openai-compatible" || pluginProvider.Description != "plugin OpenAI profile" {
		t.Fatalf("plugin provider option = %#v ok=%v, want plugin alias", pluginProvider, ok)
	}
	configured, ok := findModelCandidate(view.Candidates, "gpt-configured")
	if !ok || !configured.Configured || configured.Remote {
		t.Fatalf("configured candidate = %#v ok=%v, want configured only candidate", configured, ok)
	}
	catalog, ok := findModelCandidate(view.Candidates, "gpt-4o")
	if !ok || !catalog.Catalog || !catalog.CapabilitiesKnown || !catalog.Capabilities.SupportsImages {
		t.Fatalf("catalog candidate = %#v ok=%v, want catalog candidate with image capabilities", catalog, ok)
	}
	remote, ok := findModelCandidate(view.Candidates, "gpt-remote")
	if !ok || !remote.Remote || remote.Configured || remote.Catalog {
		t.Fatalf("remote candidate = %#v ok=%v, want remote-only candidate", remote, ok)
	}
	if captured.Provider != "openai-compatible" || captured.BaseURL != "https://api.example.test/v1" || captured.Token != "secret" {
		t.Fatalf("provider factory cfg = %#v, want discovery config with selected provider", captured)
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
	start      session.StartRequest
	list       session.ListQuery
	page       session.SessionPage
	turn       coreruntime.TurnRequest
	events     []session.Event
	turnEvents []session.Event
	loadRef    session.Ref
	state      session.State
	snapshot   session.Snapshot
}

type fakeSandboxRuntime struct {
	descriptor sandbox.Descriptor
	status     sandbox.Status
}

type catalogProvider struct {
	models []model.ModelInfo
}

type compactSummaryProvider struct {
	request  model.Request
	response string
	usage    *model.Usage
}

type emptyCompactProvider struct{}

func (emptyCompactProvider) ID() string {
	return "empty-compact"
}

func (emptyCompactProvider) Models(context.Context) ([]model.ModelInfo, error) {
	return []model.ModelInfo{{ID: "gpt-empty", Provider: "openai-compatible"}}, nil
}

func (emptyCompactProvider) Stream(context.Context, model.Request) (model.Stream, error) {
	return &model.StaticStream{}, nil
}

func (p *compactSummaryProvider) ID() string {
	return "compact-summary"
}

func (p *compactSummaryProvider) Models(context.Context) ([]model.ModelInfo, error) {
	return []model.ModelInfo{{ID: "gpt-compact", Provider: "openai-compatible"}}, nil
}

func (p *compactSummaryProvider) Stream(_ context.Context, req model.Request) (model.Stream, error) {
	p.request = model.Request{
		Model:    req.Model,
		Messages: cloneModelMessages(req.Messages),
		Stream:   req.Stream,
	}
	response := model.Response{
		Status: model.ResponseCompleted,
		Message: model.Message{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart(p.response)},
		},
		Usage: p.usage,
	}
	return &model.StaticStream{Events: []model.StreamEvent{{
		Type:     model.StreamTurnDone,
		Response: &response,
	}}}, nil
}

func cloneModelMessages(in []model.Message) []model.Message {
	if len(in) == 0 {
		return nil
	}
	out := make([]model.Message, 0, len(in))
	for _, message := range in {
		out = append(out, model.CloneMessage(message))
	}
	return out
}

func findModelProviderOption(options []appviewmodel.ModelProviderOption, id string) (appviewmodel.ModelProviderOption, bool) {
	for _, option := range options {
		if option.ID == id {
			return option, true
		}
	}
	return appviewmodel.ModelProviderOption{}, false
}

func findModelCandidate(candidates []appviewmodel.ModelCandidate, modelName string) (appviewmodel.ModelCandidate, bool) {
	for _, candidate := range candidates {
		if candidate.Model == modelName {
			return candidate, true
		}
	}
	return appviewmodel.ModelCandidate{}, false
}

func (p catalogProvider) ID() string {
	return "catalog"
}

func (p catalogProvider) Models(context.Context) ([]model.ModelInfo, error) {
	return append([]model.ModelInfo(nil), p.models...), nil
}

func (catalogProvider) Stream(context.Context, model.Request) (model.Stream, error) {
	return nil, errors.New("not implemented")
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

type recordingTaskRuntime struct {
	sessions map[string]*recordingTaskSession
}

func (r *recordingTaskRuntime) Descriptor() sandbox.Descriptor {
	return sandbox.Descriptor{Backend: sandbox.BackendHost}
}

func (r *recordingTaskRuntime) Status() sandbox.Status {
	return sandbox.Status{RequestedBackend: sandbox.BackendHost, ResolvedBackend: sandbox.BackendHost}
}

func (*recordingTaskRuntime) FileSystem() sandbox.FileSystem {
	return nil
}

func (*recordingTaskRuntime) Run(context.Context, sandbox.CommandRequest) (sandbox.CommandResult, error) {
	return sandbox.CommandResult{}, errors.New("not implemented")
}

func (*recordingTaskRuntime) Start(context.Context, sandbox.CommandRequest) (sandbox.Session, error) {
	return nil, errors.New("not implemented")
}

func (r *recordingTaskRuntime) Open(_ context.Context, ref sandbox.SessionRef) (sandbox.Session, error) {
	session, ok := r.sessions[strings.TrimSpace(ref.ID)]
	if !ok {
		return nil, fmt.Errorf("unknown task %q", ref.ID)
	}
	return session, nil
}

func (r *recordingTaskRuntime) ListSessions(context.Context, sandbox.SessionListQuery) ([]sandbox.SessionSnapshot, error) {
	out := make([]sandbox.SessionSnapshot, 0, len(r.sessions))
	for _, session := range r.sessions {
		out = append(out, session.snapshot)
	}
	return out, nil
}

func (*recordingTaskRuntime) Close() error {
	return nil
}

type recordingTaskSession struct {
	snapshot sandbox.SessionSnapshot
	stdout   string
	stderr   string
	wrote    string
}

func (s *recordingTaskSession) Ref() sandbox.SessionRef {
	return s.snapshot.Ref
}

func (s *recordingTaskSession) Snapshot(context.Context) (sandbox.SessionSnapshot, error) {
	return s.snapshot, nil
}

func (s *recordingTaskSession) Read(_ context.Context, cursor sandbox.OutputCursor) (sandbox.OutputSnapshot, error) {
	stdoutCursor := clampCursor(cursor.Stdout, s.stdout)
	stderrCursor := clampCursor(cursor.Stderr, s.stderr)
	return sandbox.OutputSnapshot{
		Stdout: s.stdout[stdoutCursor:],
		Stderr: s.stderr[stderrCursor:],
		Cursor: sandbox.OutputCursor{
			Stdout: int64(len(s.stdout)),
			Stderr: int64(len(s.stderr)),
		},
	}, nil
}

func (s *recordingTaskSession) Write(_ context.Context, input []byte) error {
	s.wrote += string(input)
	return nil
}

func (s *recordingTaskSession) Cancel(context.Context) error {
	s.snapshot.State = sandbox.SessionCancelled
	s.snapshot.Running = false
	return nil
}

func (s *recordingTaskSession) Wait(ctx context.Context) (sandbox.CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return sandbox.CommandResult{}, err
	}
	return sandbox.CommandResult{}, nil
}

func (*recordingTaskSession) Close() error {
	return nil
}

func clampCursor(cursor int64, text string) int64 {
	if cursor < 0 {
		return 0
	}
	if cursor > int64(len(text)) {
		return int64(len(text))
	}
	return cursor
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
	events := make(chan coreruntime.EventEnvelope, len(e.turnEvents))
	for _, event := range e.turnEvents {
		events <- coreruntime.EventEnvelope{Event: session.CloneEvent(event)}
	}
	close(events)
	return staticTurn{events: events}, nil
}

func collectRuntimeTurnEvents(t *testing.T, turn coreruntime.Turn) []session.Event {
	t.Helper()
	var out []session.Event
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatalf("turn event error: %s", env.Err)
		}
		out = append(out, session.CloneEvent(env.Event))
	}
	return out
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

type recordingRuntimeTurn struct {
	ref        session.Ref
	submission coreruntime.Submission
}

func (t *recordingRuntimeTurn) ID() string {
	return "turn"
}

func (t *recordingRuntimeTurn) RunID() string {
	return "run"
}

func (t *recordingRuntimeTurn) SessionRef() session.Ref {
	return t.ref
}

func (t *recordingRuntimeTurn) StartedAt() time.Time {
	return time.Time{}
}

func (t *recordingRuntimeTurn) Events() <-chan coreruntime.EventEnvelope {
	events := make(chan coreruntime.EventEnvelope)
	close(events)
	return events
}

func (t *recordingRuntimeTurn) Submit(_ context.Context, submission coreruntime.Submission) error {
	t.submission = submission
	return nil
}

func (t *recordingRuntimeTurn) Cancel() coreruntime.CancelResult {
	return coreruntime.CancelResult{Status: coreruntime.CancelCancelled}
}

func (*recordingRuntimeTurn) Close() error {
	return nil
}
