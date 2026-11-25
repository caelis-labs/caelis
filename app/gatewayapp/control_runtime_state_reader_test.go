package gatewayapp

import (
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
)

func TestDormantSessionRuntimeStateHasNoLiveHandleWithoutActivation(t *testing.T) {
	ctx := t.Context()
	storeDir, workspace := t.TempDir(), t.TempDir()
	stack, err := newGatewayAppTestStack(t, Config{StoreDir: storeDir, WorkspaceCWD: workspace})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		terminal string
		want     string
		ref      session.SessionRef
	}{
		{name: "no-run"},
		{name: "startup-cancelled", terminal: "cancelled", want: ""},
		{name: "completed", terminal: "succeeded", want: ""},
		{name: "failed", terminal: "failed", want: ""},
		{name: "crash-started", terminal: "started", want: ""},
		{name: "crash-cancel-requested", terminal: "cancel_requested", want: ""},
		{name: "crash-waiting-approval", terminal: "waiting_approval", want: ""},
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
		_, err = stack.composition.sessions.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeLifecycle), Event: &session.Event{Type: session.EventTypeLifecycle, Visibility: session.VisibilityJournal, Lifecycle: &session.EventLifecycle{Status: test.terminal}}})
		if err != nil {
			t.Fatal(err)
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
