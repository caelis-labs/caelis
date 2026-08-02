package gatewayapp

import (
	"strings"
	"sync"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestGuardianConversationManagerCommitValidatedPairAndSnapshotClones(t *testing.T) {
	manager := newGuardianConversationManager()
	snapshot, err := manager.snapshot("main-1")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != 0 || len(snapshot.Events) != 0 {
		t.Fatalf("initial snapshot = %#v, want empty version zero", snapshot)
	}

	user := guardianConversationTestEvent(session.EventTypeUser, model.RoleUser, "prompt-1")
	assistant := guardianConversationTestEvent(session.EventTypeAssistant, model.RoleAssistant, "answer-1")
	committed, rebased, err := manager.commitValidated(guardianConversationCommit{
		SessionID:       " main-1 ",
		ExpectedVersion: snapshot.Version,
		ParentCursor:    guardianParentCanonicalCursor{EventID: "parent-10", EventSeq: 10},
		User:            user,
		Assistant:       assistant,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !committed || rebased {
		t.Fatalf("commit = (%v, %v), want (true, false)", committed, rebased)
	}

	user.Message.Parts[0].Text.Text = "mutated original"
	assistant.Meta["nested"].(map[string]any)["value"] = "mutated original"
	snapshot, err = manager.snapshot("main-1")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != 1 {
		t.Fatalf("version = %d, want 1", snapshot.Version)
	}
	if got := guardianConversationTestTexts(snapshot.Events); strings.Join(got, ",") != "prompt-1,answer-1" {
		t.Fatalf("stored texts = %v, want original pair", got)
	}
	if got := snapshot.Events[1].Meta["nested"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("stored nested metadata = %v, want original", got)
	}
	if snapshot.ParentCursor != (guardianParentCanonicalCursor{EventID: "parent-10", EventSeq: 10}) {
		t.Fatalf("parent cursor = %#v", snapshot.ParentCursor)
	}

	snapshot.Events[0].Message.Parts[0].Text.Text = "mutated snapshot"
	again, err := manager.snapshot("main-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := session.EventText(again.Events[0]); got != "prompt-1" {
		t.Fatalf("stored text after snapshot mutation = %q, want prompt-1", got)
	}
}

func TestGuardianConversationManagerVersionConflictDoesNotMutate(t *testing.T) {
	manager := newGuardianConversationManager()
	firstUser, firstAssistant := guardianConversationTestPair("first")
	committed, _, err := manager.commitValidated(guardianConversationCommit{
		SessionID:       "main-1",
		ExpectedVersion: 0,
		ParentCursor:    guardianParentCanonicalCursor{EventID: "parent-1", EventSeq: 1},
		User:            firstUser,
		Assistant:       firstAssistant,
	})
	if err != nil || !committed {
		t.Fatalf("first commit = (%v, %v)", committed, err)
	}

	staleUser, staleAssistant := guardianConversationTestPair("stale")
	committed, rebased, err := manager.commitValidated(guardianConversationCommit{
		SessionID:       "main-1",
		ExpectedVersion: 0,
		ParentCursor:    guardianParentCanonicalCursor{EventID: "parent-2", EventSeq: 2},
		User:            staleUser,
		Assistant:       staleAssistant,
	})
	if err != nil {
		t.Fatal(err)
	}
	if committed || rebased {
		t.Fatalf("stale commit = (%v, %v), want (false, false)", committed, rebased)
	}
	snapshot, err := manager.snapshot("main-1")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != 1 || strings.Join(guardianConversationTestTexts(snapshot.Events), ",") != "user-first,assistant-first" {
		t.Fatalf("snapshot after stale commit = %#v", snapshot)
	}
}

func TestGuardianConversationManagerRebasesAtomicallyOnCompactChange(t *testing.T) {
	manager := newGuardianConversationManager()
	user, assistant := guardianConversationTestPair("before")
	committed, _, err := manager.commitValidated(guardianConversationCommit{
		SessionID:       "main-1",
		ExpectedVersion: 0,
		ParentCursor:    guardianParentCanonicalCursor{EventID: "parent-10", EventSeq: 10},
		User:            user,
		Assistant:       assistant,
	})
	if err != nil || !committed {
		t.Fatalf("initial commit = (%v, %v)", committed, err)
	}

	compact := guardianParentCompactIdentity{
		EventID:              "compact-11",
		EventSeq:             11,
		SummarizedThroughID:  "parent-10",
		SummarizedThroughSeq: 10,
	}
	user, assistant = guardianConversationTestPair("rebased")
	committed, rebased, err := manager.commitValidated(guardianConversationCommit{
		SessionID:       "main-1",
		ExpectedVersion: 1,
		ParentCompact:   compact,
		ParentCursor:    guardianParentCanonicalCursor{EventID: "parent-12", EventSeq: 12},
		User:            user,
		Assistant:       assistant,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !committed || !rebased {
		t.Fatalf("compact commit = (%v, %v), want atomic rebase", committed, rebased)
	}
	snapshot, err := manager.snapshot("main-1")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != 2 || snapshot.ParentCompact != compact {
		t.Fatalf("rebased snapshot metadata = %#v", snapshot)
	}
	if got := strings.Join(guardianConversationTestTexts(snapshot.Events), ","); got != "user-rebased,assistant-rebased" {
		t.Fatalf("rebased events = %q", got)
	}

	user, assistant = guardianConversationTestPair("after")
	committed, rebased, err = manager.commitValidated(guardianConversationCommit{
		SessionID:       "main-1",
		ExpectedVersion: snapshot.Version,
		ParentCompact:   compact,
		ParentCursor:    guardianParentCanonicalCursor{EventID: "parent-13", EventSeq: 13},
		User:            user,
		Assistant:       assistant,
	})
	if err != nil || !committed || rebased {
		t.Fatalf("same compact commit = (%v, %v, %v)", committed, rebased, err)
	}
	snapshot, err = manager.snapshot("main-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(guardianConversationTestTexts(snapshot.Events), ","); got != "user-rebased,assistant-rebased,user-after,assistant-after" {
		t.Fatalf("events after same compact = %q", got)
	}
}

func TestGuardianConversationManagerValidatedContextReplacesEvents(t *testing.T) {
	manager := newGuardianConversationManager()
	oldUser, oldAssistant := guardianConversationTestPair("old")
	committed, _, err := manager.commitValidated(guardianConversationCommit{
		SessionID:       "main-1",
		ExpectedVersion: 0,
		ParentCursor:    guardianParentCanonicalCursor{EventID: "parent-1", EventSeq: 1},
		User:            oldUser,
		Assistant:       oldAssistant,
	})
	if err != nil || !committed {
		t.Fatalf("initial commit = (%v, %v)", committed, err)
	}

	checkpoint := &session.Event{
		Type:       session.EventTypeCompact,
		Visibility: session.VisibilityCanonical,
		Text:       "CONTEXT CHECKPOINT\nValidated prior decisions.",
	}
	currentUser, currentAssistant := guardianConversationTestPair("current")
	contextEvents := []*session.Event{checkpoint, currentUser, currentAssistant}
	committed, rebased, err := manager.commitValidated(guardianConversationCommit{
		SessionID:       "main-1",
		ExpectedVersion: 1,
		ParentCursor:    guardianParentCanonicalCursor{EventID: "parent-2", EventSeq: 2},
		User:            currentUser,
		Assistant:       currentAssistant,
		ContextEvents:   contextEvents,
	})
	if err != nil || !committed || rebased {
		t.Fatalf("context replacement = (%v, %v, %v)", committed, rebased, err)
	}
	checkpoint.Text = "mutated checkpoint"
	currentUser.Message.Parts[0].Text.Text = "mutated current user"

	snapshot, err := manager.snapshot("main-1")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != 2 || len(snapshot.Events) != 3 {
		t.Fatalf("replacement snapshot = %#v", snapshot)
	}
	if got := session.EventText(snapshot.Events[0]); got != "CONTEXT CHECKPOINT\nValidated prior decisions." {
		t.Fatalf("checkpoint = %q", got)
	}
	if got := strings.Join(guardianConversationTestTexts(snapshot.Events[1:]), ","); got != "user-current,assistant-current" {
		t.Fatalf("replacement pair = %q", got)
	}

	badUser, badAssistant := guardianConversationTestPair("bad")
	committed, rebased, err = manager.commitValidated(guardianConversationCommit{
		SessionID:       "main-1",
		ExpectedVersion: 2,
		ParentCursor:    guardianParentCanonicalCursor{EventID: "parent-3", EventSeq: 3},
		User:            badUser,
		Assistant:       badAssistant,
		ContextEvents:   []*session.Event{badUser},
	})
	if err == nil || committed || rebased {
		t.Fatalf("incomplete replacement = (%v, %v, %v), want rejected", committed, rebased, err)
	}
	afterRejected, snapshotErr := manager.snapshot("main-1")
	if snapshotErr != nil {
		t.Fatal(snapshotErr)
	}
	if afterRejected.Version != 2 || strings.Join(guardianConversationTestTexts(afterRejected.Events[1:]), ",") != "user-current,assistant-current" {
		t.Fatalf("rejected replacement mutated state: %#v", afterRejected)
	}
}

func TestGuardianConversationManagerRejectsInvalidPairAndParentRegression(t *testing.T) {
	manager := newGuardianConversationManager()
	validUser, validAssistant := guardianConversationTestPair("valid")
	invalidAssistantRole := guardianConversationTestEvent(session.EventTypeAssistant, model.RoleUser, "wrong role")
	uiOnlyUser := guardianConversationTestEvent(session.EventTypeUser, model.RoleUser, "ui only")
	uiOnlyUser.Visibility = session.VisibilityUIOnly

	for _, test := range []struct {
		name      string
		user      *session.Event
		assistant *session.Event
	}{
		{name: "nil user", assistant: validAssistant},
		{name: "wrong assistant role", user: validUser, assistant: invalidAssistantRole},
		{name: "transient user", user: uiOnlyUser, assistant: validAssistant},
		{name: "empty assistant", user: validUser, assistant: guardianConversationTestEvent(session.EventTypeAssistant, model.RoleAssistant, "")},
	} {
		t.Run(test.name, func(t *testing.T) {
			committed, rebased, err := manager.commitValidated(guardianConversationCommit{
				SessionID:       "invalid-" + test.name,
				ExpectedVersion: 0,
				User:            test.user,
				Assistant:       test.assistant,
			})
			if err == nil || committed || rebased {
				t.Fatalf("invalid commit = (%v, %v, %v), want rejected", committed, rebased, err)
			}
			snapshot, snapshotErr := manager.snapshot("invalid-" + test.name)
			if snapshotErr != nil {
				t.Fatal(snapshotErr)
			}
			if snapshot.Version != 0 || len(snapshot.Events) != 0 {
				t.Fatalf("invalid commit mutated snapshot: %#v", snapshot)
			}
		})
	}

	compact := guardianParentCompactIdentity{
		EventID:              "compact-5",
		EventSeq:             5,
		SummarizedThroughID:  "parent-4",
		SummarizedThroughSeq: 4,
	}
	committed, _, err := manager.commitValidated(guardianConversationCommit{
		SessionID:       "main-1",
		ExpectedVersion: 0,
		ParentCompact:   compact,
		ParentCursor:    guardianParentCanonicalCursor{EventID: "parent-6", EventSeq: 6},
		User:            validUser,
		Assistant:       validAssistant,
	})
	if err != nil || !committed {
		t.Fatalf("baseline commit = (%v, %v)", committed, err)
	}

	for _, test := range []struct {
		name    string
		compact guardianParentCompactIdentity
		cursor  guardianParentCanonicalCursor
	}{
		{name: "compact removed", cursor: guardianParentCanonicalCursor{EventID: "parent-7", EventSeq: 7}},
		{name: "compact seq reused with other ID", compact: guardianParentCompactIdentity{EventID: "other-compact", EventSeq: 5, SummarizedThroughSeq: 4}, cursor: guardianParentCanonicalCursor{EventID: "parent-7", EventSeq: 7}},
		{name: "cursor regressed", compact: compact, cursor: guardianParentCanonicalCursor{EventID: "compact-5", EventSeq: 5}},
		{name: "cursor ID changed at same seq", compact: compact, cursor: guardianParentCanonicalCursor{EventID: "other-parent-6", EventSeq: 6}},
	} {
		t.Run(test.name, func(t *testing.T) {
			user, assistant := guardianConversationTestPair(test.name)
			committed, rebased, err := manager.commitValidated(guardianConversationCommit{
				SessionID:       "main-1",
				ExpectedVersion: 1,
				ParentCompact:   test.compact,
				ParentCursor:    test.cursor,
				User:            user,
				Assistant:       assistant,
			})
			if err == nil || committed || rebased {
				t.Fatalf("regressed commit = (%v, %v, %v), want rejected", committed, rebased, err)
			}
			snapshot, snapshotErr := manager.snapshot("main-1")
			if snapshotErr != nil {
				t.Fatal(snapshotErr)
			}
			if snapshot.Version != 1 || strings.Join(guardianConversationTestTexts(snapshot.Events), ",") != "user-valid,assistant-valid" {
				t.Fatalf("regression mutated snapshot: %#v", snapshot)
			}
		})
	}
}

func TestGuardianConversationManagerSessionsAreIsolatedAndForgettable(t *testing.T) {
	manager := newGuardianConversationManager()
	for _, sessionID := range []string{"main-a", "main-b"} {
		user, assistant := guardianConversationTestPair(sessionID)
		committed, _, err := manager.commitValidated(guardianConversationCommit{
			SessionID:       sessionID,
			ExpectedVersion: 0,
			User:            user,
			Assistant:       assistant,
		})
		if err != nil || !committed {
			t.Fatalf("commit %s = (%v, %v)", sessionID, committed, err)
		}
	}
	manager.forget("main-a")

	a, err := manager.snapshot("main-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := manager.snapshot("main-b")
	if err != nil {
		t.Fatal(err)
	}
	if a.Version != 0 || len(a.Events) != 0 {
		t.Fatalf("forgotten session = %#v", a)
	}
	if b.Version != 1 || strings.Join(guardianConversationTestTexts(b.Events), ",") != "user-main-b,assistant-main-b" {
		t.Fatalf("remaining session = %#v", b)
	}
}

func TestGuardianApproverReleasesConversationWithMainSession(t *testing.T) {
	reviewer := newGuardianApprovalApprover(nil)
	user, assistant := guardianConversationTestPair("release")
	committed, _, err := reviewer.conversations.commitValidated(guardianConversationCommit{
		SessionID:       "main-release",
		ExpectedVersion: 0,
		User:            user,
		Assistant:       assistant,
	})
	if err != nil || !committed {
		t.Fatalf("commit = (%v, %v)", committed, err)
	}
	reviewer.ReleaseApprovalContext(session.SessionRef{SessionID: "main-release"})
	snapshot, err := reviewer.conversations.snapshot("main-release")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != 0 || len(snapshot.Events) != 0 {
		t.Fatalf("released Guardian context = %#v, want empty", snapshot)
	}
}

func TestGuardianConversationManagerConcurrentVersionedCommitHasOneWinner(t *testing.T) {
	manager := newGuardianConversationManager()
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	errs := make(chan error, 2)
	for _, suffix := range []string{"a", "b"} {
		suffix := suffix
		wg.Add(1)
		go func() {
			defer wg.Done()
			user, assistant := guardianConversationTestPair(suffix)
			committed, _, err := manager.commitValidated(guardianConversationCommit{
				SessionID:       "main-1",
				ExpectedVersion: 0,
				ParentCursor:    guardianParentCanonicalCursor{EventID: "parent-1-" + suffix, EventSeq: 1},
				User:            user,
				Assistant:       assistant,
			})
			results <- committed
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	winners := 0
	for committed := range results {
		if committed {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("successful commits = %d, want 1", winners)
	}
	snapshot, err := manager.snapshot("main-1")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != 1 || len(snapshot.Events) != 2 {
		t.Fatalf("winning snapshot = %#v", snapshot)
	}
}

func guardianConversationTestPair(suffix string) (*session.Event, *session.Event) {
	return guardianConversationTestEvent(session.EventTypeUser, model.RoleUser, "user-"+suffix),
		guardianConversationTestEvent(session.EventTypeAssistant, model.RoleAssistant, "assistant-"+suffix)
}

func guardianConversationTestEvent(eventType session.EventType, role model.Role, text string) *session.Event {
	message := model.NewTextMessage(role, text)
	return &session.Event{
		Type:       eventType,
		Visibility: session.VisibilityCanonical,
		Message:    &message,
		Text:       text,
		Meta: map[string]any{
			"nested": map[string]any{"value": "original"},
		},
	}
}

func guardianConversationTestTexts(events []*session.Event) []string {
	texts := make([]string, 0, len(events))
	for _, event := range events {
		texts = append(texts, session.EventText(event))
	}
	return texts
}
