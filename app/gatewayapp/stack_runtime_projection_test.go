package gatewayapp

import (
	"context"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelprofile"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

func TestNilStackRuntimeProjectionPreservesUnavailableResults(t *testing.T) {
	t.Parallel()

	var stack *Stack
	if _, err := stack.ControlStatus().Doctor(context.Background(), DoctorRequest{}); err == nil || !strings.Contains(err.Error(), "stack is unavailable") {
		t.Fatalf("Doctor() error = %v, want unavailable Stack", err)
	}
	if status, found, err := stack.Agents().ControllerStatus(context.Background(), session.SessionRef{}); err != nil || found {
		t.Fatalf("ACPControllerStatus() = %#v, %v, %v, want zero, false, nil", status, found, err)
	}
	if _, err := stack.Models().ListChoices(context.Background(), session.SessionRef{}); err == nil || !strings.Contains(err.Error(), "stack is unavailable") {
		t.Fatalf("ListModelChoices() error = %v, want unavailable Stack", err)
	}
	if turns := stack.ControlKernelReads().TurnState(); turns != nil {
		t.Fatalf("ControlKernelReads().TurnState() = %#v, want nil", turns)
	}
}

func TestModelChoicesUnifyProviderAndACPProfiles(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	persistDisconnectTestAgent(t, stack, "codex")
	stack.composition.invalidateOwnPlacementSnapshot()

	choices, err := stack.Models().ListChoices(context.Background(), session.SessionRef{})
	if err != nil {
		t.Fatal(err)
	}
	foundProvider := false
	foundACP := false
	for _, choice := range choices {
		switch choice.Backend {
		case "provider":
			foundProvider = foundProvider || strings.HasPrefix(choice.ProfileID, "provider:")
		case "acp":
			foundACP = foundACP || choice.ID == "acp:codex:default" && choice.ProfileID == choice.ID && choice.Provider == "codex" && choice.Model == "default"
		}
	}
	if !foundProvider || !foundACP {
		t.Fatalf("ListModelChoices() = %#v, want provider and ACP profile choices", choices)
	}
}

func TestInitialSessionControllerFreezesACPDefaultProfile(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	persistDisconnectTestAgent(t, stack, "codex")
	doc, err := stack.composition.authorities.store.LoadContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	doc.ModelProfiles, err = modelprofile.SelectDefault(doc.ModelProfiles, "acp:codex:default", "none")
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.composition.authorities.store.Save(doc); err != nil {
		t.Fatal(err)
	}
	stack.composition.invalidateOwnPlacementSnapshot()
	stack.composition.setRuntimeDefaultProfile(doc.ModelProfiles)

	binding, err := stack.composition.initialSessionController(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if binding.Kind != session.ControllerKindACP || binding.AgentName != "codex" ||
		binding.Placement.ProfileID != "acp:codex:default" || binding.Placement.Fingerprint == "" {
		t.Fatalf("initial controller = %#v, want frozen ACP default", binding)
	}
}

func TestACPIngressCreatesKernelControllerAtomicallyUnderACPDefault(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	persistDisconnectTestAgent(t, stack, "codex")
	ctx := context.Background()
	doc, err := stack.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	doc.ModelProfiles, err = modelprofile.SelectDefault(doc.ModelProfiles, "acp:codex:default", "none")
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.composition.authorities.store.Save(doc); err != nil {
		t.Fatal(err)
	}
	stack.composition.invalidateOwnPlacementSnapshot()
	stack.composition.setRuntimeDefaultProfile(doc.ModelProfiles)

	result, err := stack.ControlClient().CreateSession(ctx, appserver.Principal{
		ID: stack.UserID(), Roles: []string{appserver.RoleACPIngress},
	}, appserver.CreateSessionRequest{
		WriteBase:          appserver.WriteBase{OperationID: "create-acp-ingress-local"},
		PreferredSessionID: "acp-ingress-local",
		WorkspaceKey:       stack.Workspace().Key,
		CWD:                stack.Workspace().CWD,
		Metadata:           map[string]any{"kept": "value"},
	})
	if err != nil || result.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("CreateSession(ACP ingress) = %#v, %v", result, err)
	}
	created, err := stack.composition.sessions.Session(ctx, session.SessionRef{SessionID: result.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if created.Controller.Kind != session.ControllerKindKernel || created.Controller.Source != "acp_ingress" {
		t.Fatalf("created controller = %#v, want atomic local kernel owner", created.Controller)
	}
	if created.Metadata["kept"] != "value" {
		t.Fatalf("durable metadata = %#v, want kept value without controller authority", created.Metadata)
	}

	forged, err := stack.ControlClient().CreateSession(ctx, appserver.Principal{ID: stack.UserID()}, appserver.CreateSessionRequest{
		WriteBase:          appserver.WriteBase{OperationID: "create-forged-acp-ingress"},
		PreferredSessionID: "forged-acp-ingress",
		WorkspaceKey:       stack.Workspace().Key,
		CWD:                stack.Workspace().CWD,
		Metadata:           map[string]any{"caelis.internal.session.create.local_controller": true},
	})
	if err != nil || forged.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("CreateSession(forged ACP ingress) = %#v, %v", forged, err)
	}
	forgedSession, err := stack.composition.sessions.Session(ctx, session.SessionRef{SessionID: forged.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if forgedSession.Controller.Kind != session.ControllerKindACP {
		t.Fatalf("forged controller = %#v, want configured ACP default", forgedSession.Controller)
	}
}

func TestStackStartsWithACPDefaultAndNoProviderProfile(t *testing.T) {
	storeDir := t.TempDir()
	store := newAppConfigStore(storeDir)
	doc, err := store.LoadContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	agents, profiles := disconnectTestCatalog(controlagents.Connection{
		ID: "codex", Launcher: controlagents.Launcher{Command: writeExternalAgentExecutable(t, t.TempDir(), "codex-acp")},
	}, "codex", "main")
	profiles, err = modelprofile.SelectDefault(profiles, "acp:codex:main", "none")
	if err != nil {
		t.Fatal(err)
	}
	doc.ExternalAgents = agents
	doc.ModelProfiles = profiles
	if err := store.Save(doc); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	stack, err := newGatewayAppTestStack(t, Config{
		AppName: "caelis", UserID: "acp-only", StoreDir: storeDir,
		WorkspaceKey: workspace, WorkspaceCWD: workspace,
	})
	if err != nil {
		t.Fatalf("NewLocalStack(ACP-only default) error = %v", err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	if got := stack.composition.EffectiveModelAlias(); got != "codex main" {
		t.Fatalf("EffectiveModelAlias() = %q, want ACP profile display", got)
	}
	binding, err := stack.composition.initialSessionController(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if binding.Kind != session.ControllerKindACP || binding.Placement.ProfileID != "acp:codex:main" {
		t.Fatalf("initial ACP-only controller = %#v", binding)
	}
}
