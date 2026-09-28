package gatewayapp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
)

func TestApplicationReviewerRuntimeReleaseDrainsCancelledReview(t *testing.T) {
	ctx := t.Context()
	stack := newLocalStateTestHost(t, &runtimeMemoryHostStub{})
	owner := appserver.Principal{ID: stack.composition.authorities.userID}
	connection, err := stack.Applications().Register(ctx, owner, application.Registration{
		OperationID: "review-drain-enroll", Name: "review-drain", Credential: "app-client-" + strings.Repeat("d", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	principal := appserver.Principal{ID: owner.ID, ApplicationID: connection.ApplicationID, ConnectionID: connection.ConnectionID}
	selected := stack.composition.lookup.DefaultID()
	created, err := stack.Applications().Create(ctx, principal, appserver.CreateApplicationSessionRequest{
		WriteBase: appserver.WriteBase{OperationID: "review-drain-create"},
		Profile: application.Profile{Version: "v1", Model: selected, ToolsVersion: "v1", Execution: "tools-only",
			Permissions: application.Permissions{ApprovalMode: "auto-review"}, Reviewer: &application.Reviewer{Kind: "guardian", Model: selected}},
	})
	if err != nil || created.SessionID == "" {
		t.Fatal(created, err)
	}
	runtime := activateSessionRuntime(t, stack, created.SessionID)
	reviewer := runtime.instance.guardian
	if reviewer == nil || reviewer.queryTools {
		t.Fatal("Application runtime does not own its scoped Guardian")
	}
	active, err := stack.composition.sessions.Session(ctx, session.SessionRef{SessionID: created.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	llm := &guardianCleanupBarrierModel{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	// Always unblock the producer before test cleanup closes Host stores.
	defer func() {
		select {
		case <-llm.release:
		default:
			close(llm.release)
		}
	}()
	reviewCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := reviewer.Decide(reviewCtx, approvalReviewerTestRequest(active, llm, "inspect", nil))
		done <- err
	}()
	select {
	case <-llm.started:
	case err := <-done:
		t.Fatalf("review never started: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("review never started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("review did not settle before producer cleanup")
	}
	released := make(chan error, 1)
	go func() { released <- stack.sessionRuntimes.release(ctx, created.SessionID) }()
	select {
	case err := <-released:
		t.Fatalf("runtime release failed to drain reviewer: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(llm.release)
	select {
	case err := <-released:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runtime release failed after reviewer drained")
	}
	events, err := stack.composition.sessions.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if session.IsModelInvocationReceipt(event) {
			return
		}
	}
	t.Fatal("drained reviewer lost its late accounting receipt")
}
