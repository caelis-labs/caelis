package bot

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestDesktopConnectionClaimReceiptAndRevocation(t *testing.T) {
	ctx := t.Context()
	store, err := OpenWorkStore(filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.InstanceID = "instance-one"
	id := Identity("alice", "bot")
	registration, err := store.RegisterClient(ctx, "alice", id, "enroll", []string{"clock", "reminders", "gesture"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AuthenticateClient(ctx, registration.Token+"bad"); err == nil {
		t.Fatal("forged credential accepted")
	}
	client, err := store.ActivateClient(ctx, registration.Client)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.RecordRequest(ctx, "alice", id, client.ID, "request", "digest", "What time is it?")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindRequest(ctx, source, Execution{InstanceID: "instance-one", SessionID: "bot-chat", HandleID: "handle", RunID: "run", TurnID: "turn"}); err != nil {
		t.Fatal(err)
	}
	call, err := store.QueueDesktop(ctx, source, "call-a", "clock", DesktopArguments{})
	if err != nil {
		t.Fatal(err)
	}
	same, err := store.QueueDesktop(ctx, source, "call-b", "clock", DesktopArguments{})
	if err != nil || same.ID != call.ID {
		t.Fatalf("new tool id duplicated native effect: %v", err)
	}
	claim, err := store.ClaimDesktop(ctx, client, call.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimDesktop(ctx, client, call.ID); err == nil {
		t.Fatal("unknown claim could be dispatched again")
	}
	receipt := DesktopReceipt{Token: claim.Token, Result: json.RawMessage(`{"time":"2026-09-22T12:00:00+08:00"}`)}
	done, err := store.CompleteDesktop(ctx, client, call.ID, receipt)
	if err != nil || done.State != "completed" {
		t.Fatalf("receipt: %+v %v", done, err)
	}
	if _, err := store.CompleteDesktop(ctx, client, call.ID, receipt); err != nil {
		t.Fatal(err)
	}
	conflict := receipt
	conflict.Result = json.RawMessage(`{}`)
	if _, err := store.CompleteDesktop(ctx, client, call.ID, conflict); err == nil {
		t.Fatal("conflicting receipt accepted")
	}
	if err := store.ExitClient(ctx, client); err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueDesktop(ctx, source, "gesture", "gesture", DesktopArguments{Gesture: "nod"}); err == nil {
		t.Fatal("revoked connection retained tools")
	}
	reopened, err := store.ActivateClient(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.ActivationID == client.ActivationID {
		t.Fatal("exit reactivation kept old activation identity")
	}
	if _, err := store.CompleteDesktop(ctx, reopened, call.ID, receipt); err == nil {
		t.Fatal("old activation receipt accepted")
	}
	store.InstanceID = "instance-two"
	if _, err := store.ActiveClient(ctx, "alice", id, client.ID); err == nil {
		t.Fatal("Host restart automatically revived tools")
	}
	if _, err := store.RegisterClient(ctx, "alice", id, "invalid", []string{"RunCommand"}); err == nil {
		t.Fatal("arbitrary command tool admitted")
	}
}
