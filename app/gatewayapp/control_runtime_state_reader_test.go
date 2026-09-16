package gatewayapp

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestDormantSessionRuntimeStateRecoversCanonicalOutcomeWithoutActivation(t *testing.T) {
	ctx := t.Context()
	storeDir, workspace := t.TempDir(), t.TempDir()
	stack, err := newGatewayAppTestStack(t, Config{StoreDir: storeDir, WorkspaceCWD: workspace})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		terminal session.ExecutionStatus
		want     string
		ref      session.SessionRef
	}{
		{name: "no-run"},
		{name: "startup-cancelled", terminal: session.ExecutionCancelled, want: "interrupted"},
		{name: "completed", terminal: session.ExecutionSucceeded, want: "completed"},
		{name: "failed", terminal: session.ExecutionFailed, want: "failed"},
		{name: "crash-started", terminal: session.ExecutionStarted, want: eventstream.LifecycleStateUnknown},
		{name: "crash-cancel-requested", terminal: session.ExecutionCancelRequested, want: eventstream.LifecycleStateUnknown},
		{name: "crash-waiting-approval", terminal: session.ExecutionWaitingApproval, want: eventstream.LifecycleStateUnknown},
	}
	for i := range tests {
		test := &tests[i]
		active, err := startGatewayAppTestSession(ctx, stack, test.name)
		if err != nil {
			t.Fatal(err)
		}
		test.ref = active.SessionRef
		if test.terminal == "" {
			continue
		}
		statuses := []session.ExecutionStatus{session.ExecutionPrepared, session.ExecutionStarted}
		if test.terminal != session.ExecutionStarted {
			statuses = append(statuses, test.terminal)
		}
		for index, status := range statuses {
			now := time.Now().UTC()
			record := session.NormalizeExecutionRecord(session.ExecutionRecord{
				Schema: session.ExecutionJournalSchemaVersion, Kind: session.JournalKindRun,
				SessionID: active.SessionID, RunID: "run-" + test.name,
				Revision: uint64(index + 1), Status: status, CreatedAt: now, UpdatedAt: now,
			})
			_, err := stack.composition.sessions.AppendEvent(ctx, session.AppendEventRequest{
				SessionRef: active.SessionRef, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest),
				Event: &session.Event{
					IdempotencyKey: fmt.Sprintf("test-run:%s:%d", record.RunID, record.Revision),
					Type:           session.EventTypeLifecycle, Visibility: session.VisibilityJournal, Time: now,
					Actor:     session.ActorRef{Kind: session.ActorKindSystem, ID: "runtime"},
					Lifecycle: &session.EventLifecycle{Status: string(status)},
					Journal:   &session.ExecutionJournalEntry{Schema: session.ExecutionJournalSchemaVersion, Kind: session.JournalKindRun, Execution: &record},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := newGatewayAppTestStack(t, Config{StoreDir: storeDir, WorkspaceCWD: workspace})
	if err != nil {
		t.Fatal(err)
	}
	client, err := appserver.BindSessionClient(restarted.ControlClient(), appserver.Principal{ID: restarted.composition.authorities.userID})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before, err := restarted.composition.sessions.Session(ctx, test.ref)
			if err != nil {
				t.Fatal(err)
			}
			eventsBefore, err := restarted.composition.sessions.Events(ctx, session.EventsRequest{SessionRef: test.ref, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			state, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: test.ref.SessionID})
			if err != nil {
				t.Fatal(err)
			}
			if state.Run.Status != test.want || state.Run.Active || state.Run.WaitingApproval || state.Approval.Active != nil {
				t.Fatalf("dormant state = %#v, approval = %#v; want %q with no live execution", state.Run, state.Approval, test.want)
			}
			if test.terminal != "" && state.Run.RunID != "run-"+test.name {
				t.Fatalf("canonical run identity lost: %#v", state.Run)
			}
			after, err := restarted.composition.sessions.Session(ctx, test.ref)
			if err != nil {
				t.Fatal(err)
			}
			eventsAfter, err := restarted.composition.sessions.Events(ctx, session.EventsRequest{SessionRef: test.ref, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(eventsBefore, eventsAfter) {
				t.Fatal("state observation changed canonical Session or execution history")
			}
			if _, loaded := restarted.sessionRuntimes.loaded(test.ref.SessionID); loaded {
				t.Fatal("state observation activated a Session Runtime")
			}
		})
	}
}
