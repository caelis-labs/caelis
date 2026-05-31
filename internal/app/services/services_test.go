package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	"github.com/OnslaughtSnail/caelis/internal/engine/control"
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

func TestTurnBeginExpandsExplicitSkillReferencesIntoInstructions(t *testing.T) {
	engine := &recordingEngine{}
	skillDir := filepath.Join(t.TempDir(), "review")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("---\nname: review\ndescription: Review code.\n---\n# Review\n\nCheck correctness first.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc, err := New(Config{
		Engine: engine,
		Runtime: config.Runtime{
			AppName: "caelis-app",
			UserID:  "tester",
		},
		Resources: appresources.Catalog{
			Skills: []plugin.SkillDescriptor{{
				Name:        "review",
				Description: "Review code.",
				Paths:       []string{skillPath},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Turns().Begin(context.Background(), BeginTurnRequest{
		SessionRef: session.Ref{SessionID: "sess-1"},
		Input:      "Use $review on this patch and ignore $missing.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if engine.turn.Input != "Use $review on this patch and ignore $missing." {
		t.Fatalf("turn input = %q, want original input preserved", engine.turn.Input)
	}
	if len(engine.turn.Instructions) != 1 {
		t.Fatalf("instructions = %#v, want one expanded skill instruction", engine.turn.Instructions)
	}
	instruction := engine.turn.Instructions[0]
	for _, want := range []string{"## Skill: review", "Source: " + skillPath, "# Review", "Check correctness first."} {
		if !strings.Contains(instruction, want) {
			t.Fatalf("instruction = %q, missing %q", instruction, want)
		}
	}
}

func TestTurnBeginHonorsSkillLoadingPolicy(t *testing.T) {
	ctx := context.Background()
	skillDir := filepath.Join(t.TempDir(), "review")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("---\nname: review\ndescription: Review code.\n---\n# Review\n\nCheck correctness first.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{
		Skills: appsettings.SkillPolicy{
			LoadingMode:       appsettings.SkillLoadingModeExplicit,
			MaxExpansionChars: 12,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := &recordingEngine{}
	svc, err := New(Config{
		Engine:   engine,
		Settings: manager,
		Resources: appresources.Catalog{
			Skills: []plugin.SkillDescriptor{{
				Name:        "review",
				Description: "Review code.",
				Paths:       []string{skillPath},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Turns().Begin(ctx, BeginTurnRequest{SessionRef: session.Ref{SessionID: "sess-1"}, Input: "Use $review."}); err != nil {
		t.Fatal(err)
	}
	if len(engine.turn.Instructions) != 1 || !strings.Contains(engine.turn.Instructions[0], "[Skill content truncated by prompt budget.]") {
		t.Fatalf("instructions = %#v, want budget-truncated skill content", engine.turn.Instructions)
	}

	if _, err := manager.SetSkillPolicy(ctx, appsettings.SkillPolicy{LoadingMode: appsettings.SkillLoadingModeMetadataOnly}); err != nil {
		t.Fatal(err)
	}
	engine.turn = coreruntime.TurnRequest{}
	if _, err := svc.Turns().Begin(ctx, BeginTurnRequest{SessionRef: session.Ref{SessionID: "sess-1"}, Input: "Use $review."}); err != nil {
		t.Fatal(err)
	}
	if len(engine.turn.Instructions) != 0 {
		t.Fatalf("metadata-only instructions = %#v, want no skill expansion", engine.turn.Instructions)
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
		Skills: appsettings.SkillPolicy{
			LoadingMode:       "metadata-only",
			MaxExpansionChars: 32,
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

	initialStore, err := svc.Settings().SetStore(ctx, config.Store{Backend: "sqlite", URI: " /tmp/initial.db "})
	if err != nil {
		t.Fatal(err)
	}
	if initialStore.Runtime.AppName != "caelis" || initialStore.Runtime.UserID != "tester" || initialStore.Store.URI != "/tmp/initial.db" {
		t.Fatalf("initial store mutation view = %#v, want effective runtime identity preserved", initialStore)
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
	if view.Skills.LoadingMode != appsettings.SkillLoadingModeMetadataOnly || view.Skills.MaxExpansionChars != 0 {
		t.Fatalf("skill view = %#v, want metadata-only effective skill policy", view.Skills)
	}

	view.Sandbox.ReadableRoots[0] = "mutated"
	again, err := svc.Settings().View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again.Sandbox.ReadableRoots[0] != "/read" {
		t.Fatalf("settings view was not cloned: %#v", again.Sandbox.ReadableRoots)
	}
	runtime := svc.Runtime()
	if runtime.AppName != "caelis-app" || runtime.Store.Backend != "sqlite" || runtime.Sandbox.Backend != "host" {
		t.Fatalf("service runtime = %#v, want updated settings runtime", runtime)
	}

	storeView, err := svc.Settings().SetStore(ctx, config.Store{
		Backend: " JSONL ",
		URI:     " /tmp/events.jsonl ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if storeView.Runtime.AppName != "caelis-app" || storeView.Store.Backend != "jsonl" || storeView.Store.URI != "/tmp/events.jsonl" {
		t.Fatalf("store mutation view = %#v, want patched store preserving runtime identity", storeView)
	}
	if runtime := svc.Runtime(); runtime.Store.Backend != "jsonl" || runtime.Store.URI != "/tmp/events.jsonl" || runtime.Sandbox.Backend != "host" {
		t.Fatalf("service runtime after SetStore = %#v, want live store patch only", runtime)
	}

	sandboxView, err := svc.Settings().SetSandboxBackend(ctx, "windows elevated")
	if err != nil {
		t.Fatal(err)
	}
	if sandboxView.Sandbox.Backend != "windows" || sandboxView.Sandbox.ReadableRoots[0] != "/read" {
		t.Fatalf("sandbox backend view = %#v, want normalized backend preserving sandbox roots", sandboxView.Sandbox)
	}
	if _, err := svc.Settings().SetSandboxBackend(ctx, "unknown-sandbox"); err == nil {
		t.Fatal("SetSandboxBackend(unknown) error = nil, want validation error")
	}

	sandboxView, err = svc.Settings().SetSandbox(ctx, config.Sandbox{
		Backend:       " BWRAP ",
		Network:       " DISABLED ",
		ReadableRoots: []string{" /src "},
		WritableRoots: []string{" /out "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sandboxView.Sandbox.Backend != "bwrap" || sandboxView.Sandbox.Network != "disabled" || sandboxView.Sandbox.WritableRoots[0] != "/out" {
		t.Fatalf("sandbox mutation view = %#v, want normalized sandbox replacement", sandboxView.Sandbox)
	}

	compactionView, err := svc.Settings().SetCompaction(ctx, appsettings.CompactionPolicy{
		Prompt:         " summarize durable state ",
		MaxSourceChars: 256,
		Auto: appsettings.AutoCompactionPolicy{
			Mode:           "off",
			WatermarkRatio: 0.5,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if compactionView.Compaction.Prompt != "summarize durable state" || compactionView.Compaction.AutoMode != "disabled" || compactionView.Compaction.AutoWatermarkRatio != 0.5 {
		t.Fatalf("compaction mutation view = %#v, want normalized compaction settings", compactionView.Compaction)
	}

	skillView, err := svc.Settings().SetSkillPolicy(ctx, appsettings.SkillPolicy{
		LoadingMode:       "explicit",
		MaxExpansionChars: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	if skillView.Skills.LoadingMode != appsettings.SkillLoadingModeExplicit || skillView.Skills.MaxExpansionChars != 1024 {
		t.Fatalf("skill mutation view = %#v, want explicit skill policy", skillView.Skills)
	}

	panel, err := svc.Settings().SetPanelField(ctx, SettingsPanelFieldUpdateRequest{
		FieldID: "sandbox.writable_roots",
		Value:   " /out , /tmp/cache , /out ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(panel.Settings.Sandbox.WritableRoots, []string{"/out", "/tmp/cache"}) {
		t.Fatalf("panel writable roots = %#v, want normalized unique roots", panel.Settings.Sandbox.WritableRoots)
	}
	panel, err = svc.Settings().SetPanelField(ctx, SettingsPanelFieldUpdateRequest{
		FieldID: "compaction.watermark",
		Value:   "0.66",
	})
	if err != nil {
		t.Fatal(err)
	}
	if panel.Settings.Compaction.AutoWatermarkRatio != 0.66 {
		t.Fatalf("panel compaction watermark = %#v, want 0.66", panel.Settings.Compaction)
	}
	panel, err = svc.Settings().SetPanelField(ctx, SettingsPanelFieldUpdateRequest{
		FieldID: "skills.max_expansion_chars",
		Value:   "2048",
	})
	if err != nil {
		t.Fatal(err)
	}
	if panel.Settings.Skills.MaxExpansionChars != 2048 {
		t.Fatalf("panel skill budget = %#v, want 2048", panel.Settings.Skills)
	}
	if _, err := svc.Settings().SetPanelField(ctx, SettingsPanelFieldUpdateRequest{FieldID: "runtime.app_name", Value: "changed"}); err == nil {
		t.Fatal("SetPanelField(runtime.app_name) error = nil, want non-editable error")
	}
}

func TestSettingsServicePanelComposesDiagnosticsAndActions(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, appsettings.NewFileStore(t.TempDir()), appsettings.Document{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "tester",
			WorkspaceKey: "repo",
			WorkspaceCWD: "/repo",
			Sandbox: config.Sandbox{
				Backend: "windows",
				Network: "disabled",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt := &recordingLifecycleSandboxRuntime{
		fakeSandboxRuntime: fakeSandboxRuntime{
			descriptor: sandbox.Descriptor{
				Backend: sandbox.BackendWindows,
				DefaultConstraints: sandbox.Constraints{
					Route:   sandbox.RouteSandbox,
					Backend: sandbox.BackendWindows,
				},
			},
			status: sandbox.Status{
				RequestedBackend:    sandbox.BackendWindows,
				ResolvedBackend:     sandbox.BackendWindows,
				FallbackToHost:      true,
				FallbackReason:      "helper missing",
				FallbackInstallHint: "install helper",
				Setup: sandbox.SetupStatus{
					Required: true,
					Error:    "workspace setup required",
				},
			},
		},
	}
	svc, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester"},
		Engine:   &recordingEngine{},
		Settings: manager,
		Sandbox:  rt,
		Resources: appresources.Catalog{
			Diagnostics: []appresources.Diagnostic{{
				Severity: appresources.DiagnosticWarning,
				Kind:     "plugin",
				ID:       "plugin-a",
				Path:     "/plugins/a",
				Message:  "plugin skipped",
				Meta:     map[string]string{"scope": "workspace"},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	panel, err := svc.Settings().Panel(ctx, SettingsPanelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !panel.Configured || panel.Settings.Sandbox.Backend != "windows" || panel.Sandbox.Status.ResolvedBackend != "windows" {
		t.Fatalf("panel runtime/sandbox = %#v/%#v, want configured windows sandbox", panel.Settings, panel.Sandbox.Status)
	}
	if !panelActionEnabled(panel.Actions, "sandbox.repair") || !panelActionEnabled(panel.Sandbox.Actions, "sandbox.reset") {
		t.Fatalf("panel actions = %#v/%#v, want enabled sandbox actions", panel.Actions, panel.Sandbox.Actions)
	}
	reset, _ := findSettingsPanelAction(panel.Actions, "sandbox.reset")
	if !reset.Destructive || !reset.RequiresConfirmation {
		t.Fatalf("reset action = %#v, want guarded destructive action", reset)
	}
	if _, ok := findSettingsPanelSection(panel.Sections, "sandbox"); !ok {
		t.Fatalf("panel sections = %#v, missing sandbox section", panel.Sections)
	}
	if !panelDiagnostic(panel.Diagnostics, "model", "configuration") ||
		!panelDiagnostic(panel.Diagnostics, "sandbox", "setup") ||
		!panelDiagnostic(panel.Diagnostics, "resources", "plugin") {
		t.Fatalf("panel diagnostics = %#v, want model, sandbox, and resource diagnostics", panel.Diagnostics)
	}
	if !slices.Contains(panelDiagnosticActions(panel.Diagnostics, "model", "configuration"), "model.connect") {
		t.Fatalf("model diagnostic actions = %#v, want model.connect", panel.Diagnostics)
	}

	if _, err := svc.Settings().RunPanelAction(ctx, SettingsPanelActionRequest{ActionID: "sandbox.prepare"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Settings().RunPanelAction(ctx, SettingsPanelActionRequest{ActionID: "sandbox.preflight", AllowNonElevatedRepair: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Settings().RunPanelAction(ctx, SettingsPanelActionRequest{ActionID: "sandbox.repair"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Settings().RunPanelAction(ctx, SettingsPanelActionRequest{ActionID: "sandbox.reset"}); err != nil {
		t.Fatal(err)
	}
	if rt.prepareCalls != 1 || !rt.preflightAllow || rt.repairCalls != 1 || rt.resetCalls != 1 {
		t.Fatalf("panel action calls prepare=%d preflight=%t repair=%d reset=%d, want all invoked", rt.prepareCalls, rt.preflightAllow, rt.repairCalls, rt.resetCalls)
	}
	if _, err := svc.Settings().RunPanelAction(ctx, SettingsPanelActionRequest{ActionID: "model.connect"}); err == nil {
		t.Fatal("RunPanelAction(model.connect) error = nil, want surface navigation action rejected")
	}
}

func TestSettingsServiceRollsBackRuntimeWhenApplyFails(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{
		Runtime: config.Runtime{
			AppName: "caelis",
			UserID:  "tester",
			Sandbox: config.Sandbox{
				Backend: "host",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("sandbox rebuild failed")
	svc, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester", Sandbox: config.Sandbox{Backend: "host"}},
		Engine:   &recordingEngine{},
		Settings: manager,
		ApplyRuntime: func(context.Context, config.Runtime) (config.Runtime, error) {
			return config.Runtime{}, wantErr
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Settings().SetSandbox(ctx, config.Sandbox{Backend: "capture"}); !errors.Is(err, wantErr) {
		t.Fatalf("SetSandbox() error = %v, want apply failure", err)
	}
	doc, err := manager.Document(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Runtime.Sandbox.Backend != "host" || svc.Runtime().Sandbox.Backend != "host" {
		t.Fatalf("runtime after failed apply = doc:%#v service:%#v, want rollback to host", doc.Runtime.Sandbox, svc.Runtime().Sandbox)
	}
}

func TestCommandServiceAvailableProjectsCoreCommands(t *testing.T) {
	svc, err := New(Config{
		Runtime: config.Runtime{AppName: "caelis", UserID: "tester"},
		Engine:  &recordingEngine{},
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := svc.Commands().Available(context.Background(), CommandCatalogRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Commands) != 11 {
		t.Fatalf("commands = %#v, want eleven core commands", view.Commands)
	}
	agent, ok := findCommandView(view.Commands, "agent")
	if !ok || agent.InputHint != "list|use|add|install|update|remove" {
		t.Fatalf("agent command = %#v ok=%v, want management hint", agent, ok)
	}
	compact, ok := findCommandView(view.Commands, "compact")
	if !ok || compact.InputHint != "" {
		t.Fatalf("compact command = %#v ok=%v, want no input hint", compact, ok)
	}
	task, ok := findCommandView(view.Commands, "task")
	if !ok || task.InputHint != "list|tail|wait|write|cancel|release|start" {
		t.Fatalf("task command = %#v ok=%v, want task management hint", task, ok)
	}
	doctor, ok := findCommandView(view.Commands, "doctor")
	if !ok || doctor.InputHint != "[fix]" {
		t.Fatalf("doctor command = %#v ok=%v, want doctor hint", doctor, ok)
	}
	if _, ok := findCommandView(view.Commands, "new"); !ok {
		t.Fatalf("new command missing from %#v", view.Commands)
	}
	settings, ok := findCommandView(view.Commands, "settings")
	if !ok || settings.InputHint != "[set <field-id> <value>|run <action-id> [confirm]]" {
		t.Fatalf("settings command = %#v ok=%v, want settings panel hint", settings, ok)
	}
}

func TestCommandServiceAvailableProjectsRegisteredAgentCommands(t *testing.T) {
	svc, err := New(Config{
		Runtime: config.Runtime{AppName: "caelis", UserID: "tester"},
		Engine:  &recordingEngine{},
		Agents: []AgentDescriptor{{
			ID:          "reviewer",
			Name:        "reviewer",
			Kind:        AgentKindExternalACP,
			Command:     "reviewer-acp",
			Description: "Review code through ACP",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := svc.Commands().Available(context.Background(), CommandCatalogRequest{})
	if err != nil {
		t.Fatal(err)
	}
	reviewer, ok := findCommandView(view.Commands, "reviewer")
	if !ok || reviewer.InputHint != "prompt" || reviewer.Description != "Review code through ACP" {
		t.Fatalf("reviewer command = %#v ok=%v, want dynamic ACP agent command", reviewer, ok)
	}
}

func TestCommandServiceExecuteStatus(t *testing.T) {
	ctx := context.Background()
	engine := &recordingEngine{snapshot: session.Snapshot{
		Session: session.Session{
			Ref:   session.Ref{SessionID: "sess-status"},
			Title: "work",
		},
		State: session.State{
			StateSessionMode: coreruntime.SessionModeManual,
		},
	}}
	svc, err := New(Config{
		Runtime: config.Runtime{
			AppName: "caelis",
			UserID:  "tester",
			Store: config.Store{
				Backend: "jsonl",
				URI:     "/tmp/events.jsonl",
			},
			Sandbox: config.Sandbox{Backend: "host"},
		},
		Engine: engine,
	})
	if err != nil {
		t.Fatal(err)
	}

	view, err := svc.Commands().Execute(ctx, CommandExecutionRequest{
		SessionRef: session.Ref{SessionID: "sess-status"},
		Input:      " /status ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !view.Handled || view.Command != "status" {
		t.Fatalf("status execution = %#v, want handled status", view)
	}
	for _, want := range []string{
		"status:",
		"session: sess-status",
		"title: work",
		"model: not configured",
		"mode: manual",
		"store: jsonl /tmp/events.jsonl",
		"sandbox: host",
	} {
		if !strings.Contains(view.Output, want) {
			t.Fatalf("status output = %q, missing %q", view.Output, want)
		}
	}

	unhandled, err := svc.Commands().Execute(ctx, CommandExecutionRequest{
		SessionRef: session.Ref{SessionID: "sess-status"},
		Input:      "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if unhandled.Handled {
		t.Fatalf("non-slash execution = %#v, want unhandled", unhandled)
	}
}

func TestCommandServiceExecuteNewStartsSession(t *testing.T) {
	ctx := context.Background()
	engine := &recordingEngine{}
	svc, err := New(Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "tester",
			WorkspaceKey: "repo",
			WorkspaceCWD: "/tmp/repo",
		},
		Engine: engine,
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := svc.Commands().Execute(ctx, CommandExecutionRequest{Input: "/new"})
	if err != nil {
		t.Fatal(err)
	}
	if !view.Handled || view.Command != "new" || view.SessionRef == nil || view.SessionRef.SessionID != "sess-1" {
		t.Fatalf("new execution = %#v, want started session ref", view)
	}
	if engine.start.AppName != "caelis" || engine.start.UserID != "tester" || engine.start.Workspace.Key != "repo" || engine.start.Workspace.CWD != "/tmp/repo" {
		t.Fatalf("start request = %#v, want runtime identity/workspace", engine.start)
	}
	if !strings.Contains(view.Output, "new session: sess-1") {
		t.Fatalf("new output = %q, want session id", view.Output)
	}
}

func TestCommandServiceExecuteSettingsPanelAndAction(t *testing.T) {
	ctx := context.Background()
	rt := &recordingLifecycleSandboxRuntime{
		fakeSandboxRuntime: fakeSandboxRuntime{
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
	}
	svc, err := New(Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "tester",
			WorkspaceKey: "repo",
			WorkspaceCWD: "/repo",
			Store:        config.Store{Backend: "sqlite", URI: "/tmp/caelis.sqlite"},
			Sandbox:      config.Sandbox{Backend: "host"},
		},
		Engine:  &recordingEngine{},
		Sandbox: rt,
		Resources: appresources.Catalog{
			Diagnostics: []appresources.Diagnostic{{
				Severity: appresources.DiagnosticWarning,
				Kind:     "plugin",
				ID:       "plugin-a",
				Message:  "plugin skipped",
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := svc.Commands().Execute(ctx, CommandExecutionRequest{Input: "/settings"})
	if err != nil {
		t.Fatal(err)
	}
	if !view.Handled || view.Command != "settings" {
		t.Fatalf("settings execution = %#v, want handled settings", view)
	}
	for _, want := range []string{
		"settings:",
		"configured: false",
		"workspace: /repo",
		"store: sqlite /tmp/caelis.sqlite",
		"model: not configured",
		"[warning] model/configuration: no model is configured",
		"[warning] resources/plugin: plugin skipped",
		"model.connect - Connect model",
		"sandbox.prepare - Prepare",
		"sections:",
	} {
		if !strings.Contains(view.Output, want) {
			t.Fatalf("settings output = %q, missing %q", view.Output, want)
		}
	}
	if _, err := svc.Commands().Execute(ctx, CommandExecutionRequest{Input: "/settings run sandbox.reset"}); err == nil {
		t.Fatal("settings reset without confirmation error = nil, want confirmation error")
	}
	ran, err := svc.Commands().Execute(ctx, CommandExecutionRequest{Input: "/settings run sandbox.prepare"})
	if err != nil {
		t.Fatal(err)
	}
	if rt.prepareCalls != 1 || !strings.Contains(ran.Output, "settings action completed: sandbox.prepare") {
		t.Fatalf("settings action output=%q prepareCalls=%d, want prepared panel", ran.Output, rt.prepareCalls)
	}
	connectPanel, err := svc.Commands().Execute(ctx, CommandExecutionRequest{Input: "/settings run model.connect"})
	if err != nil {
		t.Fatal(err)
	}
	if connectPanel.Command != "connect" || !strings.Contains(connectPanel.Output, "connect:") || !strings.Contains(connectPanel.Output, "providers:") {
		t.Fatalf("settings model.connect output = %#v, want shared connect panel", connectPanel)
	}
}

func TestCommandServiceExecuteSettingsSetField(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, appsettings.NewFileStore(t.TempDir()), appsettings.Document{
		Runtime: config.Runtime{
			AppName: "caelis",
			UserID:  "tester",
			Sandbox: config.Sandbox{
				Backend: "host",
				Network: "inherit",
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

	view, err := svc.Commands().Execute(ctx, CommandExecutionRequest{Input: "/settings set sandbox.network disabled"})
	if err != nil {
		t.Fatal(err)
	}
	if !view.Handled || view.Command != "settings" || !strings.Contains(view.Output, "settings field updated: sandbox.network") {
		t.Fatalf("settings set output = %#v, want handled update", view)
	}
	view, err = svc.Commands().Execute(ctx, CommandExecutionRequest{Input: "/settings set compaction.max_source_chars 1234"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(view.Output, "settings field updated: compaction.max_source_chars") {
		t.Fatalf("settings set compaction output = %q, want updated field", view.Output)
	}
	doc, err := manager.Document(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Runtime.Sandbox.Network != "disabled" || doc.Compaction.MaxSourceChars != 1234 {
		t.Fatalf("settings document = %#v, want disabled network and max source chars", doc)
	}
}

func TestCommandServiceExecuteDoctorAndFix(t *testing.T) {
	ctx := context.Background()
	engine := &recordingEngine{snapshot: session.Snapshot{
		Session: session.Session{Ref: session.Ref{SessionID: "sess-doctor"}},
	}}
	rt := &recordingLifecycleSandboxRuntime{
		fakeSandboxRuntime: fakeSandboxRuntime{
			descriptor: sandbox.Descriptor{
				Backend: sandbox.BackendWindows,
				DefaultConstraints: sandbox.Constraints{
					Route:   sandbox.RouteSandbox,
					Backend: sandbox.BackendWindows,
				},
			},
			status: sandbox.Status{
				RequestedBackend: sandbox.BackendWindows,
				ResolvedBackend:  sandbox.BackendWindows,
				Setup: sandbox.SetupStatus{
					Required: true,
					Error:    "workspace setup required",
				},
			},
		},
	}
	svc, err := New(Config{
		Runtime: config.Runtime{
			AppName: "caelis",
			UserID:  "tester",
			Store: config.Store{
				Backend: "sqlite",
				URI:     "/tmp/caelis.db",
			},
			Sandbox: config.Sandbox{Backend: "windows"},
		},
		Engine:  engine,
		Sandbox: rt,
	})
	if err != nil {
		t.Fatal(err)
	}

	report, err := svc.Commands().Execute(ctx, CommandExecutionRequest{
		SessionRef: session.Ref{SessionID: "sess-doctor"},
		Input:      "/doctor",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"doctor:", "warn model not configured", "ok session store: sqlite /tmp/caelis.db", "ok session: sess-doctor", "warn sandbox setup: workspace setup required"} {
		if !strings.Contains(report.Output, want) {
			t.Fatalf("doctor output = %q, missing %q", report.Output, want)
		}
	}

	fixed, err := svc.Commands().Execute(ctx, CommandExecutionRequest{Input: "/doctor fix"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sandbox repair complete", "backend: windows", "supported: true", "attempted: true", "doctor:"} {
		if !strings.Contains(fixed.Output, want) {
			t.Fatalf("doctor fix output = %q, missing %q", fixed.Output, want)
		}
	}
	if rt.repairCalls != 1 {
		t.Fatalf("doctor fix output = %q repairCalls=%d, want repair and report", fixed.Output, rt.repairCalls)
	}
}

func TestCommandServiceExecuteCompactRecordsCheckpoint(t *testing.T) {
	ctx := context.Background()
	engine := &recordingEngine{snapshot: session.Snapshot{
		Session: session.Session{Ref: session.Ref{SessionID: "sess-compact"}},
		Events: []session.Event{{
			ID:   "event-1",
			Type: session.EventUser,
			Message: &model.Message{
				Role:  model.RoleUser,
				Parts: []model.Part{model.NewTextPart("important state")},
			},
		}},
	}}
	svc, err := New(Config{
		Runtime: config.Runtime{AppName: "caelis", UserID: "tester"},
		Engine:  engine,
	})
	if err != nil {
		t.Fatal(err)
	}

	view, err := svc.Commands().Execute(ctx, CommandExecutionRequest{
		SessionRef: session.Ref{SessionID: "sess-compact"},
		Input:      "/compact",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !view.Handled || view.Command != "compact" || view.Output != "compaction completed" {
		t.Fatalf("compact execution = %#v, want handled completion", view)
	}
	if len(engine.events) != 1 {
		t.Fatalf("recorded events = %#v, want one compact event", engine.events)
	}
	event := engine.events[0]
	if event.Type != session.EventCompact {
		t.Fatalf("recorded event type = %q, want compact", event.Type)
	}
	text := session.EventText(event)
	if !strings.Contains(text, "CONTEXT CHECKPOINT") || !strings.Contains(text, "important state") {
		t.Fatalf("compact checkpoint = %q, want source summary", text)
	}
}

func TestCommandServiceExecuteTaskCommands(t *testing.T) {
	ctx := context.Background()
	taskSession := &recordingTaskSession{
		snapshot: sandbox.SessionSnapshot{
			Ref:           sandbox.SessionRef{ID: "task-1", Backend: "host"},
			State:         sandbox.SessionRunning,
			Running:       true,
			SupportsInput: true,
			Command:       "cat",
			Metadata: map[string]any{
				"title": "Echo Task",
			},
		},
		stdout: "ready\n",
	}
	svc, err := New(Config{
		Runtime: config.Runtime{AppName: "caelis", UserID: "tester"},
		Engine:  &recordingEngine{},
		Sandbox: &recordingTaskRuntime{sessions: map[string]*recordingTaskSession{"task-1": taskSession}},
	})
	if err != nil {
		t.Fatal(err)
	}

	listed, err := svc.Commands().Execute(ctx, CommandExecutionRequest{Input: "/task list"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"tasks:", "task-1", "running", "Echo Task"} {
		if !strings.Contains(listed.Output, want) {
			t.Fatalf("task list output = %q, missing %q", listed.Output, want)
		}
	}

	wrote, err := svc.Commands().Execute(ctx, CommandExecutionRequest{Input: "/task write task-1 -- ping"})
	if err != nil {
		t.Fatal(err)
	}
	if taskSession.wrote != "ping" || !strings.Contains(wrote.Output, "ready") {
		t.Fatalf("task write = %#v wrote=%q, want shared task service output", wrote, taskSession.wrote)
	}

	cancelled, err := svc.Commands().Execute(ctx, CommandExecutionRequest{Input: "/task cancel task-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cancelled.Output, "cancelled") {
		t.Fatalf("task cancel output = %q, want cancelled state", cancelled.Output)
	}
}

func TestCommandServiceExecuteConnectConfiguresAndUsesModel(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	engine := &recordingEngine{snapshot: session.Snapshot{
		Session: session.Session{Ref: session.Ref{SessionID: "sess-connect"}},
	}}
	svc, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester"},
		Engine:   engine,
		Settings: manager,
	})
	if err != nil {
		t.Fatal(err)
	}

	view, err := svc.Commands().Execute(ctx, CommandExecutionRequest{
		SessionRef: session.Ref{SessionID: "sess-connect"},
		Input:      "/connect openai-compatible gpt-connect https://api.example.test/v1 45 env:CONNECT_KEY 131072 4096 low,high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !view.Handled || view.Command != "connect" {
		t.Fatalf("connect execution = %#v, want handled connect", view)
	}
	for _, want := range []string{
		"connected: openai-compatible/gpt-connect",
		"base_url: https://api.example.test/v1",
		"context_window_tokens: 131072",
		"max_output_tokens: 4096",
		"reasoning_levels: low,high",
	} {
		if !strings.Contains(view.Output, want) {
			t.Fatalf("connect output = %q, missing %q", view.Output, want)
		}
	}
	cfg, err := manager.ResolveModel("openai-compatible/gpt-connect")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TokenEnv != "CONNECT_KEY" || cfg.Token != "" || cfg.Timeout != 45*time.Second {
		t.Fatalf("connected cfg auth/timeout = %#v, want env token and 45s", cfg)
	}
	if cfg.ContextWindowTokens != 131072 || cfg.MaxOutputTokens != 4096 || len(cfg.ReasoningLevels) != 2 {
		t.Fatalf("connected cfg limits = %#v, want parsed limits", cfg)
	}
	if engine.state[StateCurrentModelID] != cfg.ID {
		t.Fatalf("state after connect = %#v, want current model %q", engine.state, cfg.ID)
	}
}

func TestCommandServiceExecuteConnectWithoutArgsRendersPanel(t *testing.T) {
	ctx := context.Background()
	svc, err := New(Config{
		Runtime: config.Runtime{AppName: "caelis", UserID: "tester"},
		Engine:  &recordingEngine{},
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := svc.Commands().Execute(ctx, CommandExecutionRequest{Input: "/connect"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"connect:",
		"current: not configured",
		"providers:",
		"openai-compatible",
		"wizard: /connect",
		"[warning] model_configuration: no model is configured",
	} {
		if !strings.Contains(view.Output, want) {
			t.Fatalf("connect panel output = %q, missing %q", view.Output, want)
		}
	}
}

func TestCommandServiceExecuteConnectUsesPreparedCodeFreeConfig(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	auth := &recordingCodeFreeAuth{
		ensureResult: CodeFreeAuthResult{CredentialPath: "/tmp/codefree.json", UserID: "user-1", HasRefreshToken: true},
	}
	engine := &recordingEngine{snapshot: session.Snapshot{
		Session: session.Session{Ref: session.Ref{SessionID: "sess-connect"}},
	}}
	svc, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester"},
		Engine:   engine,
		Settings: manager,
		CodeFree: auth,
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := svc.Commands().Execute(ctx, CommandExecutionRequest{
		SessionRef: session.Ref{SessionID: "sess-connect"},
		Input:      "/connect codefree GLM-4.7",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(view.Output, "connected: codefree/glm-4.7") || !strings.Contains(view.Output, "base_url: https://www.srdcloud.cn") {
		t.Fatalf("connect output = %q, want prepared CodeFree config", view.Output)
	}
	if !auth.ensureReq.OpenBrowser || auth.ensureReq.BaseURL != "https://www.srdcloud.cn" {
		t.Fatalf("codefree auth req = %#v, want browser auth at default base URL", auth.ensureReq)
	}
	cfg, err := manager.ResolveModel("codefree/glm-4.7")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthType != string(model.AuthNone) || cfg.BaseURL != "https://www.srdcloud.cn" {
		t.Fatalf("codefree cfg = %#v, want no-auth prepared config", cfg)
	}
	if engine.state[StateCurrentModelID] != cfg.ID {
		t.Fatalf("state after codefree connect = %#v, want current model %q", engine.state, cfg.ID)
	}
}

func TestCommandServiceExecuteAgentManagementAndHandoff(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	engine := &recordingEngine{snapshot: session.Snapshot{
		Session: session.Session{Ref: session.Ref{AppName: "caelis", UserID: "tester", SessionID: "sess-agent", WorkspaceKey: "repo"}},
	}}
	svc, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester", WorkspaceKey: "repo"},
		Engine:   engine,
		Settings: manager,
		Agents: []AgentDescriptor{{
			ID:      "reviewer",
			Name:    "reviewer",
			Kind:    AgentKindExternalACP,
			Command: "reviewer-acp",
		}},
		BuiltinAgents: []AgentDescriptor{{
			ID:          "copilot",
			Name:        "copilot",
			Kind:        AgentKindExternalACP,
			Command:     "copilot",
			Args:        []string{"--acp", "--stdio"},
			Description: "GitHub Copilot ACP agent",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	listed, err := svc.Commands().Execute(ctx, CommandExecutionRequest{
		SessionRef: session.Ref{SessionID: "sess-agent"},
		Input:      "/agent list",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"agents:", "controller: local", "reviewer", "copilot"} {
		if !strings.Contains(listed.Output, want) {
			t.Fatalf("agent list output = %q, missing %q", listed.Output, want)
		}
	}

	added, err := svc.Commands().Execute(ctx, CommandExecutionRequest{Input: "/agent add copilot"})
	if err != nil {
		t.Fatal(err)
	}
	if added.Output != "agent registered: copilot" {
		t.Fatalf("agent add output = %q, want registered copilot", added.Output)
	}
	if agents := manager.ListACPAgents(); len(agents) != 1 || agents[0].Name != "copilot" {
		t.Fatalf("settings agents after builtin add = %#v, want copilot", agents)
	}

	custom, err := svc.Commands().Execute(ctx, CommandExecutionRequest{Input: "/agent add custom helper -- helper-acp --stdio"})
	if err != nil {
		t.Fatal(err)
	}
	if custom.Output != "agent registered: helper" {
		t.Fatalf("custom add output = %q, want helper", custom.Output)
	}
	handoff, err := svc.Commands().Execute(ctx, CommandExecutionRequest{
		SessionRef: session.Ref{SessionID: "sess-agent"},
		Input:      "/agent use reviewer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if handoff.Output != "agent controller: reviewer" {
		t.Fatalf("handoff output = %q, want reviewer", handoff.Output)
	}
	if len(handoff.Events) != 1 || handoff.Events[0].Type != session.EventHandoff || handoff.Events[0].SessionID != "sess-agent" || handoff.Events[0].Scope == nil || handoff.Events[0].Scope.Controller.ID != "reviewer" {
		t.Fatalf("handoff command events = %#v, want reviewer handoff projection", handoff.Events)
	}
	if len(engine.events) != 1 || engine.events[0].Type != session.EventHandoff || engine.events[0].Scope == nil || engine.events[0].Scope.Controller.ID != "reviewer" {
		t.Fatalf("handoff events = %#v, want reviewer handoff event", engine.events)
	}
	local, err := svc.Commands().Execute(ctx, CommandExecutionRequest{
		SessionRef: session.Ref{SessionID: "sess-agent"},
		Input:      "/agent use local",
	})
	if err != nil {
		t.Fatal(err)
	}
	if local.Output != "agent controller: local" || engine.events[0].Scope.Controller.Kind != session.ControllerBuiltin {
		t.Fatalf("local handoff = %#v events=%#v, want local controller", local, engine.events)
	}
	if len(local.Events) != 1 || local.Events[0].Type != session.EventHandoff || local.Events[0].Scope == nil || local.Events[0].Scope.Controller.Kind != session.ControllerBuiltin {
		t.Fatalf("local command events = %#v, want local handoff projection", local.Events)
	}
	removed, err := svc.Commands().Execute(ctx, CommandExecutionRequest{Input: "/agent remove helper"})
	if err != nil {
		t.Fatal(err)
	}
	if removed.Output != "agent removed: helper" {
		t.Fatalf("remove output = %q, want removed helper", removed.Output)
	}
	if agents := manager.ListACPAgents(); len(agents) != 1 || agents[0].Name != "copilot" {
		t.Fatalf("settings agents after remove = %#v, want only copilot", agents)
	}
}

func TestCommandServiceExecuteDynamicAgentPrompt(t *testing.T) {
	ctx := context.Background()
	engine := &recordingEngine{snapshot: session.Snapshot{
		Session: session.Session{Ref: session.Ref{AppName: "caelis", UserID: "tester", SessionID: "sess-agent", WorkspaceKey: "repo"}},
	}}
	var invokeReq AgentInvokeRequest
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
				invokeReq = req
				return AgentInvokeResult{
					Events: []session.Event{{
						Type: session.EventAssistant,
						Message: &model.Message{
							Role:  model.RoleAssistant,
							Parts: []model.Part{model.NewTextPart("reviewed: " + req.Input)},
						},
					}},
				}, nil
			}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := svc.Commands().Execute(ctx, CommandExecutionRequest{
		SessionRef: session.Ref{SessionID: "sess-agent"},
		Input:      "/reviewer inspect repo",
		ContentParts: []model.ContentPart{{
			Type: model.ContentPartText,
			Text: "/reviewer inspect repo",
		}, {
			Type:     model.ContentPartImage,
			MimeType: "image/png",
			Data:     "aW1hZ2U=",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !view.Handled || view.Command != "reviewer" {
		t.Fatalf("dynamic command = %#v, want handled reviewer", view)
	}
	if invokeReq.AgentID != "reviewer" || invokeReq.Input != "inspect repo" {
		t.Fatalf("invoke request = %#v, want reviewer prompt without slash prefix", invokeReq)
	}
	if invokeReq.Participant.Kind != session.ParticipantACP || invokeReq.Participant.Role != session.ParticipantSidecar || invokeReq.Participant.ID != "reviewer" {
		t.Fatalf("invoke participant = %#v, want reviewer ACP sidecar", invokeReq.Participant)
	}
	if len(invokeReq.ContentParts) != 2 || invokeReq.ContentParts[0].Text != "inspect repo" || invokeReq.ContentParts[1].Type != model.ContentPartImage {
		t.Fatalf("invoke content parts = %#v, want prompt text plus image", invokeReq.ContentParts)
	}
	if len(view.Events) != 3 {
		t.Fatalf("dynamic events = %#v, want attach, user, assistant", view.Events)
	}
	if view.Events[0].Type != session.EventParticipant || view.Events[1].Type != session.EventUser || view.Events[2].Type != session.EventAssistant {
		t.Fatalf("dynamic event types = %#v, want participant/user/assistant", view.Events)
	}
	if session.EventText(view.Events[1]) != "inspect repo" || session.EventText(view.Events[2]) != "reviewed: inspect repo" {
		t.Fatalf("dynamic event text = %q / %q, want prompt and response", session.EventText(view.Events[1]), session.EventText(view.Events[2]))
	}
	if len(engine.eventBatches) != 1 || len(engine.eventBatches[0]) != 3 {
		t.Fatalf("recorded event batches = %#v, want atomic attach/user/assistant batch", engine.eventBatches)
	}
}

func TestCommandServiceDynamicAgentPromptDoesNotPersistPrefaceOnInvokeFailure(t *testing.T) {
	ctx := context.Background()
	engine := &recordingEngine{
		snapshot: session.Snapshot{
			Session: session.Session{Ref: session.Ref{SessionID: "sess-agent"}},
		},
	}
	svc, err := New(Config{
		Runtime: config.Runtime{AppName: "caelis", UserID: "tester"},
		Engine:  engine,
		Agents: []AgentDescriptor{{
			ID:      "reviewer",
			Name:    "reviewer",
			Kind:    AgentKindExternalACP,
			Command: "reviewer-acp",
		}},
		Invokers: map[string]AgentInvoker{
			"reviewer": AgentInvokerFunc(func(context.Context, AgentInvokeRequest) (AgentInvokeResult, error) {
				return AgentInvokeResult{}, errors.New("active participant run already in progress")
			}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Commands().Execute(ctx, CommandExecutionRequest{
		SessionRef: session.Ref{SessionID: "sess-agent"},
		Input:      "/reviewer inspect repo",
	})
	if err == nil || !strings.Contains(err.Error(), "active participant run already in progress") {
		t.Fatalf("dynamic command error = %v, want invoke failure", err)
	}
	if len(engine.events) != 0 || len(engine.eventBatches) != 0 {
		t.Fatalf("recorded events = %#v batches=%#v, want no partial participant preface", engine.events, engine.eventBatches)
	}
}

func TestCommandServiceExecuteModelAndApproval(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	alpha, err := manager.UpsertModel(ctx, appsettings.ModelConfig{
		Alias:           "alpha",
		Provider:        "openai-compatible",
		Model:           "gpt-alpha",
		ReasoningMode:   "fixed",
		ReasoningLevels: []string{"low", "high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	beta, err := manager.UpsertModel(ctx, appsettings.ModelConfig{
		Alias:           "beta",
		Provider:        "openai-compatible",
		Model:           "gpt-beta",
		ReasoningMode:   "fixed",
		ReasoningLevels: []string{"low", "high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetDefaultModel(ctx, alpha.ID); err != nil {
		t.Fatal(err)
	}
	engine := &recordingEngine{snapshot: session.Snapshot{
		Session: session.Session{Ref: session.Ref{SessionID: "sess-command"}},
	}}
	svc, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester"},
		Engine:   engine,
		Settings: manager,
	})
	if err != nil {
		t.Fatal(err)
	}

	listed, err := svc.Commands().Execute(ctx, CommandExecutionRequest{
		SessionRef: session.Ref{SessionID: "sess-command"},
		Input:      "/model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !listed.Handled || listed.Command != "model" {
		t.Fatalf("model list = %#v, want handled model", listed)
	}
	for _, want := range []string{"models:", "alpha", "default", "current", "beta"} {
		if !strings.Contains(listed.Output, want) {
			t.Fatalf("model list output = %q, missing %q", listed.Output, want)
		}
	}

	switched, err := svc.Commands().Execute(ctx, CommandExecutionRequest{
		SessionRef: session.Ref{SessionID: "sess-command"},
		Input:      "/model use beta high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(switched.Output, "model switched to: beta") || !strings.Contains(switched.Output, "reasoning: high") {
		t.Fatalf("model switch output = %q, want beta/high", switched.Output)
	}
	if engine.state[StateCurrentModelID] != beta.ID || engine.state[StateCurrentReasoningEffort] != "high" {
		t.Fatalf("state after model use = %#v, want beta/high", engine.state)
	}

	currentMode, err := svc.Commands().Execute(ctx, CommandExecutionRequest{
		SessionRef: session.Ref{SessionID: "sess-command"},
		Input:      "/approval",
	})
	if err != nil {
		t.Fatal(err)
	}
	if currentMode.Output != "approval mode: auto-review" {
		t.Fatalf("approval output = %q, want auto-review", currentMode.Output)
	}
	manual, err := svc.Commands().Execute(ctx, CommandExecutionRequest{
		SessionRef: session.Ref{SessionID: "sess-command"},
		Input:      "/approval manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	if manual.Output != "approval mode: manual" || engine.state[StateSessionMode] != coreruntime.SessionModeManual {
		t.Fatalf("approval manual = %#v state=%#v, want manual", manual, engine.state)
	}
	toggled, err := svc.Commands().Execute(ctx, CommandExecutionRequest{
		SessionRef: session.Ref{SessionID: "sess-command"},
		Input:      "/approval toggle",
	})
	if err != nil {
		t.Fatal(err)
	}
	if toggled.Output != "approval mode: auto-review" || engine.state[StateSessionMode] != coreruntime.SessionModeAutoReview {
		t.Fatalf("approval toggle = %#v state=%#v, want auto-review", toggled, engine.state)
	}

	deleted, err := svc.Commands().Execute(ctx, CommandExecutionRequest{
		SessionRef: session.Ref{SessionID: "sess-command"},
		Input:      "/model del alpha",
	})
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Output != "model deleted: alpha" {
		t.Fatalf("delete output = %q, want deleted alpha", deleted.Output)
	}
	if _, err := manager.ResolveModel("alpha"); err == nil {
		t.Fatal("ResolveModel(alpha) error = nil, want deleted model")
	}
}

func TestCommandServiceModelDeleteClearsCurrentSessionModel(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	beta, err := manager.UpsertModel(ctx, appsettings.ModelConfig{
		Alias:    "beta",
		Provider: "openai-compatible",
		Model:    "gpt-beta",
	})
	if err != nil {
		t.Fatal(err)
	}
	state := session.State{StateCurrentModelID: beta.ID, StateCurrentReasoningEffort: "high"}
	engine := &recordingEngine{
		state: state,
		snapshot: session.Snapshot{
			Session: session.Session{Ref: session.Ref{SessionID: "sess-command"}},
			State:   state,
		},
	}
	svc, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester"},
		Engine:   engine,
		Settings: manager,
	})
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := svc.Commands().Execute(ctx, CommandExecutionRequest{
		SessionRef: session.Ref{SessionID: "sess-command"},
		Input:      "/model del beta",
	})
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Output != "model deleted: beta" {
		t.Fatalf("delete output = %q, want deleted beta", deleted.Output)
	}
	if _, ok := engine.state[StateCurrentModelID]; ok {
		t.Fatalf("session model state after delete = %#v, want current model cleared", engine.state)
	}
	if _, ok := engine.state[StateCurrentReasoningEffort]; ok {
		t.Fatalf("session reasoning state after delete = %#v, want reasoning cleared", engine.state)
	}
}

func TestCommandServiceModelAndApprovalUseACPControllerState(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpsertModel(ctx, appsettings.ModelConfig{
		Alias:           "remote-model",
		Provider:        "openai-compatible",
		Model:           "gpt-remote",
		ReasoningMode:   "fixed",
		ReasoningLevels: []string{"low", "high"},
	}); err != nil {
		t.Fatal(err)
	}
	controller := session.ControllerBinding{
		Kind:      session.ControllerACP,
		ID:        "reviewer",
		AgentName: "reviewer",
		EpochID:   "controller-1",
	}
	engine := &recordingEngine{
		state: session.State{},
		snapshot: session.Snapshot{
			Session: session.Session{
				Ref:        session.Ref{SessionID: "sess-controller"},
				Controller: controller,
			},
			State: session.State{},
		},
	}
	svc, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester"},
		Engine:   engine,
		Settings: manager,
	})
	if err != nil {
		t.Fatal(err)
	}
	switched, err := svc.Commands().Execute(ctx, CommandExecutionRequest{
		SessionRef: session.Ref{SessionID: "sess-controller"},
		Input:      "/model use remote-model high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if switched.Output != "model switched to: remote-model (reasoning: high)" {
		t.Fatalf("model switch output = %q, want controller model switch", switched.Output)
	}
	if engine.state[StateControllerModel] != "remote-model" || engine.state[StateControllerReasoning] != "high" {
		t.Fatalf("controller model state = %#v, want remote-model/high", engine.state)
	}
	manual, err := svc.Commands().Execute(ctx, CommandExecutionRequest{
		SessionRef: session.Ref{SessionID: "sess-controller"},
		Input:      "/approval manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	if manual.Output != "approval mode: manual" || engine.state[StateControllerMode] != "manual" {
		t.Fatalf("controller approval output/state = %#v state=%#v, want manual", manual, engine.state)
	}
	if _, ok := engine.state[StateSessionMode]; ok {
		t.Fatalf("local session mode state = %#v, want unchanged under ACP controller", engine.state)
	}
	toggled, err := svc.Commands().Execute(ctx, CommandExecutionRequest{
		SessionRef: session.Ref{SessionID: "sess-controller"},
		Input:      "/approval toggle",
	})
	if err != nil {
		t.Fatal(err)
	}
	if toggled.Output != "approval mode: auto-review" || engine.state[StateControllerMode] != "auto-review" {
		t.Fatalf("controller approval toggle = %#v state=%#v, want auto-review", toggled, engine.state)
	}
	if _, err := svc.Commands().Execute(ctx, CommandExecutionRequest{
		SessionRef: session.Ref{SessionID: "sess-controller"},
		Input:      "/model del remote-model",
	}); err == nil {
		t.Fatal("/model del under ACP controller error = nil, want usage error")
	}
}

func TestNextControllerModeUsesDeclaredModeOrder(t *testing.T) {
	next, err := nextControllerMode(ControllerStatus{
		Mode: "plan",
		ModeOptions: []ControllerMode{
			{ID: "plan", Name: "Plan"},
			{ID: "code", Name: "Code"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.ID != "code" {
		t.Fatalf("next mode = %#v, want code", next)
	}
	next, err = nextControllerMode(ControllerStatus{
		Mode: "Code",
		ModeOptions: []ControllerMode{
			{ID: "plan", Name: "Plan"},
			{ID: "code", Name: "Code"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.ID != "plan" {
		t.Fatalf("next mode from name = %#v, want plan", next)
	}
}

func TestCommandServiceExecuteResumeListsAndTargetsSession(t *testing.T) {
	ctx := context.Background()
	updated := time.Date(2026, 5, 31, 10, 30, 0, 0, time.UTC)
	engine := &recordingEngine{
		page: session.SessionPage{Sessions: []session.SessionSummary{{
			Session: session.Session{
				Ref:       session.Ref{SessionID: "sess-alpha"},
				Title:     "alpha work",
				UpdatedAt: updated,
			},
		}}},
		snapshot: session.Snapshot{
			Session: session.Session{
				Ref:       session.Ref{SessionID: "sess-alpha"},
				Title:     "alpha work",
				Workspace: session.Workspace{CWD: "/tmp/project"},
			},
			Events: []session.Event{{
				Type: session.EventUser,
				Message: &model.Message{
					Role:  model.RoleUser,
					Parts: []model.Part{model.NewTextPart("resume me")},
				},
			}},
		},
	}
	svc, err := New(Config{
		Runtime: config.Runtime{AppName: "caelis", UserID: "tester"},
		Engine:  engine,
	})
	if err != nil {
		t.Fatal(err)
	}

	listed, err := svc.Commands().Execute(ctx, CommandExecutionRequest{Input: "/resume"})
	if err != nil {
		t.Fatal(err)
	}
	if !listed.Handled || listed.Command != "resume" || !strings.Contains(listed.Output, "sess-alpha") || !strings.Contains(listed.Output, "alpha work") {
		t.Fatalf("resume list = %#v, want listed session", listed)
	}
	target, err := svc.Commands().Execute(ctx, CommandExecutionRequest{Input: "/resume sess-alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if target.SessionRef == nil || target.SessionRef.SessionID != "sess-alpha" {
		t.Fatalf("resume target = %#v, want sess-alpha ref", target)
	}
	if !strings.Contains(target.Output, "resume session: sess-alpha") || !strings.Contains(target.Output, "events: 1") {
		t.Fatalf("resume output = %q, want summary", target.Output)
	}
	if engine.loadRef.SessionID != "sess-alpha" {
		t.Fatalf("load ref = %#v, want sess-alpha", engine.loadRef)
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

func TestAgentServiceManagementViewProjectsActions(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpsertACPAgent(ctx, plugin.ACPAgentDescriptor{
		Name:    "helper",
		Command: "helper-acp",
		Args:    []string{"--stdio"},
	}); err != nil {
		t.Fatal(err)
	}
	svc, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester"},
		Engine:   &recordingEngine{},
		Settings: manager,
		Agents: []AgentDescriptor{{
			ID:          "reviewer",
			Name:        "reviewer",
			Kind:        AgentKindExternalACP,
			Command:     "reviewer-acp",
			Description: "plugin reviewer",
		}},
		BuiltinAgents: []AgentDescriptor{{
			ID:          "codex",
			Name:        "codex",
			Kind:        AgentKindExternalACP,
			Description: "OpenAI Codex ACP agent",
			Command:     "npx",
			Args:        []string{"-y", "@zed-industries/codex-acp"},
		}, {
			ID:          "helper",
			Name:        "helper",
			Kind:        AgentKindExternalACP,
			Description: "Helper ACP agent",
			Command:     "helper-acp",
		}},
		AgentInstaller: &recordingAgentInstaller{},
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := svc.Agents().Management(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !view.CanRegisterCustom || len(view.Registered) != 2 || len(view.Builtins) != 2 || len(view.Installable) != 2 {
		t.Fatalf("management view counts = %#v, want custom registration, two registered/builtin/installable entries", view)
	}
	helper, ok := findAgentManagementItem(view.Registered, "helper")
	if !ok || !helper.Registered || !agentActionEnabled(helper.Actions, agentActionInvoke) || !agentActionEnabled(helper.Actions, agentActionRemove) {
		t.Fatalf("registered helper = %#v ok=%v, want invoke/remove actions", helper, ok)
	}
	codex, ok := findAgentManagementItem(view.Builtins, "codex")
	if !ok || !codex.Builtin || codex.Registered || !codex.Installable || !agentActionEnabled(codex.Actions, agentActionRegister) || !agentActionEnabled(codex.Actions, agentActionInstall) {
		t.Fatalf("builtin codex = %#v ok=%v, want register/install actions", codex, ok)
	}
	builtinHelper, ok := findAgentManagementItem(view.Builtins, "helper")
	if !ok || !builtinHelper.Registered || agentActionEnabled(builtinHelper.Actions, agentActionRegister) || !agentActionEnabled(builtinHelper.Actions, agentActionUpdate) {
		t.Fatalf("builtin helper = %#v ok=%v, want registered helper with update action", builtinHelper, ok)
	}
	view.Registered[0].Agent.Name = "mutated"
	again, err := svc.Agents().Management(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again.Registered[0].Agent.Name == "mutated" {
		t.Fatalf("management view was not cloned: %#v", again.Registered[0])
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

func TestAgentServicePersistsRemoteControllerConfigOptions(t *testing.T) {
	ctx := context.Background()
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
		Runtime: config.Runtime{AppName: "caelis", UserID: "tester", WorkspaceKey: "repo"},
		Engine:  engine,
		Invokers: map[string]AgentInvoker{
			"reviewer": AgentInvokerFunc(func(_ context.Context, req AgentInvokeRequest) (AgentInvokeResult, error) {
				return AgentInvokeResult{
					Events: []session.Event{{
						Type: session.EventAssistant,
						Message: &model.Message{
							Role:  model.RoleAssistant,
							Parts: []model.Part{model.NewTextPart("remote controller response")},
						},
					}},
					ControllerConfigOptions: []control.ConfigOption{{
						Type:         "select",
						ID:           "model",
						Name:         "Model",
						Category:     "model",
						CurrentValue: "gpt-remote",
						Options: []control.ConfigChoice{
							{Value: "gpt-remote", Name: "Remote"},
							{Value: "gpt-next", Name: "Next"},
						},
					}, {
						Type:         "select",
						ID:           "reasoning_effort",
						Name:         "Reasoning",
						Category:     "thought_level",
						CurrentValue: "high",
						Options: []control.ConfigChoice{
							{Value: "low", Name: "Low"},
							{Value: "high", Name: "High"},
						},
					}, {
						Type:         "select",
						ID:           "mode",
						Name:         "Mode",
						Category:     "mode",
						CurrentValue: "code",
						Options: []control.ConfigChoice{
							{Value: "ask", Name: "Ask"},
							{Value: "code", Name: "Code"},
						},
					}},
				}, nil
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
	if engine.state[StateControllerConfigRef] != "controller-1" || engine.state[StateControllerModel] != "gpt-remote" || engine.state[StateControllerReasoning] != "high" || engine.state[StateControllerMode] != "code" {
		t.Fatalf("controller state = %#v, want remote current config values", engine.state)
	}
	engine.snapshot.State = cloneState(engine.state)
	status, ok, err := svc.Controllers().Status(ctx, session.Ref{SessionID: "sess-controller"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status.Model != "gpt-remote" || status.ReasoningEffort != "high" || status.Mode != "code" {
		t.Fatalf("controller status = %#v ok=%v, want remote config values", status, ok)
	}
	if len(status.ModelOptions) != 2 || status.ModelOptions[1].Value != "gpt-next" {
		t.Fatalf("model options = %#v, want remote choices", status.ModelOptions)
	}
	if len(status.EffortOptions) != 2 || status.EffortOptions[1].Value != "high" {
		t.Fatalf("effort options = %#v, want remote choices", status.EffortOptions)
	}
	if len(status.ModeOptions) != 2 || status.ModeOptions[1].ID != "code" {
		t.Fatalf("mode options = %#v, want remote choices", status.ModeOptions)
	}
}

func TestTurnServiceRoutesActiveACPControllerThroughAgentService(t *testing.T) {
	ctx := context.Background()
	controller := session.ControllerBinding{
		Kind:      session.ControllerACP,
		ID:        "reviewer",
		AgentName: "reviewer",
		EpochID:   "controller-1",
	}
	state := session.State{
		StateControllerConfigRef: "controller-1",
		StateControllerMode:      "plan",
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
	var invoked AgentInvokeRequest
	svc, err := New(Config{
		Runtime: config.Runtime{AppName: "caelis", UserID: "tester", WorkspaceKey: "repo"},
		Engine:  engine,
		Invokers: map[string]AgentInvoker{
			"reviewer": AgentInvokerFunc(func(_ context.Context, req AgentInvokeRequest) (AgentInvokeResult, error) {
				invoked = req
				return AgentInvokeResult{
					Events: []session.Event{{
						Type: session.EventAssistant,
						Message: &model.Message{
							Role:  model.RoleAssistant,
							Parts: []model.Part{model.NewTextPart("controller turn result")},
						},
					}},
				}, nil
			}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := svc.Turns().Begin(ctx, BeginTurnRequest{
		SessionRef: session.Ref{SessionID: "sess-controller"},
		Input:      "delegate this turn",
		Surface:    "headless",
	})
	if err != nil {
		t.Fatal(err)
	}
	events := collectRuntimeTurnEvents(t, turn)
	if invoked.AgentID != "reviewer" || invoked.Input != "delegate this turn" || invoked.Controller.Kind != session.ControllerACP || invoked.ControllerMode != "plan" {
		t.Fatalf("invoked request = %#v, want active controller with plan mode", invoked)
	}
	if engine.turn.Input != "" {
		t.Fatalf("engine turn = %#v, want controller route without local model turn", engine.turn)
	}
	if len(events) != 2 {
		t.Fatalf("turn events = %#v, want controller user prompt and assistant event", events)
	}
	if events[0].Type != session.EventUser || session.EventText(events[0]) != "delegate this turn" || events[0].Scope == nil || events[0].Scope.Controller.ID != "reviewer" {
		t.Fatalf("turn user event = %#v, want controller-scoped prompt", events[0])
	}
	if events[1].Type != session.EventAssistant || session.EventText(events[1]) != "controller turn result" || events[1].Actor.Kind != session.ActorController {
		t.Fatalf("turn assistant event = %#v, want controller assistant event", events[1])
	}
	if events[0].Scope.TurnID == "" || events[0].Scope.TurnID != events[1].Scope.TurnID {
		t.Fatalf("turn ids = %q / %q, want shared controller turn id", events[0].Scope.TurnID, events[1].Scope.TurnID)
	}
	if len(engine.eventBatches) != 2 || len(engine.eventBatches[0]) != 1 || engine.eventBatches[0][0].Type != session.EventUser || len(engine.eventBatches[1]) != 1 || engine.eventBatches[1][0].Type != session.EventAssistant {
		t.Fatalf("recorded batches = %#v, want user then assistant batches", engine.eventBatches)
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

func TestControllerServiceStatusIncludesLifecycleDiagnostics(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)
	controller := session.ControllerBinding{
		Kind:            session.ControllerACP,
		ID:              "reviewer",
		AgentName:       "reviewer",
		EpochID:         "controller-1",
		RemoteSessionID: "remote-reviewer",
	}
	engine := &recordingEngine{
		snapshot: session.Snapshot{
			Session: session.Session{
				Ref:        session.Ref{AppName: "caelis", UserID: "tester", SessionID: "sess-controller", WorkspaceKey: "repo"},
				Controller: controller,
			},
			State: session.State{},
		},
	}
	runs := &recordingControllerRunSource{runs: []ControllerRunStatus{{
		ID:              "run-1",
		Phase:           control.ControllerInvocationRemoteSession,
		SessionRef:      session.Ref{SessionID: "sess-controller"},
		TurnID:          "turn-1",
		Controller:      controller,
		RemoteSessionID: "remote-reviewer",
		Running:         true,
		Active:          true,
		StartedAt:       now,
		UpdatedAt:       now.Add(time.Second),
	}}}
	svc, err := New(Config{
		Runtime:        config.Runtime{AppName: "caelis", UserID: "tester", WorkspaceKey: "repo"},
		Engine:         engine,
		ControllerRuns: runs,
	})
	if err != nil {
		t.Fatal(err)
	}
	status, ok, err := svc.Controllers().Status(ctx, session.Ref{SessionID: "sess-controller"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status.Lifecycle == nil || status.Lifecycle.RunID != "run-1" || status.Lifecycle.Phase != string(control.ControllerInvocationRemoteSession) || !status.Lifecycle.Running || !status.Lifecycle.Active {
		t.Fatalf("controller status = %#v ok=%v, want active lifecycle", status, ok)
	}
	if len(status.Diagnostics) != 1 || status.Diagnostics[0].Severity != "info" || status.Diagnostics[0].Meta["turn_id"] != "turn-1" {
		t.Fatalf("controller diagnostics = %#v, want running lifecycle diagnostic", status.Diagnostics)
	}
	if runs.query.SessionRef.SessionID != "sess-controller" || runs.query.Controller.ID != "reviewer" {
		t.Fatalf("controller run query = %#v, want active controller query", runs.query)
	}
	view, err := svc.Status().View(ctx, StatusRequest{SessionRef: session.Ref{SessionID: "sess-controller"}})
	if err != nil {
		t.Fatal(err)
	}
	if view.Controller == nil || view.Controller.Lifecycle == nil || view.Controller.Lifecycle.RunID != "run-1" {
		t.Fatalf("status view controller = %#v, want lifecycle projection", view.Controller)
	}
	if !strings.Contains(formatCommandStatus(view), "controller: reviewer remote=remote-reviewer phase=remote_session") {
		t.Fatalf("formatted status = %q, want controller lifecycle", formatCommandStatus(view))
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

func TestEventServiceReplaysSurfaceNeutralEventStream(t *testing.T) {
	engine := &recordingEngine{
		replayEvents: []coreruntime.EventEnvelope{
			{
				Cursor: "cursor-user",
				Event: session.Event{
					ID:        "evt-user",
					SessionID: "sess-events",
					Type:      session.EventUser,
					Scope:     &session.EventScope{TurnID: "turn-1"},
					Message: &model.Message{
						Role:  model.RoleUser,
						Parts: []model.Part{model.NewTextPart("ping")},
					},
				},
			},
			{
				Cursor: "cursor-approval",
				Event: session.Event{
					ID:        "evt-approval",
					SessionID: "sess-events",
					Type:      session.EventApproval,
					Approval: &session.ApprovalEvent{
						ID:     "approval-1",
						Status: session.ApprovalPending,
						Tool: &session.ToolEvent{
							Name:  "run_command",
							Input: map[string]any{"command": "printf hello"},
						},
						Options: []session.ApprovalOption{{ID: "allow_once", Name: "Allow once", Kind: "allow"}},
					},
				},
			},
			{Err: "provider disconnected"},
		},
	}
	svc, err := New(Config{
		Runtime: config.Runtime{AppName: "caelis-app", UserID: "tester", WorkspaceKey: "repo"},
		Engine:  engine,
	})
	if err != nil {
		t.Fatal(err)
	}

	stream, err := svc.Events().Replay(context.Background(), EventReplayRequest{
		SessionRef:       session.Ref{SessionID: "sess-events"},
		After:            "cursor-before",
		Limit:            10,
		IncludeTransient: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := collectSessionEventEnvelopes(t, stream)
	if engine.replayReq.SessionRef.AppName != "caelis-app" || engine.replayReq.SessionRef.UserID != "tester" || engine.replayReq.After != "cursor-before" || engine.replayReq.Limit != 10 || !engine.replayReq.IncludeTransient {
		t.Fatalf("replay request = %#v, want runtime defaults and cursor options", engine.replayReq)
	}
	if len(events) != 3 {
		t.Fatalf("events = %#v, want user, approval, error", events)
	}
	if events[0].Cursor != "cursor-user" || events[0].Event == nil || events[0].Event.Type != string(session.EventUser) || events[0].Transcript == nil || events[0].Transcript.Text != "ping" {
		t.Fatalf("user event projection = %#v", events[0])
	}
	if events[1].Approval == nil || events[1].Approval.ID != "approval-1" || len(events[1].Approval.Actions) != 1 || !events[1].Approval.Actions[0].Approved {
		t.Fatalf("approval event projection = %#v, want approval action", events[1])
	}
	if events[2].Error != "provider disconnected" {
		t.Fatalf("error event = %#v, want provider disconnected", events[2])
	}
	events[0].Canonical.Message.Parts[0] = model.NewTextPart("mutated")
	stream, err = svc.Events().Replay(context.Background(), EventReplayRequest{SessionRef: session.Ref{SessionID: "sess-events"}})
	if err != nil {
		t.Fatal(err)
	}
	again := collectSessionEventEnvelopes(t, stream)
	if got := again[0].Canonical.Message.TextContent(); got != "ping" {
		t.Fatalf("canonical replay event was not cloned: %q", got)
	}
}

func TestEventServiceSubscribesActiveTurnEvents(t *testing.T) {
	events := make(chan coreruntime.EventEnvelope, 2)
	events <- coreruntime.EventEnvelope{
		Event: session.Event{
			ID:        "evt-live",
			SessionID: "sess-live",
			Type:      session.EventAssistant,
			Scope:     &session.EventScope{TurnID: "turn-live"},
			Message: &model.Message{
				Role:  model.RoleAssistant,
				Parts: []model.Part{model.NewTextPart("live answer")},
			},
		},
	}
	events <- coreruntime.EventEnvelope{
		Event: session.Event{
			ID:        "evt-lifecycle",
			SessionID: "sess-live",
			Type:      session.EventLifecycle,
			Lifecycle: &session.LifecycleEvent{Status: session.LifecycleCompleted, Reason: "done"},
		},
	}
	close(events)
	svc, err := New(Config{Engine: &recordingEngine{}})
	if err != nil {
		t.Fatal(err)
	}

	stream, err := svc.Events().SubscribeTurn(context.Background(), staticTurn{events: events})
	if err != nil {
		t.Fatal(err)
	}
	live := collectSessionEventEnvelopes(t, stream)
	if len(live) != 2 || live[0].Transcript == nil || live[0].Transcript.Text != "live answer" {
		t.Fatalf("live stream = %#v, want assistant transcript", live)
	}
	if live[1].Lifecycle == nil || live[1].Lifecycle.Status != string(session.LifecycleCompleted) || live[1].Lifecycle.Reason != "done" {
		t.Fatalf("lifecycle projection = %#v, want completed done", live[1])
	}
	if _, err := svc.Events().SubscribeTurn(context.Background(), nil); err == nil {
		t.Fatal("SubscribeTurn(nil) error = nil, want error")
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
			ModelTools: []model.ToolSpec{model.NewProviderExecutedToolSpec("web_search", map[string]json.RawMessage{
				"openai": json.RawMessage(`{"type":"web_search_preview"}`),
			})},
			Prompts: []plugin.PromptFragment{{
				ID:   "agents.workspace",
				Text: "workspace rule",
			}},
			AgentFiles: []appresources.AgentFile{{
				ID:   "agents.workspace",
				Path: "AGENTS.md",
			}},
			Diagnostics: []appresources.Diagnostic{{
				Severity: appresources.DiagnosticInfo,
				Kind:     "agent_file",
				ID:       "agents.workspace",
				Path:     "AGENTS.md",
				Message:  "agent instruction file loaded",
				Meta:     map[string]string{"scope": "workspace"},
			}, {
				Severity: appresources.DiagnosticWarning,
				Kind:     "skill_root",
				Path:     "/missing-skill-root",
				Message:  "skill root is not a directory",
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
	if status.Resources.Tools != 1 || status.Resources.ModelTools != 1 || status.Resources.Prompts != 1 || status.Resources.AgentFiles != 1 {
		t.Fatalf("resource status = %#v, want tool/model-tool/prompt/agent file counts", status.Resources)
	}
	if status.Resources.InfoCount != 1 || status.Resources.WarningCount != 1 || status.Resources.ErrorCount != 0 || len(status.Resources.Diagnostics) != 2 {
		t.Fatalf("resource diagnostics = %#v, want info/warning diagnostics", status.Resources)
	}

	status.Agents.Items[0].Args[0] = "changed"
	status.Agents.Items[0].Meta["scope"] = "changed"
	status.Resources.Diagnostics[0].Meta["scope"] = "changed"
	again, err := svc.Status().View(ctx, StatusRequest{SessionRef: session.Ref{SessionID: "sess-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if again.Agents.Items[0].Args[0] != "--stdio" || again.Agents.Items[0].Meta["scope"] != "workspace" {
		t.Fatalf("agent status was not cloned: %#v", again.Agents.Items[0])
	}
	if again.Resources.Diagnostics[0].Meta["scope"] != "workspace" {
		t.Fatalf("resource diagnostics were not cloned: %#v", again.Resources.Diagnostics[0])
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
		if name == "status" {
			if status.Lifecycle.Action != "" {
				t.Fatalf("status lifecycle = %#v, want empty lifecycle", status.Lifecycle)
			}
			continue
		}
		if status.Lifecycle.Action != name || status.Lifecycle.Backend != "host" ||
			!status.Lifecycle.Noop || status.Lifecycle.Attempted || status.Lifecycle.Supported {
			t.Fatalf("%s lifecycle = %#v, want host noop report", name, status.Lifecycle)
		}
	}
}

func TestSandboxServiceProjectsPolicyDiagnostics(t *testing.T) {
	svc, err := New(Config{
		Runtime: config.Runtime{
			Sandbox: config.Sandbox{
				Backend:       "host",
				Network:       "disabled",
				ReadableRoots: []string{"/repo"},
				WritableRoots: []string{"/repo/out"},
			},
		},
		Engine: &recordingEngine{},
		Sandbox: fakeSandboxRuntime{
			descriptor: sandbox.Descriptor{
				Backend:   sandbox.BackendHost,
				Isolation: sandbox.IsolationHost,
				Capabilities: sandbox.CapabilitySet{
					FileSystem:    true,
					CommandExec:   true,
					AsyncSessions: true,
				},
				DefaultConstraints: sandbox.Constraints{
					Route:      sandbox.RouteHost,
					Backend:    sandbox.BackendHost,
					Permission: sandbox.PermissionFullAccess,
					Isolation:  sandbox.IsolationHost,
					Network:    sandbox.NetworkInherit,
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

	status, err := svc.Sandbox().Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Network != "disabled" || status.DefaultNetwork != "inherit" ||
		status.DefaultPermission != "danger_full_access" || status.Isolation != "host" {
		t.Fatalf("sandbox policy status = %#v, want configured policy details", status)
	}
	if status.NetworkControl || status.PathPolicy || status.ReadableRootCount != 1 || status.WritableRootCount != 1 {
		t.Fatalf("sandbox capability/root status = %#v, want host policy diagnostics", status)
	}
	for _, kind := range []string{"route", "network", "roots"} {
		if !sandboxStatusDiagnostic(status.Diagnostics, kind) {
			t.Fatalf("sandbox diagnostics = %#v, missing %s", status.Diagnostics, kind)
		}
	}
}

func TestSandboxServiceRunsRuntimeLifecycle(t *testing.T) {
	rt := &recordingLifecycleSandboxRuntime{
		fakeSandboxRuntime: fakeSandboxRuntime{
			descriptor: sandbox.Descriptor{
				Backend: sandbox.BackendWindows,
				DefaultConstraints: sandbox.Constraints{
					Route:   sandbox.RouteSandbox,
					Backend: sandbox.BackendWindows,
				},
			},
			status: sandbox.Status{
				RequestedBackend: sandbox.BackendWindows,
				ResolvedBackend:  sandbox.BackendWindows,
				Setup: sandbox.SetupStatus{
					Required: true,
					Error:    "workspace setup required",
					Checks: []sandbox.SetupCheck{{
						Scope:    sandbox.SetupWorkspace,
						Required: true,
						Reason:   "policy changed",
					}},
				},
			},
		},
	}
	svc, err := New(Config{
		Runtime: config.Runtime{
			Sandbox: config.Sandbox{Backend: "windows"},
		},
		Engine:  &recordingEngine{},
		Sandbox: rt,
	})
	if err != nil {
		t.Fatal(err)
	}
	var progress []sandbox.PrepareProgress
	ctx := sandbox.ContextWithPrepareProgress(context.Background(), func(update sandbox.PrepareProgress) {
		progress = append(progress, update)
	})
	status, err := svc.Sandbox().Prepare(ctx)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if rt.prepareCalls != 1 || len(progress) != 1 || progress[0].Message != "preparing sandbox" {
		t.Fatalf("prepareCalls/progress = %d/%#v, want lifecycle call and progress", rt.prepareCalls, progress)
	}
	if status.RequestedBackend != "windows" || status.ResolvedBackend != "windows" || !status.SetupRequired || status.SetupError != "workspace setup required" {
		t.Fatalf("Prepare() status = %#v, want windows setup projection", status)
	}
	if status.Lifecycle.Action != "prepare" || !status.Lifecycle.Attempted || !status.Lifecycle.Supported || status.Lifecycle.Backend != "windows" {
		t.Fatalf("Prepare() lifecycle = %#v, want attempted prepare report", status.Lifecycle)
	}

	status, err = svc.Sandbox().Repair(context.Background())
	if err != nil {
		t.Fatalf("Repair() error = %v", err)
	}
	if status.Lifecycle.Action != "repair" || !status.Lifecycle.Attempted || !status.Lifecycle.Supported {
		t.Fatalf("Repair() lifecycle = %#v, want attempted repair report", status.Lifecycle)
	}
	status, err = svc.Sandbox().Preflight(context.Background(), true)
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if status.Lifecycle.Action != "preflight" || !status.Lifecycle.Attempted || !status.Lifecycle.Supported {
		t.Fatalf("Preflight() lifecycle = %#v, want attempted preflight report", status.Lifecycle)
	}
	status, err = svc.Sandbox().Reset(context.Background())
	if err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	if status.Lifecycle.Action != "reset" || !status.Lifecycle.Attempted || !status.Lifecycle.Supported {
		t.Fatalf("Reset() lifecycle = %#v, want attempted reset report", status.Lifecycle)
	}
	if rt.repairCalls != 1 || !rt.preflightAllow || rt.resetCalls != 1 {
		t.Fatalf("lifecycle calls repair=%d preflightAllow=%t reset=%d, want all invoked", rt.repairCalls, rt.preflightAllow, rt.resetCalls)
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

func TestTaskServiceListsAndControlsResolvedTasksWithoutSandbox(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	taskSession := &recordingTaskSession{
		snapshot: sandbox.SessionSnapshot{
			Ref:           sandbox.SessionRef{ID: "spawn-1", Backend: sandbox.BackendCustom},
			Command:       "SPAWN reviewer",
			State:         sandbox.SessionCompleted,
			Running:       false,
			SupportsInput: false,
			ExitCode:      0,
			StartedAt:     now,
			UpdatedAt:     now.Add(time.Second),
			Terminal:      sandbox.TerminalRef{ID: "spawn-spawn-1", SessionID: "spawn-1"},
			Metadata: map[string]any{
				"task_kind":         "subagent",
				"source":            "spawn",
				"agent":             "reviewer",
				"remote_session_id": "remote-1",
			},
		},
		stdout: "child done\n",
	}
	svc, err := New(Config{
		Runtime:      config.Runtime{AppName: "caelis", UserID: "tester"},
		Engine:       &recordingEngine{},
		TaskResolver: &recordingTaskResolver{sessions: map[string]*recordingTaskSession{"spawn-1": taskSession}},
	})
	if err != nil {
		t.Fatal(err)
	}

	list, err := svc.Tasks().List(context.Background(), ListTasksRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !list.Supported || list.Count != 1 {
		t.Fatalf("task list = %#v, want resolver-backed task list", list)
	}
	task := list.Tasks[0]
	if task.ID != "spawn-1" || task.Kind != "subagent" || task.Agent != "reviewer" || task.RemoteSessionID != "remote-1" || task.Source != "spawn" {
		t.Fatalf("resolved task = %#v, want spawn subagent metadata", task)
	}

	tail, err := svc.Tasks().Tail(context.Background(), TaskOutputRequest{TaskID: "spawn-1"})
	if err != nil {
		t.Fatal(err)
	}
	if tail.Stdout != "child done\n" || tail.Task.Kind != "subagent" {
		t.Fatalf("tail = %#v, want resolver output and metadata", tail)
	}

	cancelled, err := svc.Tasks().Cancel(context.Background(), TaskCancelRequest{TaskOutputRequest: TaskOutputRequest{TaskID: "spawn-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Task.State != string(sandbox.SessionCancelled) {
		t.Fatalf("cancelled task = %#v, want resolver cancel path", cancelled.Task)
	}
}

func TestTaskServiceListsLiveAndDurableTaskHistory(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	engine := &recordingEngine{
		snapshot: session.Snapshot{
			Session: session.Session{Ref: session.Ref{AppName: "caelis-app", UserID: "tester", SessionID: "sess-tasks", WorkspaceKey: "repo"}},
			Events: []session.Event{
				{
					ID:         "evt-transient",
					Type:       session.EventToolResult,
					Visibility: session.VisibilityUIOnly,
					Time:       now.Add(-time.Minute),
					Tool: &session.ToolEvent{
						Name: "RUN_COMMAND",
						Meta: map[string]any{
							"caelis": map[string]any{"runtime": map[string]any{"task": map[string]any{"task_id": "ignored"}}},
						},
					},
				},
				{
					ID:   "evt-run-start",
					Type: session.EventToolResult,
					Time: now,
					Scope: &session.EventScope{
						TurnID: "turn-1",
					},
					Tool: &session.ToolEvent{
						ID:     "call-run",
						Name:   "RUN_COMMAND",
						Title:  "RUN_COMMAND go test ./...",
						Status: session.ToolRunning,
						Input: map[string]any{
							"command": "go test ./...",
							"cwd":     "/repo",
						},
						Meta: map[string]any{
							"caelis": map[string]any{
								"runtime": map[string]any{
									"task": map[string]any{
										"action":        "start",
										"task_id":       "task-1",
										"state":         "running",
										"running":       true,
										"terminal_id":   "term-1",
										"stdout_cursor": int64(20),
										"stderr_cursor": int64(5),
									},
								},
							},
						},
					},
				},
				{
					ID:   "evt-spawn-result",
					Type: session.EventToolResult,
					Time: now.Add(time.Second),
					Scope: &session.EventScope{
						TurnID: "turn-2",
					},
					Tool: &session.ToolEvent{
						ID:     "spawn-call",
						Name:   "SPAWN",
						Title:  "SPAWN reviewer: inspect",
						Status: session.ToolCompleted,
						Input: map[string]any{
							"agent":  "reviewer",
							"prompt": "inspect",
						},
						Meta: map[string]any{
							"caelis": map[string]any{
								"runtime": map[string]any{
									"task": map[string]any{
										"task_id":           "spawn-1",
										"task_kind":         "subagent",
										"state":             "completed",
										"running":           false,
										"agent":             "reviewer",
										"remote_session_id": "remote-1",
									},
								},
							},
						},
					},
				},
				{
					ID:   "evt-task-tail",
					Type: session.EventToolResult,
					Time: now.Add(2 * time.Second),
					Scope: &session.EventScope{
						TurnID: "turn-1",
					},
					Tool: &session.ToolEvent{
						ID:     "task-call",
						Name:   "task",
						Status: session.ToolRunning,
						Output: map[string]any{
							"action":        "tail",
							"task_id":       "task-1",
							"state":         "running",
							"running":       true,
							"stdout_cursor": float64(42),
							"stderr_cursor": float64(6),
						},
					},
				},
				{
					ID:   "evt-spawn-child",
					Type: session.EventAssistant,
					Time: now.Add(3 * time.Second),
					Scope: &session.EventScope{
						TurnID: "turn-2",
						Participant: session.ParticipantBinding{
							ID:           "spawn-1",
							Kind:         session.ParticipantSubagent,
							Role:         session.ParticipantDelegated,
							AgentName:    "reviewer",
							SessionID:    "remote-1",
							ParentTurnID: "turn-2",
							DelegationID: "spawn-1",
							AttachedAt:   now.Add(time.Second),
						},
					},
					Message: &model.Message{Role: model.RoleAssistant, Parts: []model.Part{model.NewTextPart("child done")}},
				},
			},
		},
	}
	taskSession := &recordingTaskSession{
		snapshot: sandbox.SessionSnapshot{
			Ref:           sandbox.SessionRef{ID: "task-1", Backend: sandbox.BackendHost},
			Command:       "go test ./...",
			Dir:           "/repo",
			State:         sandbox.SessionCompleted,
			Running:       false,
			SupportsInput: true,
			StartedAt:     now,
			UpdatedAt:     now.Add(4 * time.Second),
			Terminal:      sandbox.TerminalRef{ID: "term-1", SessionID: "task-1"},
			OutputPreview: &sandbox.OutputSnapshot{
				Stdout: "final output\n",
				Cursor: sandbox.OutputCursor{Stdout: 42, Stderr: 6},
			},
		},
	}
	liveOnly := &recordingTaskSession{
		snapshot: sandbox.SessionSnapshot{
			Ref:       sandbox.SessionRef{ID: "live-only", Backend: sandbox.BackendHost},
			Command:   "sleep 10",
			State:     sandbox.SessionRunning,
			Running:   true,
			UpdatedAt: now.Add(5 * time.Second),
		},
	}
	svc, err := New(Config{
		Runtime: config.Runtime{AppName: "caelis-app", UserID: "tester", WorkspaceKey: "repo"},
		Engine:  engine,
		Sandbox: &recordingTaskRuntime{sessions: map[string]*recordingTaskSession{
			"task-1":    taskSession,
			"live-only": liveOnly,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	list, err := svc.Tasks().List(context.Background(), ListTasksRequest{
		SessionRef:     session.Ref{SessionID: "sess-tasks"},
		IncludeHistory: true,
		Limit:          10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if engine.loadRef.AppName != "caelis-app" || engine.loadRef.UserID != "tester" || engine.loadRef.WorkspaceKey != "repo" {
		t.Fatalf("load ref = %#v, want runtime defaults", engine.loadRef)
	}
	if !list.Supported || list.Count != 3 {
		t.Fatalf("task list = %#v, want live/history task set", list)
	}
	if list.Tasks[0].ID != "live-only" {
		t.Fatalf("first task = %#v, want newest live task first", list.Tasks[0])
	}
	task, ok := findTaskItem(list.Tasks, "task-1")
	if !ok {
		t.Fatalf("task-1 missing from %#v", list.Tasks)
	}
	if task.Source != "live" || task.State != string(sandbox.SessionCompleted) || task.StdoutCursor != 42 || task.StderrCursor != 6 || task.Action != "tail" || task.TerminalID != "term-1" {
		t.Fatalf("task-1 = %#v, want live state merged with durable cursors", task)
	}
	if !strings.Contains(task.OutputPreview, "final output") {
		t.Fatalf("task-1 = %#v, want live terminal preview", task)
	}
	spawn, ok := findTaskItem(list.Tasks, "spawn-1")
	if !ok {
		t.Fatalf("spawn-1 missing from %#v", list.Tasks)
	}
	if spawn.Source != "history" || spawn.Kind != "subagent" || spawn.Agent != "reviewer" || spawn.RemoteSessionID != "remote-1" || spawn.State != "completed" || spawn.EventID != "evt-spawn-child" || spawn.TurnID != "turn-2" {
		t.Fatalf("spawn task = %#v, want durable subagent task metadata", spawn)
	}
	if _, ok := findTaskItem(list.Tasks, "ignored"); ok {
		t.Fatalf("transient task leaked into history: %#v", list.Tasks)
	}

	list.Tasks[0].Command = "mutated"
	again, err := svc.Tasks().List(context.Background(), ListTasksRequest{
		SessionRef:     session.Ref{SessionID: "sess-tasks"},
		IncludeHistory: true,
		Limit:          10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.Tasks[0].Command == "mutated" {
		t.Fatalf("task list was not rebuilt from live/history sources: %#v", again.Tasks[0])
	}
}

func TestTaskServiceListsDurableTaskHistoryWithoutSandboxLister(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	engine := &recordingEngine{
		snapshot: session.Snapshot{
			Session: session.Session{Ref: session.Ref{SessionID: "sess-history"}},
			Events: []session.Event{{
				ID:   "evt-run",
				Type: session.EventToolResult,
				Time: now,
				Tool: &session.ToolEvent{
					Name: "RUN_COMMAND",
					Input: map[string]any{
						"command": "make quality",
					},
					Meta: map[string]any{
						"caelis": map[string]any{
							"runtime": map[string]any{
								"task": map[string]any{
									"task_id": "task-history",
									"state":   "completed",
									"running": false,
								},
							},
						},
					},
				},
			}},
		},
	}
	svc, err := New(Config{
		Runtime: config.Runtime{AppName: "caelis-app", UserID: "tester"},
		Engine:  engine,
	})
	if err != nil {
		t.Fatal(err)
	}

	unsupported, err := svc.Tasks().List(context.Background(), ListTasksRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if unsupported.Supported || unsupported.Count != 0 {
		t.Fatalf("task list without lister/history = %#v, want unsupported", unsupported)
	}
	history, err := svc.Tasks().List(context.Background(), ListTasksRequest{
		SessionRef:     session.Ref{SessionID: "sess-history"},
		IncludeHistory: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !history.Supported || history.Count != 1 || history.Tasks[0].ID != "task-history" || history.Tasks[0].Command != "make quality" {
		t.Fatalf("history task list = %#v, want durable task without live sandbox", history)
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

func TestModelServicePrepareConnectConfigReusesExistingProfileAuth(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.UpsertModel(ctx, appsettings.ModelConfig{
		Provider:     "xiaomi",
		Model:        "mimo-v2.5-pro",
		BaseURL:      ConnectXiaomiTokenPlanCNBaseURL,
		Token:        "secret-token",
		PersistToken: true,
		AuthType:     "api_key",
		Timeout:      45 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis-app", UserID: "tester"},
		Engine:   &recordingEngine{},
		Settings: manager,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := svc.Models().PrepareConnectConfig(ctx, appsettings.ModelConfig{
		Provider: "xiaomi",
		Model:    "mimo-v2-pro",
		BaseURL:  ConnectXiaomiTokenPlanCNBaseURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Token != "" || prepared.TokenEnv != "" || prepared.AuthType != "" || prepared.Timeout != 0 || prepared.PersistToken {
		t.Fatalf("prepared reusable auth fields = %#v, want profile auth left untouched", prepared)
	}
	second, err := svc.Models().Connect(ctx, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if second.ProfileID != first.ProfileID || second.Token != "secret-token" || second.Timeout != 45*time.Second {
		t.Fatalf("connected reusable profile = first:%#v second:%#v, want existing auth profile reused", first, second)
	}
}

func TestModelServicePromptCapabilitiesReflectConfiguredModels(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis-app", UserID: "tester"},
		Engine:   &recordingEngine{},
		Settings: manager,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Models().Connect(ctx, appsettings.ModelConfig{
		Provider: "deepseek",
		Model:    "deepseek-v4-pro",
	}); err != nil {
		t.Fatal(err)
	}
	caps, err := svc.Models().PromptCapabilities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if caps.Image {
		t.Fatalf("prompt capabilities = %#v, want no image support for deepseek-only config", caps)
	}
	if _, err := svc.Models().Connect(ctx, appsettings.ModelConfig{
		Provider: "openai",
		Model:    "gpt-4o",
	}); err != nil {
		t.Fatal(err)
	}
	caps, err = svc.Models().PromptCapabilities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !caps.Image {
		t.Fatalf("prompt capabilities = %#v, want image support once configured model supports images", caps)
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
	var factoryCalls int
	svc, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis-app", UserID: "tester", WorkspaceKey: "repo"},
		Engine:   &recordingEngine{},
		Settings: manager,
		ModelProvider: func(_ context.Context, cfg appsettings.ModelConfig) (model.Provider, error) {
			factoryCalls++
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
	again, err := svc.Models().ProviderModels(ctx, appsettings.ModelConfig{
		Provider: "openai-compatible",
		BaseURL:  "https://api.example.test/v1",
		Token:    "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(again, models) || factoryCalls != 1 {
		t.Fatalf("cached provider models = %#v calls=%d, want cached result with one provider call", again, factoryCalls)
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
				{
					ID:                     "gpt-remote",
					Provider:               "openai-compatible",
					ContextWindowTokens:    64000,
					MaxOutputTokens:        12000,
					ReasoningEfforts:       []string{"low", "high"},
					DefaultReasoningEffort: "low",
					SupportsToolCalls:      true,
					SupportsJSON:           true,
				},
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
	if !remote.CapabilitiesKnown ||
		remote.Capabilities.ContextWindowTokens != 64000 ||
		remote.Capabilities.MaxOutputTokens != 12000 ||
		!remote.Capabilities.SupportsToolCalls ||
		!remote.Capabilities.SupportsJSONOutput ||
		!slices.Equal(remote.ReasoningLevels, []string{"none", "low", "high"}) {
		t.Fatalf("remote capabilities = %#v levels=%#v, want hydrated remote capabilities", remote.Capabilities, remote.ReasoningLevels)
	}
	remoteCaps, ok := svc.Models().LookupCapabilities("openai-compatible", "gpt-remote")
	if !ok || remoteCaps.ContextWindowTokens != 64000 || !slices.Equal(svc.Models().ReasoningLevels("openai-compatible", "gpt-remote"), []string{"none", "low", "high"}) {
		t.Fatalf("cached remote caps = %#v ok=%v, want cached discovery capabilities", remoteCaps, ok)
	}
	if captured.Provider != "openai-compatible" || captured.BaseURL != "https://api.example.test/v1" || captured.Token != "secret" {
		t.Fatalf("provider factory cfg = %#v, want discovery config with selected provider", captured)
	}
}

func TestModelServiceConnectPanelProjectsSharedSetup(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := manager.UpsertModel(ctx, appsettings.ModelConfig{
		Provider: "xiaomi",
		Model:    "mimo-v2.5-pro",
		BaseURL:  ConnectXiaomiTokenPlanCNBaseURL,
		TokenEnv: "MIMO_TOKEN_PLAN_API_KEY",
	})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(Config{
		Runtime: config.Runtime{AppName: "caelis", UserID: "tester"},
		Engine: &recordingEngine{snapshot: session.Snapshot{
			Session: session.Session{Ref: session.Ref{SessionID: "sess-connect-panel"}},
			State:   session.State{StateCurrentModelID: cfg.ID},
		}},
		Settings: manager,
	})
	if err != nil {
		t.Fatal(err)
	}
	panel, err := svc.Models().ConnectPanel(ctx, ModelConnectRequest{SessionRef: session.Ref{SessionID: "sess-connect-panel"}})
	if err != nil {
		t.Fatal(err)
	}
	if panel.Current == nil || panel.Current.ID != cfg.ID || len(panel.Configured) != 1 || len(panel.Wizard.Steps) == 0 {
		t.Fatalf("connect panel current/configured/wizard = %#v/%#v/%#v, want shared setup view", panel.Current, panel.Configured, panel.Wizard)
	}
	xiaomi, ok := findConnectProvider(panel.Providers, "xiaomi")
	if !ok || !xiaomi.Configured || xiaomi.ConfiguredModelCount != 1 || xiaomi.TokenEnv != "XIAOMI_API_KEY" || len(xiaomi.Endpoints) != 2 {
		t.Fatalf("xiaomi provider = %#v ok=%v, want configured provider with endpoints", xiaomi, ok)
	}
	tokenPlan, ok := findConnectEndpoint(xiaomi.Endpoints, "token-plan-cn")
	if !ok || tokenPlan.TokenEnv != "MIMO_TOKEN_PLAN_API_KEY" || !tokenPlan.ReusableAuth {
		t.Fatalf("token plan endpoint = %#v ok=%v, want reusable auth endpoint", tokenPlan, ok)
	}
	if len(panel.Diagnostics) != 0 {
		t.Fatalf("connect diagnostics = %#v, want none for configured current model", panel.Diagnostics)
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
	start        session.StartRequest
	list         session.ListQuery
	page         session.SessionPage
	turn         coreruntime.TurnRequest
	events       []session.Event
	eventBatches [][]session.Event
	turnEvents   []session.Event
	replayReq    coreruntime.ReplayRequest
	replayEvents []coreruntime.EventEnvelope
	loadRef      session.Ref
	state        session.State
	snapshot     session.Snapshot
}

type fakeSandboxRuntime struct {
	descriptor sandbox.Descriptor
	status     sandbox.Status
}

type recordingLifecycleSandboxRuntime struct {
	fakeSandboxRuntime
	prepareCalls   int
	repairCalls    int
	preflightAllow bool
	resetCalls     int
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

func findConnectProvider(providers []appviewmodel.ModelConnectProvider, id string) (appviewmodel.ModelConnectProvider, bool) {
	for _, provider := range providers {
		if provider.ID == id || provider.Provider == id || provider.Label == id {
			return provider, true
		}
	}
	return appviewmodel.ModelConnectProvider{}, false
}

func findConnectEndpoint(endpoints []appviewmodel.ModelConnectEndpoint, id string) (appviewmodel.ModelConnectEndpoint, bool) {
	for _, endpoint := range endpoints {
		if endpoint.ID == id {
			return endpoint, true
		}
	}
	return appviewmodel.ModelConnectEndpoint{}, false
}

func findCommandView(commands []appviewmodel.CommandView, name string) (appviewmodel.CommandView, bool) {
	for _, command := range commands {
		if command.Name == name {
			return command, true
		}
	}
	return appviewmodel.CommandView{}, false
}

func findAgentManagementItem(items []appviewmodel.AgentManagementItem, name string) (appviewmodel.AgentManagementItem, bool) {
	for _, item := range items {
		if item.Agent.Name == name || item.Agent.ID == name {
			return item, true
		}
	}
	return appviewmodel.AgentManagementItem{}, false
}

func findTaskItem(items []appviewmodel.TaskItem, id string) (appviewmodel.TaskItem, bool) {
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return appviewmodel.TaskItem{}, false
}

func findSettingsPanelSection(sections []appviewmodel.SettingsPanelSection, id string) (appviewmodel.SettingsPanelSection, bool) {
	for _, section := range sections {
		if section.ID == id {
			return section, true
		}
	}
	return appviewmodel.SettingsPanelSection{}, false
}

func findSettingsPanelAction(actions []appviewmodel.SettingsPanelAction, id string) (appviewmodel.SettingsPanelAction, bool) {
	for _, action := range actions {
		if action.ID == id {
			return action, true
		}
	}
	return appviewmodel.SettingsPanelAction{}, false
}

func panelActionEnabled(actions []appviewmodel.SettingsPanelAction, id string) bool {
	action, ok := findSettingsPanelAction(actions, id)
	return ok && action.Enabled
}

func panelDiagnostic(items []appviewmodel.SettingsPanelDiagnostic, source string, kind string) bool {
	for _, item := range items {
		if item.Source == source && item.Kind == kind {
			return true
		}
	}
	return false
}

func panelDiagnosticActions(items []appviewmodel.SettingsPanelDiagnostic, source string, kind string) []string {
	for _, item := range items {
		if item.Source == source && item.Kind == kind {
			return item.ActionIDs
		}
	}
	return nil
}

func sandboxStatusDiagnostic(items []SandboxDiagnostic, kind string) bool {
	for _, item := range items {
		if item.Kind == kind {
			return true
		}
	}
	return false
}

func agentActionEnabled(actions []appviewmodel.AgentManagementAction, id string) bool {
	for _, action := range actions {
		if action.ID == id {
			return action.Enabled
		}
	}
	return false
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

func (r *recordingLifecycleSandboxRuntime) Prepare(ctx context.Context) error {
	r.prepareCalls++
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{Message: "preparing sandbox", Step: 1, Total: 1})
	return nil
}

func (r *recordingLifecycleSandboxRuntime) Repair(context.Context) error {
	r.repairCalls++
	return nil
}

func (r *recordingLifecycleSandboxRuntime) Preflight(_ context.Context, opts sandbox.PreflightOptions) error {
	r.preflightAllow = opts.AllowNonElevatedRepair
	return nil
}

func (r *recordingLifecycleSandboxRuntime) Reset(context.Context) error {
	r.resetCalls++
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

type recordingTaskResolver struct {
	sessions map[string]*recordingTaskSession
}

func (r *recordingTaskResolver) OpenTask(_ context.Context, ref sandbox.SessionRef) (sandbox.Session, bool, error) {
	session, ok := r.sessions[strings.TrimSpace(ref.ID)]
	if !ok {
		return nil, false, nil
	}
	return session, true, nil
}

func (r *recordingTaskResolver) ListTasks(context.Context, sandbox.SessionListQuery) ([]sandbox.SessionSnapshot, error) {
	out := make([]sandbox.SessionSnapshot, 0, len(r.sessions))
	for _, session := range r.sessions {
		out = append(out, session.snapshot)
	}
	return out, nil
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
	e.eventBatches = append(e.eventBatches, cloneTestEvents(events))
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

func collectSessionEventEnvelopes(t *testing.T, stream <-chan appviewmodel.SessionEventEnvelope) []appviewmodel.SessionEventEnvelope {
	t.Helper()
	var out []appviewmodel.SessionEventEnvelope
	for env := range stream {
		out = append(out, appviewmodel.CloneSessionEventEnvelope(env))
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

type recordingControllerRunSource struct {
	query ControllerRunQuery
	runs  []ControllerRunStatus
}

func (s *recordingControllerRunSource) ControllerRuns(_ context.Context, query ControllerRunQuery) ([]ControllerRunStatus, error) {
	s.query = query
	out := make([]ControllerRunStatus, len(s.runs))
	copy(out, s.runs)
	return out, nil
}

func (e *recordingEngine) Interrupt(context.Context, session.Ref) error {
	return nil
}

func (e *recordingEngine) Replay(_ context.Context, req coreruntime.ReplayRequest) (<-chan coreruntime.EventEnvelope, error) {
	e.replayReq = req
	events := make(chan coreruntime.EventEnvelope, len(e.replayEvents))
	for _, env := range e.replayEvents {
		next := env
		next.Event = session.CloneEvent(env.Event)
		events <- next
	}
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
