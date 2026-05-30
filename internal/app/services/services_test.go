package services

import (
	"context"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/core/config"
	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/plugin"
	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
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
