package bot

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestReminderAuthorityOccurrenceAndRestart(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "control.sqlite")
	store, err := OpenWorkStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	store.InstanceID = "host-one"
	id := Identity("owner", "bot")
	enrollment, err := store.RegisterClient(ctx, "owner", id, "enroll", []string{"reminders"})
	if err != nil {
		t.Fatal(err)
	}
	client, err := store.ActivateClient(ctx, enrollment.Client)
	if err != nil {
		t.Fatal(err)
	}
	var credential clientCredential
	if err := store.db.Get(ctx, "client", client.ID, &credential); err != nil {
		t.Fatal(err)
	}
	credential.Client.ActivatedAt = time.Now().Add(-24 * time.Hour)
	if err := store.db.Replace(ctx, "client", client.ID, credential); err != nil {
		t.Fatal(err)
	}
	client = credential.Client
	source, err := store.RecordRequest(ctx, "owner", id, client.ID, "prompt", "digest", "Remind me to read this report.")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindRequest(ctx, source, Execution{InstanceID: "host-one", SessionID: "bot-chat", HandleID: "handle", RunID: "run", TurnID: "turn"}); err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Add(-time.Second).Truncate(time.Second)
	call, err := store.QueueDesktop(ctx, source, "save", "reminders", DesktopArguments{Operation: "save", ID: "report", Label: "Read report", Prompt: "Read the report", At: due.Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimDesktop(ctx, client, call.ID)
	if err != nil {
		t.Fatal(err)
	}
	receipt := DesktopReceipt{Token: claim.Token, Result: json.RawMessage(`{"saved":true}`)}
	if _, err := store.CompleteDesktop(ctx, client, call.ID, receipt); err != nil {
		t.Fatal(err)
	}
	grants, err := store.Reminders(ctx, client)
	if err != nil || len(grants) != 1 {
		t.Fatalf("grants: %+v %v", grants, err)
	}
	grant := grants[0]
	if _, err := store.QueueReminderOccurrence(ctx, client, grant.ID, "forged-version", due); err == nil {
		t.Fatal("forged grant version accepted")
	}
	fire, err := store.QueueReminderOccurrence(ctx, client, grant.ID, grant.Version, due)
	if err != nil {
		t.Fatal(err)
	}
	same, err := store.QueueReminderOccurrence(ctx, client, grant.ID, grant.Version, due)
	if err != nil || same.ID != fire.ID {
		t.Fatalf("duplicate occurrence: %+v %v", same, err)
	}
	if _, err := store.QueueReminderOccurrence(ctx, client, grant.ID, grant.Version, due.Add(time.Second)); err == nil {
		t.Fatal("unscheduled occurrence accepted")
	}
	admitted, ok, err := store.ClaimReminder(ctx, fire)
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if _, ok, err := store.ClaimReminder(ctx, fire); err != nil || ok {
		t.Fatalf("claimed twice: %v %v", ok, err)
	}
	reminderSource, err := store.ReminderRequest(ctx, admitted, fire)
	if err != nil || reminderSource.Kind != "reminder" || reminderSource.Text != source.Text || reminderSource.OriginalRequestID != source.ID {
		t.Fatalf("source provenance: %+v %v", reminderSource, err)
	}
	if err := store.BindRequest(ctx, reminderSource, Execution{InstanceID: "host-one", SessionID: "bot-chat", HandleID: "reminder", RunID: "run2", TurnID: "turn2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueDesktop(ctx, reminderSource, "recursive-save", "reminders", call.Arguments); err == nil {
		t.Fatal("reminder assignment authorized another schedule")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenWorkStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store.InstanceID = "host-two"
	pending, err := store.PendingReminders(ctx, "owner", id)
	if err != nil || len(pending) != 0 {
		t.Fatalf("unknown dispatch replayed after restart: %+v %v", pending, err)
	}
	if _, err := store.ActiveClient(ctx, "owner", id, client.ID); err == nil {
		t.Fatal("restart revived native connection")
	}
	client, err = store.ActivateClient(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	occurrences, err := store.ReminderOccurrences(ctx, client)
	if err != nil || len(occurrences) != 1 || occurrences[0].ID != fire.ID || occurrences[0].State != "claimed" {
		t.Fatalf("uncertain occurrence recovery: %+v %v", occurrences, err)
	}
}

func TestDesktopExitFencesClaimAndDoesNotRevokeNewActivation(t *testing.T) {
	ctx := t.Context()
	store, err := OpenWorkStore(filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.InstanceID = "host"
	id := Identity("owner", "bot")
	registration, err := store.RegisterClient(ctx, "owner", id, "enroll", []string{"clock"})
	if err != nil {
		t.Fatal(err)
	}
	client, err := store.ActivateClient(ctx, registration.Client)
	if err != nil {
		t.Fatal(err)
	}
	oldFire := ReminderFire{ID: "pending-before-exit", PrincipalID: "owner", BotID: id, ClientID: client.ID, Due: time.Now().Add(-time.Second), State: "pending"}
	if _, err := store.db.Put(ctx, "reminder_fire", oldFire.ID, oldFire); err != nil {
		t.Fatal(err)
	}
	if err := store.ExitClient(ctx, client); err != nil {
		t.Fatal(err)
	}
	next, err := store.ActivateClient(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ExitClient(ctx, client); err == nil {
		t.Fatal("stale exit revoked new activation")
	}
	if current, err := store.ActiveClient(ctx, "owner", id, next.ID); err != nil || current.ActivationID != next.ActivationID {
		t.Fatalf("new activation changed: %+v %v", current, err)
	}
	if _, claimed, err := store.ClaimReminder(ctx, oldFire); err != nil || claimed {
		t.Fatalf("explicit exit revived pending reminder: %v %v", claimed, err)
	}
	occurrences, err := store.ReminderOccurrences(ctx, next)
	if err != nil || len(occurrences) != 1 || occurrences[0].State != "suppressed" {
		t.Fatalf("stopped occurrence recovery: %+v %v", occurrences, err)
	}
}
