package gatewayapp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/collaboration"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/surfaces/headless"
)

// RunLiveControllerCollaborationTest receives the production AppServer assembly
// from the external test package, which also dispatches real CLI MCP children.
func RunLiveControllerCollaborationTest(t *testing.T, assemble func(*Stack) (appserver.AppServerServices, error)) {
	if os.Getenv("CAELIS_CODEX_COLLABORATION_E2E") != "1" {
		t.Skip("set CAELIS_CODEX_COLLABORATION_E2E=1 for real controller and child Codex turns")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	home := t.TempDir()
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	auth, err := os.ReadFile(filepath.Join(userHome, ".codex", "auth.json"))
	if err != nil {
		t.Fatal("Codex authentication unavailable")
	}
	if err = os.WriteFile(filepath.Join(home, "auth.json"), auth, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", home)
	host := newHostedChildInputTestStack(t, newHostedChildInputTestProvider(t, false), "manual")
	services, err := assemble(host)
	if err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(t.TempDir(), "child.token")
	token, err := controlserver.LoadOrCreateBearerToken(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := controlserver.BearerTokenAuthenticator(token, appserver.Principal{ID: "owner", Roles: []string{appserver.RoleACPIngress}})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := controlserver.New(controlserver.HandlerConfig{Services: services, AdapterHost: host.AdapterHost(), Authenticator: authenticator, AllowedHosts: []string{"127.0.0.1"}, ServerInfo: appserver.ServerInfo{Capabilities: appserver.RequiredManagedHostCapabilities()}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer func() { _ = host.Close(); server.Close() }()
	host.SetBuiltInChildControl(server.URL, tokenFile)
	doc, err := host.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	nativeProfile := doc.ModelProfiles.DefaultProfileID
	modelID := os.Getenv("CAELIS_CODEX_TIER_MODEL")
	if modelID == "" {
		modelID = "gpt-6-luna"
	}
	connection := agents.Connection{ID: "codex", Name: "Codex", Launcher: agents.Launcher{Kind: agents.LaunchKindHostedAdapter, AdapterID: "codex"}}
	external, profiles := disconnectTestCatalog(connection, "codex", modelID)
	p := profiles.Profiles[0]
	p.Speed = modelprofile.SpeedCapability{ACPConfigID: "service_tier", DefaultSpeed: "standard", Choices: []modelprofile.SpeedChoice{{Canonical: "standard", WireValue: "default"}, {Canonical: "fast", WireValue: "priority"}}}
	doc.ExternalAgents = external
	doc.ModelProfiles, err = modelprofile.Upsert(doc.ModelProfiles, p)
	if err != nil {
		t.Fatal(err)
	}
	doc.AgentBindings.Roles = append(doc.AgentBindings.Roles, agentbinding.Role{Handle: "native-helper", Description: "Native result fixture."}, agentbinding.Role{Handle: "acp-helper", Description: "ACP peer-message fixture."})
	doc.AgentBindings, err = agentbinding.Bind(doc.AgentBindings, agentbinding.Binding{Handle: "native-helper", ProfileID: nativeProfile, Effort: "none"}, doc.ModelProfiles)
	if err != nil {
		t.Fatal(err)
	}
	doc.AgentBindings, err = agentbinding.Bind(doc.AgentBindings, agentbinding.Binding{Handle: "acp-helper", ProfileID: p.ID, Effort: "none", Speed: "standard"}, doc.ModelProfiles)
	if err != nil {
		t.Fatal(err)
	}
	if err = host.composition.authorities.store.Save(doc); err != nil {
		t.Fatal(err)
	}
	parent, err := startGatewayAppTestSession(ctx, host, "live-codex-controller")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := host.ConfigurationCommands().UseSessionModel(ctx, appserver.Principal{ID: "owner"}, appserver.SessionModelRequest{WriteBase: appserver.WriteBase{OperationID: "select-live-controller", SessionID: parent.SessionID, ExpectedRevision: &parent.Revision, ExpectedControllerEpoch: parent.Controller.EpochID}, Model: p.ID})
	if err != nil || receipt.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("select controller: %#v %v", receipt, err)
	}
	parent = mustCurrentSession(t, host, parent.SessionID)
	var toolUpdates []string
	var permissionUpdates []string
	output, err := runHeadlessOnceForGatewayAppTest(ctx, host, parent, parent.SessionID, `Use Caelis collaboration tools. Search for Caelis StartThread and the collaboration tools if deferred. Read StartThread's advertised Agent names and descriptions. Create exactly two Caelis participants: select the role described as "Native result fixture." with handle leaf and prompt "Reply NATIVE_CHILD_OK only", and select the role described as "ACP peer-message fixture." with handle cedar and prompt "Search Caelis SendMessage and send to parent the exact text ACP_PEER_OK, then reply ACP_CHILD_OK. Do not call shell or file tools." Use Caelis WaitThread and ReadThread to obtain both public results, SendMessage to leaf with message NATIVE_PEER_OK, and ReadMessages with cursor 0 to inspect shared messages. Finish with COLLABORATION_OK. Do not use Codex spawn_agent or any other non-Caelis delegation. Do not call shell or file tools.`, headless.Options{ObserveEnvelope: func(env eventstream.Envelope) error {
		if env.Permission != nil {
			raw, _ := json.Marshal(env.Permission)
			permissionUpdates = append(permissionUpdates, string(raw))
		}
		if env.Update != nil {
			raw, _ := json.Marshal(env.Update)
			if strings.Contains(string(raw), "tool_call") {
				toolUpdates = append(toolUpdates, string(raw))
			}
		}
		return nil
	}, ResolveApproval: func(_ context.Context, req headless.ApprovalRequest) (approval.Decision, error) {
		if req.Payload != nil && (req.Payload.RawInput["mcp_server"] == "caelis-collaboration" || req.Payload.ToolName == "StartThread") {
			return approval.Decision{Approved: true, OptionID: "allow_once"}, nil
		}
		return approval.Decision{Approved: false}, nil
	}})
	if err != nil {
		for _, update := range append(toolUpdates, permissionUpdates...) {
			t.Log(update)
		}
		t.Fatal(err)
	}
	t.Logf("controller output: %s", output.Output)
	parent = mustCurrentSession(t, host, parent.SessionID)
	entries, err := host.composition.authorities.taskStore.ListSession(ctx, parent.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	frozenKinds := map[placement.Kind]bool{}
	for _, participant := range parent.Participants {
		if participant.Kind != session.ParticipantKindSubagent {
			continue
		}
		found := false
		for _, entry := range entries {
			if entry.TaskID == participant.DelegationID {
				found = true
				if entry.State != task.StateCompleted {
					t.Fatalf("child Task did not finish: %s %s", entry.TaskID, entry.State)
				}
			}
		}
		if !found || participant.SessionID == "" || participant.Placement.Fingerprint == "" {
			t.Fatal("child missed canonical Task/Session/placement")
		}
		frozenKinds[participant.Placement.Kind] = true
	}
	if !frozenKinds[placement.KindModel] || !frozenKinds[placement.KindAgent] {
		for _, update := range toolUpdates {
			t.Log(update)
		}
		events, _ := host.composition.sessions.Events(ctx, session.EventsRequest{SessionRef: parent.SessionRef, IncludeTransient: true})
		for _, event := range events {
			if event.Protocol != nil && event.Protocol.Update != nil && event.Protocol.Update.ToolCallID != "" {
				raw, _ := json.Marshal(event.Protocol.Update)
				t.Log(string(raw))
			}
			if event.Journal != nil && event.Journal.ToolExecution != nil {
				t.Logf("tool journal: %s %s", event.Journal.ToolExecution.Status, event.Journal.ToolExecution.Error)
			}
		}
		t.Fatalf("expected native and ACP children, got %#v", parent.Participants)
	}
	events, err := host.composition.sessions.Events(ctx, session.EventsRequest{SessionRef: parent.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	spawnCalls := map[string]bool{}
	for _, event := range events {
		if event.Journal != nil && event.Journal.ToolExecution != nil {
			record := event.Journal.ToolExecution
			if record.ToolName == "StartThread" && record.Status == session.ToolExecutionSucceeded && record.Key.RunID != "" && record.Key.TurnID != "" {
				spawnCalls[record.Key.ToolCallID] = true
			}
		}
	}
	if len(spawnCalls) != 2 {
		t.Fatalf("canonical creation journal has %d calls", len(spawnCalls))
	}
	for _, handle := range []string{"leaf", "cedar"} {
		read, err := host.CollaborationService().Read(ctx, collaboration.Identity{Session: parent.SessionID, Member: "parent"}, collaboration.Target{Handle: handle})
		if err != nil || read.Output == "" {
			t.Fatalf("missing public result for %s: %#v %v", handle, read, err)
		}
	}
	zero := uint64(0)
	page, err := host.CollaborationService().ReadMessages(ctx, collaboration.Identity{Session: parent.SessionID, Member: "parent"}, &zero, 32)
	if err != nil {
		t.Fatal(err)
	}
	bodies := []string{}
	for _, message := range page.Messages {
		bodies = append(bodies, message.Text)
	}
	if !strings.Contains(strings.Join(bodies, "\n"), "ACP_PEER_OK") || !strings.Contains(strings.Join(bodies, "\n"), "NATIVE_PEER_OK") {
		t.Fatalf("peer messages missing: %v", bodies)
	}
	if path := os.Getenv("CAELIS_CODEX_COLLABORATION_E2E_OUT"); path != "" {
		data, _ := json.MarshalIndent(map[string]any{"participants": parent.Participants, "tasks": entries, "spawn_calls": len(spawnCalls), "messages": page, "tool_updates": toolUpdates}, "", "  ")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
