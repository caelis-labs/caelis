package gatewayapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func TestWorkerCreationSelectsProviderFromACPHostDefault(t *testing.T) {
	ctx := t.Context()
	stack := newLocalStateTestHost(t, nil)
	provider := stack.composition.lookup.DefaultID()
	persistDisconnectTestAgent(t, stack, "codex")
	doc, err := stack.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	doc.ModelProfiles, err = modelprofile.SelectDefault(doc.ModelProfiles, "acp:codex:default", "none")
	if err != nil {
		t.Fatal(err)
	}
	if err = stack.composition.authorities.store.Save(doc); err != nil {
		t.Fatal(err)
	}
	stack.composition.invalidateOwnPlacementSnapshot()
	stack.composition.setRuntimeDefaultProfile(doc.ModelProfiles)
	initial, err := stack.composition.initialSessionController(ctx)
	if err != nil || initial.Kind != session.ControllerKindACP || initial.EpochID == "" {
		t.Fatalf("Host default controller = %+v, %v", initial, err)
	}
	owner := appserver.Principal{ID: stack.composition.authorities.userID}
	conn, err := stack.Applications().Register(ctx, owner, application.Registration{OperationID: "enroll", Name: "worker-test", Credential: "app-client-" + strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	p := appserver.Principal{ID: owner.ID, ApplicationID: conn.ApplicationID, ConnectionID: conn.ConnectionID}
	req := appserver.CreateWorkerRequest{WriteBase: appserver.WriteBase{OperationID: "worker"}, CWD: stack.composition.workspace.CWD, Model: provider}
	created, err := stack.Applications().CreateWorker(ctx, p, req)
	if err != nil || created.Outcome != appserver.OutcomeCommitted || created.SessionID == "" {
		t.Fatalf("create Worker = %+v, %v", created, err)
	}
	active := mustCurrentSession(t, stack, created.SessionID)
	if active.Controller.Kind != session.ControllerKindKernel || active.Controller.EpochID == initial.EpochID || active.Revision != created.Revision {
		t.Fatalf("Worker controller = %+v, revision = %d; receipt = %+v", active.Controller, active.Revision, created)
	}
	state, err := stack.composition.sessions.SnapshotState(ctx, active.SessionRef)
	if err != nil || kernel.CurrentModelAlias(state) != provider {
		t.Fatalf("Worker model = %q, %v; want %q", kernel.CurrentModelAlias(state), err, provider)
	}
	repeated, err := stack.Applications().CreateWorker(ctx, p, req)
	if err != nil || repeated != created {
		t.Fatalf("repeated Worker = %+v, %v; want %+v", repeated, err, created)
	}
	if current := mustCurrentSession(t, stack, created.SessionID); current.Revision != created.Revision {
		t.Fatalf("retry changed Worker revision: %d", current.Revision)
	}
}
