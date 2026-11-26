package appserver

import (
	"database/sql"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestSessionWorkbenchIgnoresLateOlderTerminal(t *testing.T) {
	ctx := t.Context()
	workbench, events, ref := testSessionWorkbenchTurnStore(t)
	older, err := workbench.BeginTurn(ctx, ref, "older")
	if err != nil {
		t.Fatal(err)
	}
	newer, err := workbench.BeginTurn(ctx, ref, "newer")
	if err != nil {
		t.Fatal(err)
	}
	// Neither Turn has a result position. The outcome still follows admission order.
	if _, err := workbench.FinishTurn(ctx, newer, 0, eventstream.LifecycleStateCompleted); err != nil {
		t.Fatal(err)
	}
	if _, err := workbench.FinishTurn(ctx, older, 0, eventstream.LifecycleStateFailed); err != nil {
		t.Fatal(err)
	}
	state, err := workbench.states(ctx, ref.UserID, []string{ref.SessionID})
	if err != nil || len(state) != 1 || state[ref.SessionID].failed {
		t.Fatalf("late failed outcome overwrote the newer completed Turn: %+v, %v", state, err)
	}

	latest, err := workbench.BeginTurn(ctx, ref, "latest")
	if err != nil {
		t.Fatal(err)
	}
	message := model.NewTextMessage(model.RoleAssistant, "Result from the latest Turn")
	reply, err := events.AppendEvent(ctx, session.AppendEventRequest{SessionRef: ref, Event: &session.Event{
		Type: session.EventTypeAssistant, Message: &message, Scope: &session.EventScope{TurnID: "latest"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	seq, err := workbench.FinishTurn(ctx, latest, 0, eventstream.LifecycleStateFailed)
	if err != nil || seq != reply.Seq || seq == 0 {
		t.Fatalf("finish recovered sequence %d, want %d: %v", seq, reply.Seq, err)
	}
	state, err = workbench.states(ctx, ref.UserID, []string{ref.SessionID})
	if err != nil || !state[ref.SessionID].failed || !state[ref.SessionID].unread {
		t.Fatalf("latest failure and result should both be visible: %+v, %v", state, err)
	}
}

func TestSessionWorkbenchReadBeforeResultIsIndexed(t *testing.T) {
	ctx := t.Context()
	workbench, events, ref := testSessionWorkbenchTurnStore(t)
	if _, err := workbench.BeginTurn(ctx, ref, "unfinished"); err != nil {
		t.Fatal(err)
	}
	message := model.NewTextMessage(model.RoleAssistant, "Visible before indexing")
	reply, err := events.AppendEvent(ctx, session.AppendEventRequest{SessionRef: ref, Event: &session.Event{
		Type: session.EventTypeAssistant, Message: &message, Scope: &session.EventScope{TurnID: "unfinished"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := workbench.acknowledge(ctx, ref.UserID, ref.SessionID, reply.Seq+1); !errorcode.Is(err, errorcode.InvalidArgument) {
		t.Fatalf("read beyond durable history should fail: %v", err)
	}
	if err := workbench.acknowledge(ctx, ref.UserID, ref.SessionID, reply.Seq); err != nil {
		t.Fatal(err)
	}
	if err := workbench.RecoverPending(ctx); err != nil {
		t.Fatal(err)
	}
	state, err := workbench.states(ctx, ref.UserID, []string{ref.SessionID})
	if err != nil || len(state) != 1 || state[ref.SessionID].unread {
		t.Fatalf("result recovery discarded the earlier authoritative receipt: %+v, %v", state, err)
	}
}

func testSessionWorkbenchTurnStore(t *testing.T) (*SessionWorkbenchStore, *inmemory.Store, session.SessionRef) {
	t.Helper()
	ctx := t.Context()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	events := inmemory.NewStore(inmemory.Config{})
	workbench, err := NewSessionWorkbenchStore(ctx, db, events)
	if err != nil {
		t.Fatal(err)
	}
	active, err := events.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	return workbench, events, active.SessionRef
}
