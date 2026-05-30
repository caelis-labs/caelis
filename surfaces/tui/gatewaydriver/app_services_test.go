package gatewaydriver

import (
	"context"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/core/config"
	coremodel "github.com/OnslaughtSnail/caelis/core/model"
	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	coresandbox "github.com/OnslaughtSnail/caelis/core/sandbox"
	coresession "github.com/OnslaughtSnail/caelis/core/session"
	sandboxhost "github.com/OnslaughtSnail/caelis/internal/adapters/sandbox/host"
	appservices "github.com/OnslaughtSnail/caelis/internal/app/services"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
	"github.com/OnslaughtSnail/caelis/kernel"
	portsession "github.com/OnslaughtSnail/caelis/ports/session"
)

func TestCoreEventMetaMergesToolRuntimeMeta(t *testing.T) {
	got := coreEventMeta(coresession.Event{
		Meta: map[string]any{
			"caelis": map[string]any{
				"runtime": map[string]any{
					"stream": map[string]any{"parent_call_id": "spawn-1"},
				},
			},
		},
		Tool: &coresession.ToolEvent{
			Meta: map[string]any{
				"caelis": map[string]any{
					"runtime": map[string]any{
						"tool": map[string]any{"diff_hunks": []any{"hunk-1"}},
					},
				},
			},
		},
	})
	runtimeMeta := got["caelis"].(map[string]any)["runtime"].(map[string]any)
	if runtimeMeta["stream"] == nil || runtimeMeta["tool"] == nil {
		t.Fatalf("meta = %#v, want stream and tool runtime metadata", got)
	}
}

