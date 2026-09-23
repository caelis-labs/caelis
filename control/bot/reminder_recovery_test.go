package bot

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func reminderTestClient(t *testing.T) (*WorkStore, Client, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "control.sqlite")
	store, err := OpenWorkStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.InstanceID = "host-one"
	registration, err := store.RegisterClient(t.Context(), "owner", Identity("owner", "bot"), "enroll", []string{"reminders"})
	if err != nil {
		t.Fatal(err)
	}
	client, err := store.ActivateClient(t.Context(), registration.Client)
	if err != nil {
		t.Fatal(err)
	}
	var credential clientCredential
	if err := store.db.Get(t.Context(), "client", client.ID, &credential); err != nil {
		t.Fatal(err)
	}
	credential.Client.ActivatedAt = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.db.Replace(t.Context(), "client", client.ID, credential); err != nil {
		t.Fatal(err)
	}
	return store, credential.Client, path
}

func queueTestReminder(t *testing.T, store *WorkStore, client Client, requestID string, args DesktopArguments) DesktopCall {
	t.Helper()
	source, err := store.RecordRequest(t.Context(), client.PrincipalID, client.BotID, client.ID, requestID, requestID, "Manage my reminder.")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindRequest(t.Context(), source, Execution{InstanceID: store.InstanceID, SessionID: "bot-chat", HandleID: requestID, RunID: requestID, TurnID: requestID}); err != nil {
		t.Fatal(err)
	}
	call, err := store.QueueDesktop(t.Context(), source, requestID, "reminders", args)
	if err != nil {
		t.Fatal(err)
	}
	return call
}

