package gatewayapp

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelprofile"
	controlplacement "github.com/caelis-labs/caelis/control/placement"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func TestDeleteModelsMixedProfilesPreservesConnectionAndSiblingSession(t *testing.T) {
	ctx := t.Context()
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	commands := stack.ConfigurationCommands()
	profiles, err := stack.connectTestModels([]ModelConfig{
		{Provider: "ollama", Model: "remote-model"},
		{Provider: "ollama", Model: "fallback"},
	})
	if err != nil || len(profiles) != 2 {
		t.Fatalf("provider models = %#v, %v", profiles, err)
	}
	provider, fallback := profiles[0], profiles[1]
	doc, err := stack.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	connection := testACPControllerConnection(t, stack, "shared")
	agents, acp := disconnectTestCatalog(connection, "shared", "remote-model", "sibling-model")
	selected, sibling := acp.Profiles[0].ID, acp.Profiles[1].ID
	doc.ExternalAgents = agents
	doc.ModelProfiles, err = modelprofile.Upsert(doc.ModelProfiles, acp.Profiles...)
	if err != nil {
		t.Fatal(err)
	}
	doc.ModelProfiles, err = modelprofile.SelectDefault(doc.ModelProfiles, fallback.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	doc.AgentBindings = agentbinding.Configuration{Bindings: []agentbinding.Binding{
		{Handle: agentbinding.HandleBreeze, ProfileID: provider.ID, Effort: "none"},
		{Handle: agentbinding.HandleOrbit, ProfileID: selected, Effort: "none"},
		{Handle: agentbinding.HandleZenith, ProfileID: sibling, Effort: "none"},
	}}
	if err := stack.composition.authorities.store.Save(doc); err != nil {
		t.Fatal(err)
	}
	doc, err = stack.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	setSessionModel := func(name, profileID string, load bool) session.Session {
		t.Helper()
		active, err := startGatewayAppTestSession(ctx, stack, name)
		if err != nil {
			t.Fatal(err)
		}
		if load {
			runtime, _, err := stack.sessionRuntimes.activateSession(ctx, active.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			release, err := stack.sessionRuntimes.acquireRuntimeUse(runtime)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(release)
			active = mustCurrentSession(t, stack, active.SessionID)
		}
		result, err := commands.UseSessionModel(ctx, principal, appserver.SessionModelRequest{
			WriteBase: appserver.WriteBase{OperationID: "select-" + name, SessionID: active.SessionID,
				ExpectedRevision: &active.Revision, ExpectedControllerEpoch: active.Controller.EpochID},
			Model: profileID,
		})
		if err != nil || result.Outcome != appserver.OutcomeCommitted {
			t.Fatalf("select %q = %#v, %v", profileID, result, err)
		}
		return mustCurrentSession(t, stack, active.SessionID)
	}
	providerSession := setSessionModel("provider", provider.ID, false)
	selectedSession := setSessionModel("selected-acp", selected, true)
	siblingSession, err := startGatewayAppTestSession(ctx, stack, "sibling-acp")
	if err != nil {
		t.Fatal(err)
	}
	placed, err := controlplacement.ResolveProfile(newPlacementSnapshot(doc).placement, sibling, "none")
	if err != nil {
		t.Fatal(err)
	}
	// The sibling stays dormant. The loaded ACP fixture advertises the selected
	// model, while this binding exercises preservation at the same Agent ID.
	siblingSession, err = stack.composition.sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef: siblingSession.SessionRef, Binding: dormantACPControllerBinding(placed, "fixture", time.Now()),
	})
	if err != nil {
		t.Fatal(err)
	}
	siblingSession = mustCurrentSession(t, stack, siblingSession.SessionID)
	appendApprovalReviewerTextEvent(t, ctx, stack.composition.sessions, selectedSession, session.EventTypeUser, model.RoleUser, "Retain this conversation after changing models.")
	beforeEvents, err := stack.composition.sessions.Events(ctx, session.EventsRequest{SessionRef: selectedSession.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	beforeContext := guardianReceiptParentModelContext(t, selectedSession, beforeEvents)

	revision, err := stack.ControlStatus().ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	request := appserver.DeleteModelsRequest{
		WriteBase:  appserver.WriteBase{OperationID: "mixed-model-removal", ExpectedRevision: &revision},
		ProfileIDs: []string{selected, provider.ID},
	}
	invalid := request
	invalid.OperationID = "invalid-model-removal"
	invalid.ProfileIDs = append(slices.Clone(request.ProfileIDs), "acp:shared:absent")
	if rejected, err := commands.DeleteModels(ctx, principal, invalid); err == nil || rejected.Outcome != appserver.OutcomeRejected || rejected.Revision != revision {
		t.Fatalf("invalid batch = %#v, %v", rejected, err)
	}
	result, err := commands.DeleteModels(ctx, principal, request)
	if err != nil || result.Outcome != appserver.OutcomeCommitted || result.Revision != revision+1 || result.Detail != "" {
		t.Fatalf("mixed batch = %#v, %v", result, err)
	}
	if recovered, err := commands.DeleteModels(ctx, principal, request); err != nil || !reflect.DeepEqual(recovered, result) {
		t.Fatalf("ledger recovery = %#v, %v; want %#v", recovered, err, result)
	}
	assertCatalog := func(removed ...string) {
		t.Helper()
		saved, err := stack.composition.authorities.store.LoadContext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(saved.ExternalAgents, doc.ExternalAgents) {
			t.Fatalf("saved ACP connection = %#v", saved.ExternalAgents)
		}
		for _, id := range removed {
			if _, present := modelprofile.Lookup(saved.ModelProfiles, id); present {
				t.Fatalf("removed profile %q persisted: %#v", id, saved.ModelProfiles)
			}
		}
		if _, present := modelprofile.Lookup(saved.ModelProfiles, fallback.ID); !present || saved.ModelProfiles.DefaultProfileID != fallback.ID {
			t.Fatalf("saved fallback = %#v", saved.ModelProfiles)
		}
	}
	assertCatalog(selected, provider.ID)
	saved, err := stack.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := modelprofile.Lookup(saved.ModelProfiles, sibling); !present {
		t.Fatalf("sibling model missing: %#v", saved.ModelProfiles)
	}
	if binding, ok := agentbinding.Lookup(saved.AgentBindings, agentbinding.HandleZenith); !ok || binding.ProfileID != sibling || len(saved.AgentBindings.Bindings) != 1 {
		t.Fatalf("saved bindings = %#v", saved.AgentBindings)
	}
	if after := mustCurrentSession(t, stack, siblingSession.SessionID); !reflect.DeepEqual(after, siblingSession) {
		t.Fatalf("sibling Session changed: %#v, want %#v", after, siblingSession)
	}
	if after := mustCurrentSession(t, stack, selectedSession.SessionID); after.Controller.Kind != session.ControllerKindKernel {
		t.Fatalf("selected ACP controller was not handed off: %#v", after.Controller)
	}
	for _, active := range []session.Session{selectedSession, providerSession} {
		state, err := stack.composition.sessions.SnapshotState(ctx, active.SessionRef)
		if err != nil || kernel.CurrentModelAlias(state) != fallback.Backend.Provider.ModelConfigID {
			t.Fatalf("Session %q fallback state is %#v, %v", active.SessionID, state, err)
		}
	}
	refreshed := mustCurrentSession(t, stack, selectedSession.SessionID)
	afterEvents, err := stack.composition.sessions.Events(ctx, session.EventsRequest{SessionRef: selectedSession.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt := guardianReceiptParentModelContext(t, refreshed, afterEvents); !reflect.DeepEqual(rebuilt, beforeContext) {
		t.Fatalf("model context after ACP handoff = %#v, want %#v", rebuilt, beforeContext)
	}
	last, err := commands.DeleteModels(ctx, principal, appserver.DeleteModelsRequest{
		WriteBase: appserver.WriteBase{OperationID: "last-acp-profile", ExpectedRevision: &result.Revision}, ProfileIDs: []string{sibling},
	})
	if err != nil || last.Outcome != appserver.OutcomeCommitted || last.Revision != result.Revision+1 || last.Detail != "" {
		t.Fatalf("last ACP profile removal = %#v, %v", last, err)
	}
	assertCatalog(selected, sibling, provider.ID)
}
