package gatewaydriver

import (
	"context"
	"maps"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/core/config"
	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	coresession "github.com/OnslaughtSnail/caelis/core/session"
	appservices "github.com/OnslaughtSnail/caelis/internal/app/services"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
	portsession "github.com/OnslaughtSnail/caelis/ports/session"
)

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
	svc, err := appservices.New(appservices.Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "user-1",
			WorkspaceKey: "repo",
			WorkspaceCWD: t.TempDir(),
			Sandbox:      config.Sandbox{Backend: "host"},
		},
		Engine:   engine,
		Settings: manager,
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

type appServiceDriverEngine struct {
	start    coresession.StartRequest
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
	return coresession.SessionPage{}, nil
}

func (e *appServiceDriverEngine) LoadSession(_ context.Context, ref coresession.Ref) (coresession.Snapshot, error) {
	snapshot := e.snapshot
	if snapshot.Session.SessionID == "" {
		snapshot.Session.Ref = ref
	}
	snapshot.State = cloneCoreState(e.state)
	return snapshot, nil
}

func (e *appServiceDriverEngine) RecordEvents(_ context.Context, _ coresession.Ref, events []coresession.Event) (coresession.Cursor, error) {
	e.events = cloneCoreEvents(events)
	return "", nil
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
