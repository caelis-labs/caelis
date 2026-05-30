package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/core/config"
	"github.com/OnslaughtSnail/caelis/core/model"
	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/internal/app/services"
)

func TestRunOnceStartsSessionAndReturnsAssistantOutput(t *testing.T) {
	engine := &fakeEngine{
		events: []coreruntime.EventEnvelope{{
			Cursor: "2",
			Event: session.Event{
				Type: session.EventAssistant,
				Message: &model.Message{
					Role:  model.RoleAssistant,
					Parts: []model.Part{model.NewTextPart("done")},
					Usage: &model.Usage{InputTokens: 7, OutputTokens: 3, TotalTokens: 10},
				},
			},
		}},
	}
	svc := newTestServices(t, engine)

	result, err := RunOnce(context.Background(), Request{
		Services:           svc,
		PreferredSessionID: "sess-fixed",
		Workspace:          session.Workspace{Key: "repo", CWD: "/tmp/repo"},
		Input:              "hello",
		Model:              "gpt-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Session.SessionID != "sess-fixed" || engine.start.Workspace.Key != "repo" {
		t.Fatalf("session = %#v start = %#v, want preferred workspace session", result.Session, engine.start)
	}
	if engine.turn.Model != "gpt-test" || engine.turn.Surface != "headless" {
		t.Fatalf("turn = %#v, want headless model turn", engine.turn)
	}
	if result.Output != "done" || result.Usage.TotalTokens != 10 || result.LastCursor != "2" {
		t.Fatalf("result = %#v, want assistant output, usage, cursor", result)
	}
	var text bytes.Buffer
	if err := WriteResult(&text, result, OutputText); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(text.String()) != "done" {
		t.Fatalf("text output = %q, want done", text.String())
	}
	var raw bytes.Buffer
	if err := WriteResult(&raw, result, OutputJSON); err != nil {
		t.Fatal(err)
	}
	var decoded Result
	if err := json.Unmarshal(raw.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Output != "done" || decoded.Session.SessionID != "sess-fixed" {
		t.Fatalf("json output = %#v, want serialized result", decoded)
	}
}

func TestRunOnceAutoDeniesApprovalByDefault(t *testing.T) {
	engine := &fakeEngine{
		events: []coreruntime.EventEnvelope{{
			Cursor: "3",
			Event: session.Event{
				Type: session.EventApproval,
				Approval: &session.ApprovalEvent{
					ID:     "approval-call-1",
					Status: session.ApprovalPending,
					Options: []session.ApprovalOption{
						{ID: "allow_once", Kind: "allow"},
						{ID: "reject_once", Kind: "reject"},
					},
				},
			},
		}},
	}
	svc := newTestServices(t, engine)

	if _, err := RunOnce(context.Background(), Request{Services: svc, Input: "run"}); err != nil {
		t.Fatal(err)
	}
	if len(engine.turn.submissions) != 1 {
		t.Fatalf("submissions = %d, want 1", len(engine.turn.submissions))
	}
	decision := engine.turn.submissions[0].Approval
	if decision == nil || decision.Approved || decision.OptionID != "reject_once" {
		t.Fatalf("approval decision = %#v, want auto deny reject_once", decision)
	}
}

func TestRunOnceCanApproveWithCustomResolver(t *testing.T) {
	engine := &fakeEngine{
		events: []coreruntime.EventEnvelope{{
			Event: session.Event{
				Type: session.EventApproval,
				Approval: &session.ApprovalEvent{
					ID:      "approval-call-1",
					Status:  session.ApprovalPending,
					Options: []session.ApprovalOption{{ID: "allow_once", Kind: "allow"}},
				},
			},
		}},
	}
	svc := newTestServices(t, engine)

	_, err := RunOnce(context.Background(), Request{
		Services: svc,
		Input:    "run",
		ResolveApproval: func(context.Context, *session.ApprovalEvent) (coreruntime.ApprovalDecision, error) {
			return coreruntime.ApprovalDecision{Approved: true, Outcome: "allow", OptionID: "allow_once"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := engine.turn.submissions[0].Approval; got == nil || !got.Approved || got.OptionID != "allow_once" {
		t.Fatalf("approval decision = %#v, want custom allow_once", got)
	}
}

func newTestServices(t *testing.T, engine *fakeEngine) services.Services {
	t.Helper()
	svc, err := services.New(services.Config{
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
	return svc
}

type fakeEngine struct {
	start  session.StartRequest
	turn   *fakeTurn
	events []coreruntime.EventEnvelope
}

func (e *fakeEngine) StartSession(_ context.Context, req session.StartRequest) (session.Session, error) {
	e.start = req
	sessionID := firstNonEmpty(req.PreferredSessionID, "sess-1")
	return session.Session{
		Ref: session.Ref{
			AppName:      req.AppName,
			UserID:       req.UserID,
			SessionID:    sessionID,
			WorkspaceKey: req.Workspace.Key,
		},
		Workspace: req.Workspace,
		Title:     req.Title,
	}, nil
}

func (e *fakeEngine) LoadSession(context.Context, session.Ref) (session.Snapshot, error) {
	return session.Snapshot{}, nil
}

func (e *fakeEngine) ListSessions(context.Context, session.ListQuery) (session.SessionPage, error) {
	return session.SessionPage{}, nil
}

func (e *fakeEngine) RecordEvents(context.Context, session.Ref, []session.Event) (session.Cursor, error) {
	return "", nil
}

func (e *fakeEngine) UpdateSessionState(context.Context, session.Ref, session.StatePatch) error {
	return nil
}

func (e *fakeEngine) BeginTurn(_ context.Context, req coreruntime.TurnRequest) (coreruntime.Turn, error) {
	e.turn = newFakeTurn(req, e.events)
	return e.turn, nil
}

func (e *fakeEngine) Interrupt(context.Context, session.Ref) error {
	return nil
}

func (e *fakeEngine) Replay(context.Context, coreruntime.ReplayRequest) (<-chan coreruntime.EventEnvelope, error) {
	events := make(chan coreruntime.EventEnvelope)
	close(events)
	return events, nil
}

type fakeTurn struct {
	coreruntime.TurnRequest
	events      chan coreruntime.EventEnvelope
	submissions []coreruntime.Submission
}

func newFakeTurn(req coreruntime.TurnRequest, events []coreruntime.EventEnvelope) *fakeTurn {
	ch := make(chan coreruntime.EventEnvelope, len(events))
	for _, env := range events {
		ch <- env
	}
	close(ch)
	return &fakeTurn{TurnRequest: req, events: ch}
}

func (t *fakeTurn) ID() string                               { return "turn-1" }
func (t *fakeTurn) RunID() string                            { return "run-1" }
func (t *fakeTurn) SessionRef() session.Ref                  { return t.TurnRequest.SessionRef }
func (t *fakeTurn) StartedAt() time.Time                     { return time.Unix(1, 0).UTC() }
func (t *fakeTurn) Events() <-chan coreruntime.EventEnvelope { return t.events }
func (t *fakeTurn) Submit(_ context.Context, req coreruntime.Submission) error {
	t.submissions = append(t.submissions, req)
	return nil
}
func (t *fakeTurn) Cancel() coreruntime.CancelResult {
	return coreruntime.CancelResult{Status: coreruntime.CancelCancelled}
}
func (t *fakeTurn) Close() error { return nil }