func completeTestReminder(t *testing.T, store *WorkStore, client Client, call DesktopCall) {
	t.Helper()
	claim, err := store.ClaimDesktop(t.Context(), client, call.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteDesktop(t.Context(), client, call.ID, DesktopReceipt{Token: claim.Token, Result: json.RawMessage(`{"ok":true}`)}); err != nil {
		t.Fatal(err)
	}
}

func dailyReminderArguments() DesktopArguments {
	return DesktopArguments{Operation: "save", ID: "daily", Label: "Daily", Prompt: "Read the report.", Daily: "09:00", TimeZone: "Asia/Shanghai"}
}

func TestReminderQueuedChangeReleasedOnActivationReplacement(t *testing.T) {
	for _, transition := range []string{"exit", "host-restart"} {
		t.Run(transition, func(t *testing.T) {
			store, client, _ := reminderTestClient(t)
			call := queueTestReminder(t, store, client, "old-request", dailyReminderArguments())
			if transition == "exit" {
				if err := store.ExitClient(t.Context(), client); err != nil {
					t.Fatal(err)
				}
				var stopped desktopRecord
				if err := store.db.Get(t.Context(), "desktop", call.ID, &stopped); err != nil || stopped.Call.State != "suppressed" {
					t.Fatalf("exit left an unclaimed action pending: %+v %v", stopped, err)
				}
			} else {
				store.InstanceID = "host-two"
			}
			next, err := store.ActivateClient(t.Context(), client)
			if err != nil {
				t.Fatal(err)
			}
			late := call
			late.ID = "late-old-activation"
			late.Arguments.ID = "late"
			if queued, err := store.queueDesktop(t.Context(), client, late); err != nil || queued {
				t.Fatalf("late old-activation insertion succeeded: %v %v", queued, err)
			}
			// A new real request may change the same native reminder after the
			// old unclaimed activation has lost its authority.
			replacement := queueTestReminder(t, store, next, "new-request", DesktopArguments{Operation: "remove", ID: "daily"})
			if old, err := store.GetDesktopCall(t.Context(), next, call.ID); err != nil || old.State != "suppressed" {
				t.Fatalf("unclaimed action was not suppressed: %+v %v", old, err)
			}
			if _, err := store.ClaimDesktop(t.Context(), next, call.ID); err == nil {
				t.Fatal("old action can still dispatch")
			}
			completeTestReminder(t, store, next, replacement)
		})
	}
}

func TestReminderRevocationFencesCachedClaim(t *testing.T) {
	for _, operation := range []string{"remove", "save"} {
		t.Run(operation, func(t *testing.T) {
			store, client, _ := reminderTestClient(t)
			completeTestReminder(t, store, client, queueTestReminder(t, store, client, "save-original", dailyReminderArguments()))
			grants, err := store.Reminders(t.Context(), client)
			if err != nil || len(grants) != 1 {
				t.Fatalf("grants: %+v %v", grants, err)
			}
			grant := grants[0]
			due := time.Date(2025, 9, 22, 1, 0, 0, 0, time.UTC)
			fire, err := store.queueReminderOccurrence(t.Context(), client, grant.ID, grant.Version, due, due)
			if err != nil {
				t.Fatal(err)
			}
			args := dailyReminderArguments()
			if operation == "remove" {
				args = DesktopArguments{Operation: "remove", ID: "daily"}
			}
			// Commit the native mutation after the claimant has cached grant v1,
			// before it executes the dispatch CAS. This ordering needs no sleeps.
			completeTestReminder(t, store, client, queueTestReminder(t, store, client, "replace-original", args))
			if claimed, err := store.claimReminder(t.Context(), client, fire, grant); err != nil || claimed {
				t.Fatalf("revoked cached grant authorized dispatch: %v %v", claimed, err)
			}
			if _, err := store.ReminderRequest(t.Context(), grant, fire); err == nil {
				t.Fatal("revoked occurrence created an authorized source")
			}
		})
	}
}

func TestReminderDailyCatchUpPreservesNextScheduledDay(t *testing.T) {
	store, client, path := reminderTestClient(t)
	completeTestReminder(t, store, client, queueTestReminder(t, store, client, "save-daily", dailyReminderArguments()))
	grants, err := store.Reminders(t.Context(), client)
	if err != nil || len(grants) != 1 {
		t.Fatalf("grants: %+v %v", grants, err)
	}
	grant := grants[0]
	// Shanghai: yesterday 09:00 is caught up today at 08:00. Today's
	// scheduled 09:00 is still in the future and must remain eligible.
	yesterday := time.Date(2025, 9, 22, 1, 0, 0, 0, time.UTC)
	today := yesterday.AddDate(0, 0, 1)
	if _, err := store.queueReminderOccurrence(t.Context(), client, grant.ID, grant.Version, yesterday, today.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenWorkStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.InstanceID = client.InstanceID
	grants, err = store.Reminders(t.Context(), client)
	if err != nil || len(grants) != 1 || !grants[0].LastOccurrence.Equal(yesterday) || !grants[0].CoalescedThrough.Equal(today.Add(-time.Hour)) {
		t.Fatalf("scheduled time and catch-up cutoff did not survive reopening: %+v %v", grants, err)
	}
	if _, err := store.queueReminderOccurrence(t.Context(), client, grant.ID, grant.Version, today, today); err != nil {
		t.Fatalf("catch-up swallowed the next daily occurrence: %v", err)
	}
}

func TestReminderClaimedChangeSurvivesActivationReplacement(t *testing.T) {
	store, client, path := reminderTestClient(t)
	call := queueTestReminder(t, store, client, "old-request", dailyReminderArguments())
	claim, err := store.ClaimDesktop(t.Context(), client, call.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ExitClient(t.Context(), client); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenWorkStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.InstanceID = "host-two"
	client, err = store.ActivateClient(t.Context(), client)
	if err != nil {
		t.Fatal(err)
	}
	if old, err := store.GetDesktopCall(t.Context(), client, call.ID); err != nil || old.State != "claimed" {
		t.Fatalf("unknown claim was discarded: %+v %v", old, err)
	}
	if _, err := store.CompleteDesktop(t.Context(), client, call.ID, DesktopReceipt{Token: claim.Token, Result: json.RawMessage(`{"ok":true}`)}); err == nil {
		t.Fatal("stale activation receipt accepted")
	}
	source, err := store.RecordRequest(t.Context(), client.PrincipalID, client.BotID, client.ID, "new-request", "new", "Replace my reminder.")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindRequest(t.Context(), source, Execution{InstanceID: store.InstanceID, SessionID: "bot-chat", HandleID: "new", RunID: "new", TurnID: "new"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueDesktop(t.Context(), source, "new", "reminders", dailyReminderArguments()); err == nil {
		t.Fatal("new activation bypassed an unknown reminder mutation")
	}
}

func TestReminderActivationSuppressionIsAtomicAndScoped(t *testing.T) {
	store, client, _ := reminderTestClient(t)
	call := queueTestReminder(t, store, client, "request", dailyReminderArguments())
	renewed, err := store.ActivateClient(t.Context(), client)
	if err != nil || renewed.ActivationID != client.ActivationID {
		t.Fatalf("renew: %+v %v", renewed, err)
	}
	if calls, err := store.DesktopCalls(t.Context(), renewed); err != nil || len(calls) != 1 || calls[0].State != "queued" {
		t.Fatalf("renewal suppressed pending work: %+v %v", calls, err)
	}
	registration, err := store.RegisterClient(t.Context(), client.PrincipalID, client.BotID, "another-client", []string{"reminders"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.ActivateClient(t.Context(), registration.Client)
	if err != nil {
		t.Fatal(err)
	}
	otherCall := queueTestReminder(t, store, other, "other-request", dailyReminderArguments())
	// Fail the second write to prove the activation and suppression commit
	// together. A failed suppression must not leave an inactive owner behind.
	if _, err := store.db.db.Exec(`CREATE TRIGGER reject_suppression BEFORE UPDATE ON bot_authority
 WHEN NEW.kind='desktop' AND json_extract(NEW.body,'$.call.state')='suppressed'
 BEGIN SELECT RAISE(ABORT,'test suppression failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.ExitClient(t.Context(), renewed); err == nil {
		t.Fatal("exit ignored suppression failure")
	}
	if _, err := store.ActiveClient(t.Context(), client.PrincipalID, client.BotID, client.ID); err != nil {
		t.Fatalf("failed exit changed the active lease: %v", err)
	}
	if _, err := store.db.db.Exec(`DROP TRIGGER reject_suppression`); err != nil {
		t.Fatal(err)
	}
	changed := store.DesktopChanged()
	if err := store.ExitClient(t.Context(), renewed); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	default:
		t.Fatal("suppression did not wake the waiting tool")
	}
	if result, err := store.AwaitDesktop(t.Context(), call.ID); err != nil || result.State != "suppressed" {
		t.Fatalf("suppressed action did not settle: %+v %v", result, err)
	}
	completeTestReminder(t, store, other, otherCall)
}

func TestReminderClaimBeforeRevocationRetainsAdmission(t *testing.T) {
	store, client, _ := reminderTestClient(t)
	completeTestReminder(t, store, client, queueTestReminder(t, store, client, "save", dailyReminderArguments()))
	grants, err := store.Reminders(t.Context(), client)
	if err != nil || len(grants) != 1 {
		t.Fatalf("grants: %+v %v", grants, err)
	}
	grant := grants[0]
	due := time.Date(2025, 9, 22, 1, 0, 0, 0, time.UTC)
	fire, err := store.queueReminderOccurrence(t.Context(), client, grant.ID, grant.Version, due, due)
	if err != nil {
		t.Fatal(err)
	}
	admitted, ok, err := store.ClaimReminder(t.Context(), fire)
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	completeTestReminder(t, store, client, queueTestReminder(t, store, client, "remove", DesktopArguments{Operation: "remove", ID: "daily"}))
	if _, err := store.ReminderRequest(t.Context(), admitted, fire); err != nil {
		t.Fatalf("revocation retroactively cancelled admitted execution: %v", err)
	}
}

func TestReminderCatchUpCoalescesBacklog(t *testing.T) {
	for _, schedule := range []string{"daily", "interval"} {
		t.Run(schedule, func(t *testing.T) {
			store, client, _ := reminderTestClient(t)
			args := dailyReminderArguments()
			step := 24 * time.Hour
			if schedule == "interval" {
				args.Daily, args.TimeZone, args.EveryMinutes = "", "", 60
				step = time.Hour
			}
			completeTestReminder(t, store, client, queueTestReminder(t, store, client, "save", args))
			grants, err := store.Reminders(t.Context(), client)
			if err != nil || len(grants) != 1 {
				t.Fatalf("grants: %+v %v", grants, err)
			}
			grant := grants[0]
			due := time.Date(2025, 9, 20, 1, 0, 0, 0, time.UTC)
			grant.CreatedAt = due.Add(-step)
			if err := store.db.Replace(t.Context(), "reminder", grant.ID, grant); err != nil {
				t.Fatal(err)
			}
			now := due.Add(2*step + time.Minute)
			first, err := store.queueReminderOccurrence(t.Context(), client, grant.ID, grant.Version, due, now)
			if err != nil {
				t.Fatal(err)
			}
			for _, backlog := range []time.Time{due.Add(step), due.Add(2 * step)} {
				if _, err := store.queueReminderOccurrence(t.Context(), client, grant.ID, grant.Version, backlog, now); err == nil {
					t.Fatal("catch-up admitted another historical occurrence")
				}
			}
			if duplicate, err := store.queueReminderOccurrence(t.Context(), client, grant.ID, grant.Version, due, now); err != nil || duplicate.ID != first.ID {
				t.Fatalf("catch-up lost the original receipt: %+v %v", duplicate, err)
			}
			next := due.Add(3 * step)
			if schedule == "interval" {
				next = now.Add(step)
			}
			if _, err := store.queueReminderOccurrence(t.Context(), client, grant.ID, grant.Version, next, next); err != nil {
				t.Fatalf("catch-up blocked a future occurrence: %v", err)
			}
		})
	}
}

func TestReminderDailyRepeatedHourIsConsumedOnce(t *testing.T) {
	store, client, _ := reminderTestClient(t)
	args := dailyReminderArguments()
	args.Daily, args.TimeZone = "01:30", "America/New_York"
	completeTestReminder(t, store, client, queueTestReminder(t, store, client, "save", args))
	grants, err := store.Reminders(t.Context(), client)
	if err != nil || len(grants) != 1 {
		t.Fatalf("grants: %+v %v", grants, err)
	}
	grant := grants[0]
	first := time.Date(2025, 11, 2, 5, 30, 0, 0, time.UTC)
	if _, err := store.queueReminderOccurrence(t.Context(), client, grant.ID, grant.Version, first, first); err != nil {
		t.Fatal(err)
	}
	repeated := first.Add(time.Hour)
	if _, err := store.queueReminderOccurrence(t.Context(), client, grant.ID, grant.Version, repeated, repeated); err == nil {
		t.Fatal("repeated local time admitted a second daily occurrence")
	}
	tomorrow := repeated.AddDate(0, 0, 1)
	if _, err := store.queueReminderOccurrence(t.Context(), client, grant.ID, grant.Version, tomorrow, tomorrow); err != nil {
		t.Fatalf("repeated-hour deduplication blocked tomorrow: %v", err)
	}
}