func TestBindAppServicesRoutesModelModeAndStatus(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := manager.UpsertModel(ctx, appsettings.ModelConfig{
		Alias:                  "test-model",
		Provider:               "openai-compatible",
		Model:                  "gpt-test",
		BaseURL:                "https://api.example.test/v1",
		DefaultReasoningEffort: "low",
		ReasoningMode:          "fixed",
		ReasoningLevels:        []string{"low", "high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := &appServiceDriverEngine{}
	codeFreeAuth := &appServiceDriverCodeFreeAuth{}
	var providerDiscovery appsettings.ModelConfig
	workspaceCWD := t.TempDir()
	sandboxRuntime, err := sandboxhost.New(ctx, coresandbox.Config{CWD: workspaceCWD})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := appservices.New(appservices.Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "user-1",
			WorkspaceKey: "repo",
			WorkspaceCWD: workspaceCWD,
			Store:        config.Store{Backend: "sqlite", URI: "/tmp/caelis-app-service.sqlite"},
			Sandbox:      config.Sandbox{Backend: "host"},
		},
		Engine:   engine,
		Sandbox:  sandboxRuntime,
		Settings: manager,
		CodeFree: codeFreeAuth,
		ModelProvider: func(_ context.Context, cfg appsettings.ModelConfig) (coremodel.Provider, error) {
			providerDiscovery = cfg
			return appServiceDriverModelCatalog{models: []coremodel.ModelInfo{{ID: "gpt-remote", Provider: "openai-compatible"}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	stack := BindAppServices(&DriverStack{}, svc)
	driver, err := NewGatewayDriver(ctx, stack, "sess-app", "surface", "")
	if err != nil {
		t.Fatalf("NewGatewayDriver() error = %v", err)
	}
	if engine.start.PreferredSessionID != "sess-app" {
		t.Fatalf("StartSession() preferred id = %q, want sess-app", engine.start.PreferredSessionID)
	}
	choices, err := stack.ListModelChoices(ctx, portsession.SessionRef{SessionID: "sess-app"})
	if err != nil {
		t.Fatal(err)
	}
	if len(choices) != 1 || choices[0].ID != cfg.ID || choices[0].Alias != "test-model" {
		t.Fatalf("model choices = %#v, want bound app settings model", choices)
	}
	models, err := driver.CompleteSlashArg(ctx, "connect-model:volcengine|https%3A%2F%2Fark.cn-beijing.volces.com%2Fapi%2Fv3|60|secret|", "", 20)
	if err != nil {
		t.Fatalf("CompleteSlashArg(connect-model) error = %v", err)
	}
	if !slashCandidatesHaveValue(models, "doubao-seed-2.0-code") {
		t.Fatalf("connect model candidates = %#v, want app-service provider catalog model", models)
	}
	models, err = driver.CompleteSlashArg(ctx, "connect-model:openai-compatible|https%3A%2F%2Fapi.example.test%2Fv1|60|secret|", "remote", 20)
	if err != nil {
		t.Fatalf("CompleteSlashArg(connect-model remote) error = %v", err)
	}
	if !slashCandidatesHaveValue(models, "gpt-remote") {
		t.Fatalf("connect model candidates = %#v, want remote provider model", models)
	}
	if providerDiscovery.Provider != "openai-compatible" || providerDiscovery.BaseURL != "https://api.example.test/v1" || providerDiscovery.Token != "secret" {
		t.Fatalf("provider discovery cfg = %#v, want connect wizard provider config", providerDiscovery)
	}
	defaults, err := connectDefaultsForConfigWithStack(ctx, stack, ConnectConfig{Provider: "deepseek", Model: "deepseek-v4-pro"})
	if err != nil {
		t.Fatalf("connect defaults error = %v", err)
	}
	if defaults.ContextWindow != 1048576 || defaults.MaxOutput != 32768 || !equalStrings(defaults.ReasoningLevels, []string{"none", "high", "max"}) {
		t.Fatalf("connect defaults = %#v, want app-service capability catalog", defaults)
	}
	connected, err := stack.Connect(ModelConfig{
		Alias:    "next-model",
		Provider: "openai-compatible",
		Model:    "gpt-next",
		BaseURL:  "https://api.example.test/v1",
	})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if connected != "next-model" {
		t.Fatalf("Connect() = %q, want next-model", connected)
	}
	if _, err := driver.Connect(ctx, ConnectConfig{Provider: "codefree", Model: "GLM-4.7"}); err != nil {
		t.Fatalf("Connect(codefree) error = %v", err)
	}
	if !codeFreeAuth.ensure.OpenBrowser || codeFreeAuth.ensure.BaseURL != "https://www.srdcloud.cn" {
		t.Fatalf("codefree ensure req = %#v, want browser auth through app service", codeFreeAuth.ensure)
	}
	if _, err := driver.CompleteSlashArg(ctx, "connect-model:codefree|https%3A%2F%2Fwww.srdcloud.cn|60||", "", 20); err != nil {
		t.Fatalf("CompleteSlashArg(codefree model) error = %v", err)
	}
	if !codeFreeAuth.modelSelection.OpenBrowser || codeFreeAuth.modelSelection.BaseURL != "https://www.srdcloud.cn" {
		t.Fatalf("codefree model auth req = %#v, want model-selection auth through app service", codeFreeAuth.modelSelection)
	}
	if err := stack.DeleteModel(ctx, portsession.SessionRef{SessionID: "sess-app"}, "next-model"); err != nil {
		t.Fatalf("DeleteModel() error = %v", err)
	}
	if err := stack.CompactSession(ctx, portsession.SessionRef{SessionID: "sess-app"}); err != nil {
		t.Fatalf("CompactSession() error = %v", err)
	}
	if len(engine.events) != 1 || engine.events[0].Type != coresession.EventCompact {
		t.Fatalf("compact events = %#v, want app-service compact event", engine.events)
	}

	status, err := driver.UseModel(ctx, "test-model", "high")
	if err != nil {
		t.Fatalf("UseModel() error = %v", err)
	}
	if engine.state[appservices.StateCurrentModelID] != cfg.ID || engine.state[appservices.StateCurrentReasoningEffort] != "high" {
		t.Fatalf("session state after UseModel = %#v, want model and reasoning override", engine.state)
	}
	if status.Model != "test-model [high]" || status.Provider != "openai-compatible" || status.ModelName != "gpt-test" {
		t.Fatalf("status model = %q provider=%q name=%q, want app service model projection", status.Model, status.Provider, status.ModelName)
	}
	if status.SandboxResolvedBackend != "host" || status.Route != "host" {
		t.Fatalf("status sandbox = %#v, want host app runtime projection", status)
	}
	if status.StoreDir != "/tmp/caelis-app-service.sqlite" {
		t.Fatalf("status store = %q, want app-service store URI for /doctor", status.StoreDir)
	}
	status, err = driver.RepairSandbox(ctx)
	if err != nil {
		t.Fatalf("RepairSandbox() error = %v", err)
	}
	if status.SandboxResolvedBackend != "host" || status.Route != "host" || status.SandboxSetupRequired {
		t.Fatalf("repair sandbox status = %#v, want host no-op repair status", status)
	}

	status, err = driver.SetSessionMode(ctx, coreruntime.SessionModeManual)
	if err != nil {
		t.Fatalf("SetSessionMode() error = %v", err)
	}
	if engine.state[appservices.StateSessionMode] != coreruntime.SessionModeManual {
		t.Fatalf("session state after SetSessionMode = %#v, want manual", engine.state)
	}
	if status.SessionMode != coreruntime.SessionModeManual || status.ModeLabel != coreruntime.SessionModeManual {
		t.Fatalf("status mode = %q label=%q, want manual", status.SessionMode, status.ModeLabel)
	}
	status, err = driver.CycleSessionMode(ctx)
	if err != nil {
		t.Fatalf("CycleSessionMode() error = %v", err)
	}
	if engine.state[appservices.StateSessionMode] != coreruntime.SessionModeAutoReview || status.SessionMode != coreruntime.SessionModeAutoReview {
		t.Fatalf("session mode after cycle = state=%#v status=%q, want auto-review", engine.state, status.SessionMode)
	}

	turn, err := driver.Submit(ctx, Submission{Text: "  hello from tui  "})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if turn == nil {
		t.Fatal("Submit() turn = nil, want core app-service turn")
	}
	closeGatewayDriverTestTurn(t, turn)
	if engine.turn.SessionRef.SessionID != "sess-app" || engine.turn.Input != "hello from tui" || engine.turn.Surface != "surface" {
		t.Fatalf("turn request = %#v, want active session/input/surface from TUI driver", engine.turn)
	}
}

func TestBindAppServicesListSessionsUsesCanonicalUserPromptFallback(t *testing.T) {
	ctx := context.Background()
	engine := &appServiceDriverEngine{
		page: coresession.SessionPage{
			Sessions: []coresession.SessionSummary{{
				Session: coresession.Session{
					Ref: coresession.Ref{
						AppName:      "caelis",
						UserID:       "user-1",
						SessionID:    "sess-resume",
						WorkspaceKey: "repo",
					},
					Workspace: coresession.Workspace{Key: "repo", CWD: "/repo"},
				},
			}},
		},
		snapshot: coresession.Snapshot{
			Session: coresession.Session{
				Ref:       coresession.Ref{AppName: "caelis", UserID: "user-1", SessionID: "sess-resume", WorkspaceKey: "repo"},
				Workspace: coresession.Workspace{Key: "repo", CWD: "/repo"},
			},
			Events: []coresession.Event{{
				Type: coresession.EventUser,
				Message: &coremodel.Message{
					Role:  coremodel.RoleUser,
					Parts: []coremodel.Part{coremodel.NewTextPart("  resume this canonical prompt\nwith extra spacing  ")},
				},
			}},
		},
	}
	svc, err := appservices.New(appservices.Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "user-1",
			WorkspaceKey: "repo",
			WorkspaceCWD: "/repo",
		},
		Engine: engine,
	})
	if err != nil {
		t.Fatal(err)
	}
	driver, err := NewGatewayDriver(ctx, BindAppServices(&DriverStack{}, svc), "", "surface", "")
	if err != nil {
		t.Fatal(err)
	}

	candidates, err := driver.ListSessions(ctx, 10)
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("ListSessions() = %#v, want one prompt-backed candidate", candidates)
	}
	if candidates[0].SessionID != "sess-resume" || candidates[0].Prompt != "resume this canonical prompt with extra spacing" {
		t.Fatalf("candidate = %#v, want prompt fallback from canonical user event", candidates[0])
	}
}

func TestBindAppServicesAgentCatalogAndParticipantPrompt(t *testing.T) {
	ctx := context.Background()
	engine := &appServiceDriverEngine{}
	svc, err := appservices.New(appservices.Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "user-1",
			WorkspaceKey: "repo",
			WorkspaceCWD: "/repo",
		},
		Engine: engine,
		Agents: []appservices.AgentDescriptor{{
			ID:          "reviewer",
			Name:        "Reviewer",
			Kind:        appservices.AgentKindExternalACP,
			Command:     "reviewer-acp",
			Description: "review code through ACP",
		}},
		Invokers: map[string]appservices.AgentInvoker{
			"reviewer": appservices.AgentInvokerFunc(func(_ context.Context, req appservices.AgentInvokeRequest) (appservices.AgentInvokeResult, error) {
				return appservices.AgentInvokeResult{
					Events: []coresession.Event{{
						Type: coresession.EventAssistant,
						Message: &coremodel.Message{
							Role:  coremodel.RoleAssistant,
							Parts: []coremodel.Part{coremodel.NewTextPart("agent result for " + req.Input)},
						},
					}},
				}, nil
			}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	driver, err := NewGatewayDriver(ctx, BindAppServices(&DriverStack{}, svc), "sess-app", "surface", "")
	if err != nil {
		t.Fatal(err)
	}

	agents, err := driver.ListAgents(ctx, 10)
	if err != nil {
		t.Fatalf("ListAgents() error = %v", err)
	}
	if len(agents) != 1 || agents[0].Name != "reviewer" || agents[0].Description != "review code through ACP" {
		t.Fatalf("agents = %#v, want service-backed reviewer catalog", agents)
	}

	turn, err := driver.StartAgentSubagent(ctx, "reviewer", " inspect ", nil)
	if err != nil {
		t.Fatalf("StartAgentSubagent() error = %v", err)
	}
	got := drainGatewayDriverTestTurn(t, turn)
	if len(got) != 2 {
		t.Fatalf("turn events = %#v, want user prompt and agent response", got)
	}
	if got[0].Event.Kind != kernel.EventKindUserMessage || got[0].Event.Origin == nil || got[0].Event.Origin.Scope != kernel.EventScopeParticipant {
		t.Fatalf("first event = %#v, want participant-scoped user prompt", got[0].Event)
	}
	if got[1].Event.Kind != kernel.EventKindAssistantMessage || got[1].Event.Origin == nil || got[1].Event.Origin.ParticipantID != "reviewer" {
		t.Fatalf("second event = %#v, want participant-scoped assistant response", got[1].Event)
	}
	if len(engine.events) != 3 {
		t.Fatalf("recorded events = %#v, want attach, user prompt, assistant response", engine.events)
	}
	if engine.events[0].Type != coresession.EventParticipant || engine.events[1].Type != coresession.EventUser || engine.events[2].Type != coresession.EventAssistant {
		t.Fatalf("recorded event types = %#v, want participant/user/assistant", engine.events)
	}
	status, err := driver.AgentStatus(ctx)
	if err != nil {
		t.Fatalf("AgentStatus() error = %v", err)
	}
	if len(status.Participants) != 1 || status.Participants[0].ID != "reviewer" {
		t.Fatalf("agent status = %#v, want reviewer participant from canonical events", status)
	}
}

func TestBindAppServicesContinuesSidecarAfterDriverReload(t *testing.T) {
	ctx := context.Background()
	engine := &appServiceDriverEngine{}
	participantSessionIDs := []string{}
	svc, err := appservices.New(appservices.Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "user-1",
			WorkspaceKey: "repo",
			WorkspaceCWD: "/repo",
		},
		Engine: engine,
		Agents: []appservices.AgentDescriptor{{
			ID:          "reviewer",
			Name:        "reviewer",
			Kind:        appservices.AgentKindExternalACP,
			Command:     "reviewer-acp",
			Description: "review code through ACP",
		}},
		Invokers: map[string]appservices.AgentInvoker{
			"reviewer": appservices.AgentInvokerFunc(func(_ context.Context, req appservices.AgentInvokeRequest) (appservices.AgentInvokeResult, error) {
				participantSessionIDs = append(participantSessionIDs, req.Participant.SessionID)
				remoteSessionID := strings.TrimSpace(req.Participant.SessionID)
				if remoteSessionID == "" {
					remoteSessionID = "remote-reviewer"
				}
				participant := req.Participant
				participant.SessionID = remoteSessionID
				return appservices.AgentInvokeResult{
					Events: []coresession.Event{{
						Type: coresession.EventAssistant,
						Scope: &coresession.EventScope{
							Participant: participant,
						},
						Message: &coremodel.Message{
							Role:  coremodel.RoleAssistant,
							Parts: []coremodel.Part{coremodel.NewTextPart("agent result for " + req.Input)},
						},
					}},
				}, nil
			}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	driver, err := NewGatewayDriver(ctx, BindAppServices(&DriverStack{}, svc), "sess-app", "surface", "")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := driver.StartAgentSubagent(ctx, "reviewer", " inspect ", nil)
	if err != nil {
		t.Fatalf("StartAgentSubagent() error = %v", err)
	}
	drainGatewayDriverTestTurn(t, turn)

	reloaded, err := NewGatewayDriver(ctx, BindAppServices(&DriverStack{}, svc), "sess-app", "surface", "")
	if err != nil {
		t.Fatal(err)
	}
	status, err := reloaded.AgentStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Participants) != 1 || status.Participants[0].SessionID != "remote-reviewer" {
		t.Fatalf("reloaded status = %#v, want remote-reviewer participant session", status)
	}
	followup, err := reloaded.ContinueSubagent(ctx, "reviewer", " follow-up ", nil)
	if err != nil {
		t.Fatalf("ContinueSubagent() error = %v", err)
	}
	drainGatewayDriverTestTurn(t, followup)
	if len(participantSessionIDs) != 2 || participantSessionIDs[0] != "" || participantSessionIDs[1] != "remote-reviewer" {
		t.Fatalf("participant session ids = %#v, want initial empty then remote-reviewer", participantSessionIDs)
	}
}

func TestBindAppServicesRemovesStaticACPAgent(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := appservices.New(appservices.Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "user-1",
			WorkspaceKey: "repo",
			WorkspaceCWD: "/repo",
		},
		Engine:   &appServiceDriverEngine{},
		Settings: manager,
		Agents: []appservices.AgentDescriptor{{
			ID:          "reviewer",
			Name:        "reviewer",
			Kind:        appservices.AgentKindExternalACP,
			Command:     "reviewer-acp",
			Description: "review code through ACP",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	driver, err := NewGatewayDriver(ctx, BindAppServices(&DriverStack{}, svc), "sess-app", "surface", "")
	if err != nil {
		t.Fatal(err)
	}
	agents, err := driver.ListAgents(ctx, 10)
	if err != nil {
		t.Fatalf("ListAgents() error = %v", err)
	}
	if len(agents) != 1 || agents[0].Name != "reviewer" {
		t.Fatalf("agents before remove = %#v, want reviewer", agents)
	}
	if _, err := driver.RemoveAgent(ctx, "reviewer"); err != nil {
		t.Fatalf("RemoveAgent(static) error = %v", err)
	}
	agents, err = driver.ListAgents(ctx, 10)
	if err != nil {
		t.Fatalf("ListAgents(after remove) error = %v", err)
	}
	if len(agents) != 0 {
		t.Fatalf("agents after remove = %#v, want none", agents)
	}
	if disabled := manager.ListDisabledACPAgents(); len(disabled) != 1 || disabled[0] != "reviewer" {
		t.Fatalf("disabled agents = %#v, want reviewer", disabled)
	}
}

func TestBindAppServicesRegistersCustomACPAgent(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	engine := &appServiceDriverEngine{}
	var invokedAgent appservices.AgentDescriptor
	svc, err := appservices.New(appservices.Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "user-1",
			WorkspaceKey: "repo",
			WorkspaceCWD: "/repo",
		},
		Engine:   engine,
		Settings: manager,
		InvokerFactory: func(agent appservices.AgentDescriptor) (appservices.AgentInvoker, error) {
			invokedAgent = agent
			return appservices.AgentInvokerFunc(func(_ context.Context, req appservices.AgentInvokeRequest) (appservices.AgentInvokeResult, error) {
				return appservices.AgentInvokeResult{
					Events: []coresession.Event{{
						Type: coresession.EventAssistant,
						Message: &coremodel.Message{
							Role:  coremodel.RoleAssistant,
							Parts: []coremodel.Part{coremodel.NewTextPart("custom result for " + req.Input)},
						},
					}},
				}, nil
			}), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	driver, err := NewGatewayDriver(ctx, BindAppServices(&DriverStack{}, svc), "sess-app", "surface", "")
	if err != nil {
		t.Fatal(err)
	}
	status, err := driver.AddAgentWithOptions(ctx, "helper", AgentAddOptions{
		Custom: &CustomAgentConfig{
			Name:        "helper",
			Description: "custom helper",
			Command:     "helper-acp",
			Args:        []string{"--stdio"},
			Env:         map[string]string{"HELPER_TOKEN": "secret"},
		},
	})
	if err != nil {
		t.Fatalf("AddAgentWithOptions(custom) error = %v", err)
	}
	if len(status.AvailableAgents) != 1 || status.AvailableAgents[0].Name != "helper" {
		t.Fatalf("status agents = %#v, want helper", status.AvailableAgents)
	}
	agents, err := driver.ListAgents(ctx, 10)
	if err != nil {
		t.Fatalf("ListAgents() error = %v", err)
	}
	if len(agents) != 1 || agents[0].Name != "helper" || agents[0].Description != "custom helper" {
		t.Fatalf("agents = %#v, want custom helper", agents)
	}
	turn, err := driver.StartAgentSubagent(ctx, "helper", " inspect ", nil)
	if err != nil {
		t.Fatalf("StartAgentSubagent(custom) error = %v", err)
	}
	got := drainGatewayDriverTestTurn(t, turn)
	if len(got) != 2 || got[1].Event.Origin == nil || got[1].Event.Origin.ParticipantID != "helper" {
		t.Fatalf("turn events = %#v, want helper participant response", got)
	}
	if invokedAgent.ID != "helper" || invokedAgent.Env["HELPER_TOKEN"] != "secret" {
		t.Fatalf("invoked agent = %#v, want custom helper descriptor", invokedAgent)
	}
	status, err = driver.RemoveAgent(ctx, "helper")
	if err != nil {
		t.Fatalf("RemoveAgent(custom) error = %v", err)
	}
	if len(status.AvailableAgents) != 0 {
		t.Fatalf("status agents after remove = %#v, want none", status.AvailableAgents)
	}
}

func TestBindAppServicesRegistersBuiltinACPAgent(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := appservices.New(appservices.Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "user-1",
			WorkspaceKey: "repo",
			WorkspaceCWD: "/repo",
		},
		Engine:   &appServiceDriverEngine{},
		Settings: manager,
		BuiltinAgents: []appservices.AgentDescriptor{{
			ID:          "copilot",
			Name:        "copilot",
			Kind:        appservices.AgentKindExternalACP,
			Description: "GitHub Copilot ACP agent",
			Command:     "copilot",
			Args:        []string{"--acp", "--stdio"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	driver, err := NewGatewayDriver(ctx, BindAppServices(&DriverStack{}, svc), "sess-app", "surface", "")
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := driver.CompleteSlashArg(ctx, "agent add", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(agent add) error = %v", err)
	}
	if len(candidates) != 1 || candidates[0].Value != "copilot" || candidates[0].Detail != "GitHub Copilot ACP agent" {
		t.Fatalf("agent add candidates = %#v, want copilot builtin", candidates)
	}
	status, err := driver.AddAgent(ctx, "copilot")
	if err != nil {
		t.Fatalf("AddAgent(copilot) error = %v", err)
	}
	if len(status.AvailableAgents) != 1 || status.AvailableAgents[0].Name != "copilot" {
		t.Fatalf("status agents = %#v, want copilot", status.AvailableAgents)
	}
	if agents := manager.ListACPAgents(); len(agents) != 1 || agents[0].Name != "copilot" || agents[0].Command != "copilot" {
		t.Fatalf("settings agents = %#v, want persisted copilot", agents)
	}
	if _, err := driver.AddAgentWithOptions(ctx, "copilot", AgentAddOptions{Install: true}); err == nil {
		t.Fatal("AddAgentWithOptions(install) error = nil, want explicit unsupported install error")
	}
	status, err = driver.RemoveAgent(ctx, "copilot")
	if err != nil {
		t.Fatalf("RemoveAgent(copilot) error = %v", err)
	}
	if len(status.AvailableAgents) != 0 {
		t.Fatalf("status agents after remove = %#v, want none", status.AvailableAgents)
	}
}

func TestBindAppServicesInstallsBuiltinACPAgent(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	installer := &appServiceDriverAgentInstaller{}
	svc, err := appservices.New(appservices.Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "user-1",
			WorkspaceKey: "repo",
			WorkspaceCWD: "/repo",
		},
		Engine:   &appServiceDriverEngine{},
		Settings: manager,
		BuiltinAgents: []appservices.AgentDescriptor{{
			ID:          "codex",
			Name:        "codex",
			Kind:        appservices.AgentKindExternalACP,
			Description: "OpenAI Codex ACP agent",
			Command:     "npx",
			Args:        []string{"-y", "@zed-industries/codex-acp"},
		}},
		AgentInstaller: installer,
	})
	if err != nil {
		t.Fatal(err)
	}
	driver, err := NewGatewayDriver(ctx, BindAppServices(&DriverStack{}, svc), "sess-app", "surface", "")
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := driver.CompleteSlashArg(ctx, "agent install", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(agent install) error = %v", err)
	}
	if len(candidates) != 1 || candidates[0].Value != "codex" || candidates[0].Display != "codex (npm install)" {
		t.Fatalf("agent install candidates = %#v, want codex install option", candidates)
	}
	updateCandidates, err := driver.CompleteSlashArg(ctx, "agent update", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(agent update) error = %v", err)
	}
	if len(updateCandidates) != 1 || updateCandidates[0].Value != "codex" || updateCandidates[0].Display != "codex (npm install)" {
		t.Fatalf("agent update candidates = %#v, want codex install option", updateCandidates)
	}
	status, err := driver.AddAgentWithOptions(ctx, "codex", AgentAddOptions{Install: true})
	if err != nil {
		t.Fatalf("AddAgentWithOptions(codex install) error = %v", err)
	}
	if !installer.called || installer.agent.Name != "codex" {
		t.Fatalf("installer called=%v agent=%#v, want codex", installer.called, installer.agent)
	}
	if len(status.AvailableAgents) != 1 || status.AvailableAgents[0].Name != "codex" {
		t.Fatalf("status agents = %#v, want codex", status.AvailableAgents)
	}
	if agents := manager.ListACPAgents(); len(agents) != 1 || agents[0].Name != "codex" || agents[0].Command != "/installed/codex-acp" {
		t.Fatalf("settings agents = %#v, want installed codex", agents)
	}
	installer.called = false
	status, err = driver.AddAgentWithOptions(ctx, "codex", AgentAddOptions{Install: true})
	if err != nil {
		t.Fatalf("AddAgentWithOptions(codex update) error = %v", err)
	}
	if !installer.called || installer.agent.Name != "codex" {
		t.Fatalf("update installer called=%v agent=%#v, want codex", installer.called, installer.agent)
	}
	if len(status.AvailableAgents) != 1 || status.AvailableAgents[0].Name != "codex" {
		t.Fatalf("status agents after update = %#v, want codex", status.AvailableAgents)
	}
}

func TestBindAppServicesHandoffACPControllerAndRoutesPrompt(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpsertModel(ctx, appsettings.ModelConfig{
		Alias:           "remote-model",
		Provider:        "openai-compatible",
		Model:           "gpt-test",
		BaseURL:         "https://api.example.test/v1",
		ReasoningMode:   "fixed",
		ReasoningLevels: []string{"low", "high"},
	}); err != nil {
		t.Fatal(err)
	}
	engine := &appServiceDriverEngine{}
	var controllerRemoteIDs []string
	var controllerConfigs []string
	svc, err := appservices.New(appservices.Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "user-1",
			WorkspaceKey: "repo",
			WorkspaceCWD: "/repo",
		},
		Engine:   engine,
		Settings: manager,
		Agents: []appservices.AgentDescriptor{{
			ID:          "reviewer",
			Name:        "reviewer",
			Kind:        appservices.AgentKindExternalACP,
			Command:     "reviewer-acp",
			Description: "review code through ACP",
		}},
		Invokers: map[string]appservices.AgentInvoker{
			"reviewer": appservices.AgentInvokerFunc(func(_ context.Context, req appservices.AgentInvokeRequest) (appservices.AgentInvokeResult, error) {
				if req.Controller.Kind != coresession.ControllerACP || req.Controller.ID != "reviewer" {
					t.Fatalf("controller invoke = %#v, want reviewer ACP controller", req.Controller)
				}
				controllerRemoteIDs = append(controllerRemoteIDs, req.Controller.RemoteSessionID)
				controllerConfigs = append(controllerConfigs, req.ControllerModel+"/"+req.ControllerReasoningEffort+"/"+req.ControllerMode)
				controller := req.Controller
				controller.RemoteSessionID = "remote-reviewer"
				return appservices.AgentInvokeResult{
					Events: []coresession.Event{{
						Type:  coresession.EventAssistant,
						Scope: &coresession.EventScope{Controller: controller},
						Message: &coremodel.Message{
							Role:  coremodel.RoleAssistant,
							Parts: []coremodel.Part{coremodel.NewTextPart("controller result for " + req.Input)},
						},
					}},
				}, nil
			}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	driver, err := NewGatewayDriver(ctx, BindAppServices(&DriverStack{}, svc), "sess-app", "surface", "")
	if err != nil {
		t.Fatal(err)
	}
	status, err := driver.HandoffAgent(ctx, "reviewer")
	if err != nil {
		t.Fatalf("HandoffAgent(reviewer) error = %v", err)
	}
	if status.ControllerKind != "acp" || status.ControllerLabel != "reviewer" {
		t.Fatalf("status after handoff = %#v, want reviewer ACP controller", status)
	}
	modelStatus, err := driver.UseModel(ctx, "remote-model", "high")
	if err != nil {
		t.Fatalf("UseModel under ACP controller error = %v", err)
	}
	if engine.state[appservices.StateControllerModel] != "remote-model" || engine.state[appservices.StateControllerReasoning] != "high" {
		t.Fatalf("controller state after UseModel = %#v, want remote-model/high", engine.state)
	}
	if modelStatus.Model != "remote-model [high]" || modelStatus.Provider != "acp" {
		t.Fatalf("status after ACP UseModel = %#v, want remote ACP model projection", modelStatus)
	}
	setModeStatus, err := driver.SetSessionMode(ctx, coreruntime.SessionModeManual)
	if err != nil {
		t.Fatalf("SetSessionMode under ACP controller error = %v", err)
	}
	if engine.state[appservices.StateControllerMode] != coreruntime.SessionModeManual || setModeStatus.SessionMode != coreruntime.SessionModeManual {
		t.Fatalf("controller mode after set = state:%#v status:%#v, want manual", engine.state, setModeStatus)
	}
	if _, ok := engine.state[appservices.StateSessionMode]; ok {
		t.Fatalf("local session mode state = %#v, want unchanged under ACP controller", engine.state)
	}
	modeStatus, err := driver.CycleSessionMode(ctx)
	if err != nil {
		t.Fatalf("CycleSessionMode under ACP controller error = %v", err)
	}
	if engine.state[appservices.StateControllerMode] != coreruntime.SessionModeAutoReview || modeStatus.SessionMode != coreruntime.SessionModeAutoReview {
		t.Fatalf("controller mode after cycle = state:%#v status:%#v, want auto-review", engine.state, modeStatus)
	}
	turn, err := driver.Submit(ctx, Submission{Text: " inspect "})
	if err != nil {
		t.Fatalf("Submit under ACP controller error = %v", err)
	}
	got := drainGatewayDriverTestTurn(t, turn)
	if len(got) != 2 {
		t.Fatalf("turn events = %#v, want controller user prompt and response", got)
	}
	if len(engine.events) != 3 {
		t.Fatalf("recorded events = %#v, want handoff/user/assistant", engine.events)
	}
	if engine.events[0].Type != coresession.EventHandoff || engine.events[0].Scope == nil || engine.events[0].Scope.Controller.ID != "reviewer" {
		t.Fatalf("handoff event = %#v, want reviewer controller", engine.events[0])
	}
	if engine.events[1].Type != coresession.EventUser || engine.events[1].Scope == nil || engine.events[1].Scope.Controller.ID != "reviewer" {
		t.Fatalf("controller user event = %#v, want reviewer scope", engine.events[1])
	}
	if engine.events[2].Type != coresession.EventAssistant || engine.events[2].Scope == nil || engine.events[2].Scope.Controller.ID != "reviewer" {
		t.Fatalf("controller response event = %#v, want reviewer scope", engine.events[2])
	}
	if engine.events[2].Scope.Controller.RemoteSessionID != "remote-reviewer" {
		t.Fatalf("controller response remote session = %q, want remote-reviewer", engine.events[2].Scope.Controller.RemoteSessionID)
	}
	turn, err = driver.Submit(ctx, Submission{Text: " continue "})
	if err != nil {
		t.Fatalf("second Submit under ACP controller error = %v", err)
	}
	got = drainGatewayDriverTestTurn(t, turn)
	if len(got) != 2 {
		t.Fatalf("second turn events = %#v, want controller user prompt and response", got)
	}
	if len(controllerRemoteIDs) != 2 || controllerRemoteIDs[0] != "" || controllerRemoteIDs[1] != "remote-reviewer" {
		t.Fatalf("controller remote ids = %#v, want first empty then reused remote-reviewer", controllerRemoteIDs)
	}
	if len(controllerConfigs) != 2 || controllerConfigs[0] != "remote-model/high/auto-review" || controllerConfigs[1] != "remote-model/high/auto-review" {
		t.Fatalf("controller configs = %#v, want persisted controller config intent on prompts", controllerConfigs)
	}
	status, err = driver.HandoffAgent(ctx, "local")
	if err != nil {
		t.Fatalf("HandoffAgent(local) error = %v", err)
	}
	if status.ControllerKind != "kernel" {
		t.Fatalf("status after local handoff = %#v, want kernel controller", status)
	}
}

type appServiceDriverModelCatalog struct {
	models []coremodel.ModelInfo
}

func (p appServiceDriverModelCatalog) ID() string {
	return "app-service-driver-model-catalog"
}

func (p appServiceDriverModelCatalog) Models(context.Context) ([]coremodel.ModelInfo, error) {
	return append([]coremodel.ModelInfo(nil), p.models...), nil
}

func (appServiceDriverModelCatalog) Stream(context.Context, coremodel.Request) (coremodel.Stream, error) {
	return &coremodel.StaticStream{}, nil
}

type appServiceDriverEngine struct {
	start    coresession.StartRequest
	page     coresession.SessionPage
	state    coresession.State
	snapshot coresession.Snapshot
	events   []coresession.Event
	turn     coreruntime.TurnRequest
}

func (e *appServiceDriverEngine) StartSession(_ context.Context, req coresession.StartRequest) (coresession.Session, error) {
	e.start = req
	sessionID := req.PreferredSessionID
	if sessionID == "" {
		sessionID = "sess-app"
	}
	active := coresession.Session{
		Ref: coresession.Ref{
			AppName:      req.AppName,
			UserID:       req.UserID,
			SessionID:    sessionID,
			WorkspaceKey: req.Workspace.Key,
		},
		Workspace: req.Workspace,
		Title:     req.Title,
		Meta:      maps.Clone(req.Meta),
	}
	e.snapshot.Session = active
	if e.state == nil {
		e.state = coresession.State{}
	}
	e.snapshot.State = cloneCoreState(e.state)
	return active, nil
}

func (e *appServiceDriverEngine) ListSessions(context.Context, coresession.ListQuery) (coresession.SessionPage, error) {
	return coresession.CloneSessionPage(e.page), nil
}

func (e *appServiceDriverEngine) LoadSession(_ context.Context, ref coresession.Ref) (coresession.Snapshot, error) {
	snapshot := e.snapshot
	if snapshot.Session.SessionID == "" {
		snapshot.Session.Ref = ref
	}
	snapshot.Events = append(cloneCoreEvents(snapshot.Events), cloneCoreEvents(e.events)...)
	snapshot.State = cloneCoreState(e.state)
	return snapshot, nil
}

func (e *appServiceDriverEngine) RecordEvents(_ context.Context, _ coresession.Ref, events []coresession.Event) (coresession.Cursor, error) {
	e.events = append(e.events, cloneCoreEvents(events)...)
	return coresession.Cursor("test-cursor"), nil
}

func (e *appServiceDriverEngine) UpdateSessionState(_ context.Context, _ coresession.Ref, patch coresession.StatePatch) error {
	next, err := patch(cloneCoreState(e.state))
	if err != nil {
		return err
	}
	e.state = cloneCoreState(next)
	e.snapshot.State = cloneCoreState(next)
	return nil
}

func (e *appServiceDriverEngine) BeginTurn(_ context.Context, req coreruntime.TurnRequest) (coreruntime.Turn, error) {
	e.turn = req
	events := make(chan coreruntime.EventEnvelope)
	close(events)
	return appServiceDriverTurn{events: events}, nil
}

func (e *appServiceDriverEngine) Interrupt(context.Context, coresession.Ref) error {
	return nil
}

func (e *appServiceDriverEngine) Replay(context.Context, coreruntime.ReplayRequest) (<-chan coreruntime.EventEnvelope, error) {
	events := make(chan coreruntime.EventEnvelope)
	close(events)
	return events, nil
}

type appServiceDriverTurn struct {
	events <-chan coreruntime.EventEnvelope
}

func (t appServiceDriverTurn) ID() string {
	return "turn"
}

func (t appServiceDriverTurn) RunID() string {
	return "run"
}

func (t appServiceDriverTurn) SessionRef() coresession.Ref {
	return coresession.Ref{SessionID: "sess-app"}
}

func (t appServiceDriverTurn) StartedAt() time.Time {
	return time.Time{}
}

func (t appServiceDriverTurn) Events() <-chan coreruntime.EventEnvelope {
	return t.events
}

func (t appServiceDriverTurn) Submit(context.Context, coreruntime.Submission) error {
	return nil
}

func (t appServiceDriverTurn) Cancel() coreruntime.CancelResult {
	return coreruntime.CancelResult{Status: coreruntime.CancelCancelled}
}

func (t appServiceDriverTurn) Close() error {
	return nil
}

func cloneCoreState(in coresession.State) coresession.State {
	if in == nil {
		return nil
	}
	return coresession.State(maps.Clone(in))
}

func cloneCoreEvents(in []coresession.Event) []coresession.Event {
	out := make([]coresession.Event, 0, len(in))
	for _, event := range in {
		out = append(out, coresession.CloneEvent(event))
	}
	return out
}

type appServiceDriverCodeFreeAuth struct {
	ensure         appservices.CodeFreeAuthRequest
	modelSelection appservices.CodeFreeAuthRequest
	refresh        appservices.CodeFreeAuthRequest
}

func (a *appServiceDriverCodeFreeAuth) EnsureAuth(_ context.Context, req appservices.CodeFreeAuthRequest) (appservices.CodeFreeAuthResult, error) {
	a.ensure = req
	return appservices.CodeFreeAuthResult{CredentialPath: "/tmp/codefree.json", UserID: "user-1"}, nil
}

func (a *appServiceDriverCodeFreeAuth) EnsureModelSelectionAuth(_ context.Context, req appservices.CodeFreeAuthRequest) (appservices.CodeFreeAuthResult, error) {
	a.modelSelection = req
	return appservices.CodeFreeAuthResult{CredentialPath: "/tmp/codefree.json", UserID: "user-1", LoginStarted: true}, nil
}

func (a *appServiceDriverCodeFreeAuth) Refresh(_ context.Context, req appservices.CodeFreeAuthRequest) (appservices.CodeFreeAuthResult, error) {
	a.refresh = req
	return appservices.CodeFreeAuthResult{CredentialPath: "/tmp/codefree.json", UserID: "user-1"}, nil
}

type appServiceDriverAgentInstaller struct {
	called bool
	agent  appservices.AgentDescriptor
}

func (i *appServiceDriverAgentInstaller) InstallBuiltinACPAgent(_ context.Context, agent appservices.AgentDescriptor) (appservices.AgentDescriptor, error) {
	i.called = true
	i.agent = agent
	agent.Command = "/installed/" + agent.Name + "-acp"
	agent.Args = nil
	return agent, nil
}

func (i *appServiceDriverAgentInstaller) InstallableBuiltinACPAgentOptions(_ context.Context, builtins []appservices.AgentDescriptor) ([]appservices.AgentInstallOption, error) {
	out := make([]appservices.AgentInstallOption, 0, len(builtins))
	for _, agent := range builtins {
		out = append(out, appservices.AgentInstallOption{
			Value:   agent.Name,
			Display: agent.Name + " (npm install)",
			Detail:  "npm install " + agent.Name,
		})
	}
	return out, nil
}

func drainGatewayDriverTestTurn(t *testing.T, turn Turn) []kernel.EventEnvelope {
	t.Helper()
	if turn == nil {
		t.Fatal("turn = nil")
	}
	defer turn.Close()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	var out []kernel.EventEnvelope
	for {
		select {
		case env, ok := <-turn.Events():
			if !ok {
				return out
			}
			out = append(out, env)
		case <-timer.C:
			_ = turn.Close()
			t.Fatal("turn did not close")
		}
	}
}
