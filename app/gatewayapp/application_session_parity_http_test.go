package gatewayapp_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/control/uipreferences"
)

func TestOwnedWorkerHTTPUsesNativeSessionControlsAndIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	provider := &nativeModelScript{}
	host := startApplicationHTTPHost(t, filepath.Join(root, "store"), root, provider)
	defer host.close(t)
	connectRevisionHTTPModel(t, ctx, host, "gpt-5.4")
	client, _ := registerApplicationHTTP(t, ctx, host, "owner", filepath.Join(root, "owner.token"))
	other, _ := registerApplicationHTTP(t, ctx, host, "foreign", filepath.Join(root, "foreign.token"))
	created, err := client.CreateWorker(ctx, appserver.CreateWorkerRequest{WriteBase: appserver.WriteBase{OperationID: "worker"}, CWD: root, Model: "openai/gpt-5.4"})
	if err != nil || created.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("create: %+v, %v", created, err)
	}
	sid := created.SessionID
	state, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	base := appserver.WriteBase{OperationID: "select-model", SessionID: sid, ExpectedRevision: &state.Revision, ExpectedControllerEpoch: state.Controller.EpochID}
	selected, err := client.UseSessionModel(ctx, appserver.SessionModelRequest{WriteBase: base, Model: "openai/gpt-5.4", ReasoningEffort: "high", FastMode: true})
	if err != nil || selected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("owned model: %+v, %v", selected, err)
	}
	base.OperationID = "stale-model"
	if _, err := client.UseSessionModel(ctx, appserver.SessionModelRequest{WriteBase: base, Model: "openai/gpt-5.4", ReasoningEffort: "low"}); errorcode.CodeOf(err) != errorcode.Conflict {
		t.Fatalf("stale revision did not retain native CAS: %v", err)
	}
	modeBase := appserver.WriteBase{OperationID: "worker-approval-mode", SessionID: sid, ExpectedRevision: &selected.Revision, ExpectedControllerEpoch: state.Controller.EpochID}
	if changed, err := client.ConfigureSessionMode(ctx, appserver.SessionModeRequest{WriteBase: modeBase, Mode: "manual"}); err != nil || changed.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("owned native Session mode: %+v, %v", changed, err)
	}
	for _, who := range []*httpclient.Client{client, host.host} {
		status, err := who.SessionStatus(ctx, appserver.StatusRequest{SessionID: sid, IncludeDiagnostics: true})
		if err != nil || status.Session.ID != sid || status.ModelStatus.ReasoningEffort != "high" || !status.ModelStatus.FastMode {
			t.Fatalf("owned status: %+v, %v", status, err)
		}
		presentation, err := who.PresentationSnapshot(ctx, appserver.PresentationRequest{SessionID: sid})
		if err != nil || presentation.Models == nil || presentation.Models.CurrentModelID != "openai/gpt-5.4" {
			t.Fatalf("owned presentation: %+v, %v", presentation, err)
		}
		for _, command := range []string{"model", "model openai/gpt-5.4", "model openai/gpt-5.4 high"} {
			items, err := who.CompleteSlashArg(ctx, appserver.CompletionRequest{SessionID: sid, Command: command})
			if err != nil || len(items) == 0 {
				t.Fatalf("owned model/effort/speed completion %q: %+v, %v", command, items, err)
			}
		}
		if _, err := who.CompleteFile(ctx, appserver.CompletionRequest{SessionID: sid}); err != nil {
			t.Fatal(err)
		}
		if _, err := who.CompleteSkill(ctx, appserver.CompletionRequest{SessionID: sid}); err != nil {
			t.Fatal(err)
		}
		if _, err := who.ResolveSkill(ctx, appserver.CompletionRequest{SessionID: sid, Name: "unknown"}); err != nil {
			t.Fatal(err)
		}
		if _, err := who.SubagentInputStatuses(ctx, appserver.SubagentInputStatusRequest{SessionID: sid}); err != nil {
			t.Fatal(err)
		}
		if _, err := who.AgentStatus(ctx, appserver.AgentRequest{SessionID: sid}); err != nil {
			t.Fatalf("owned Session agent status: %v", err)
		}
		if _, err := who.ListAgents(ctx, appserver.AgentRequest{SessionID: sid}); err != nil {
			t.Fatalf("owned Session agent catalog: %v", err)
		}
	}
	userHandles, err := host.host.Handles(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	state, err = client.InspectSession(ctx, appserver.StateRequest{SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(appserver.WriteBase) error{
		func(base appserver.WriteBase) error {
			_, err := client.ConfigureSessionControllerMode(ctx, appserver.SessionControllerModeRequest{WriteBase: base, Mode: "unavailable"})
			return err
		},
		func(base appserver.WriteBase) error {
			_, err := client.ConfigureSessionPresentationMode(ctx, appserver.SessionPresentationModeRequest{WriteBase: base, Mode: "unavailable"})
			return err
		},
		func(base appserver.WriteBase) error {
			_, err := client.ConfigureSessionPresentation(ctx, appserver.SessionPresentationConfigRequest{WriteBase: base, ConfigID: "unavailable", Value: "unavailable"})
			return err
		},
	} {
		modeBase.OperationID += "-probe"
		modeBase.ExpectedRevision = &state.Revision
		if err := change(modeBase); err == nil || errorcode.CodeOf(err) == errorcode.PermissionDenied || errorcode.CodeOf(err) == errorcode.Conflict {
			t.Fatalf("owned mode/config selection did not reach native option validation: %v", err)
		}
	}
	appHandles, err := client.Handles(ctx, sid)
	if err != nil || !reflect.DeepEqual(userHandles, appHandles) {
		t.Fatalf("native handle projection differs: %v / %v, %v", userHandles, appHandles, err)
	}
	state, err = client.InspectSession(ctx, appserver.StateRequest{SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	base = appserver.WriteBase{OperationID: "compact", SessionID: sid, ExpectedRevision: &state.Revision, ExpectedControllerEpoch: state.Controller.EpochID}
	if compacted, err := client.CompactSession(ctx, appserver.CompactSessionRequest{WriteBase: base}); err != nil || compacted.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("native compact owner: %+v, %v", compacted, err)
	}
	base.OperationID = "unknown-handle"
	if _, err := client.StartParticipant(ctx, appserver.StartParticipantRequest{WriteBase: base, Handle: "missing", Input: "review"}); errorcode.CodeOf(err) == errorcode.PermissionDenied || err == nil {
		t.Fatalf("owned participant did not reach native handle validation: %v", err)
	}
	if _, err := client.SubmitSubagentInput(ctx, appserver.SubagentInputRequest{SessionID: sid, OperationID: "unknown-child", ParticipantID: "missing", TaskID: "missing", Input: "continue"}); errorcode.CodeOf(err) == errorcode.PermissionDenied || err == nil {
		t.Fatalf("owned subagent input did not reach native exact-child validation: %v", err)
	}
	modeBase.OperationID = "local-handoff"
	modeBase.ExpectedRevision = &state.Revision
	if handoff, err := client.HandoffAgent(ctx, appserver.HandoffAgentRequest{WriteBase: modeBase, Target: "local"}); err != nil || handoff.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("owned native controller handoff: %+v, %v", handoff, err)
	}
	assertForeignSessionControlDenied(t, ctx, other, sid, state)
	closedWorker, err := client.CreateWorker(ctx, appserver.CreateWorkerRequest{WriteBase: appserver.WriteBase{OperationID: "closed-worker"}, CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	closing, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: closedWorker.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	closed, err := client.CloseSession(ctx, appserver.CloseSessionRequest{WriteBase: appserver.WriteBase{OperationID: "close-worker", SessionID: closing.SessionID, ExpectedRevision: &closing.Revision, ExpectedControllerEpoch: closing.Controller.EpochID}})
	if err != nil || closed.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("owned native close: %+v, %v", closed, err)
	}
	if _, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: closing.SessionID}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UseSessionModel(ctx, appserver.SessionModelRequest{WriteBase: appserver.WriteBase{OperationID: "closed-model", SessionID: closing.SessionID, ExpectedRevision: &closed.Revision, ExpectedControllerEpoch: closing.Controller.EpochID}, Model: "openai/gpt-5.4"}); errorcode.CodeOf(err) != errorcode.FailedPrecondition {
		t.Fatalf("closed native Worker mutated: %v", err)
	}
	if err := client.RevokeApplicationConnection(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SessionStatus(ctx, appserver.StatusRequest{SessionID: sid}); err != nil {
		t.Fatalf("revoked status observation: %v", err)
	}
	if _, err := client.PresentationSnapshot(ctx, appserver.PresentationRequest{SessionID: sid}); err != nil {
		t.Fatalf("revoked presentation observation: %v", err)
	}
	if _, err := client.Handles(ctx, sid); err != nil {
		t.Fatalf("revoked handles observation: %v", err)
	}
	if _, err := client.SubagentInputStatuses(ctx, appserver.SubagentInputStatusRequest{SessionID: sid}); err != nil {
		t.Fatalf("revoked input receipt observation: %v", err)
	}
	base.OperationID = "revoked-compact"
	if _, err := client.CompactSession(ctx, appserver.CompactSessionRequest{WriteBase: base}); err == nil {
		t.Fatal("revoked compact acquired mutation authority")
	}
	base.OperationID = "revoked-model"
	if _, err := client.UseSessionModel(ctx, appserver.SessionModelRequest{WriteBase: base, Model: "openai/gpt-5.4"}); err == nil {
		t.Fatal("revoked model acquired mutation authority")
	}
	if err := client.KillTerminal(ctx, appserver.TerminalRequest{SessionID: sid, TerminalID: "missing"}); errorcode.CodeOf(err) != errorcode.FailedPrecondition {
		t.Fatalf("revoked kill passed mutation owner: %v", err)
	}
	if err := client.ReleaseTerminal(ctx, appserver.TerminalRequest{SessionID: sid, TerminalID: "missing"}); errorcode.CodeOf(err) != errorcode.FailedPrecondition {
		t.Fatalf("revoked release passed mutation owner: %v", err)
	}
}

func assertForeignSessionControlDenied(t *testing.T, ctx context.Context, client *httpclient.Client, sid string, state appserver.SessionState) {
	t.Helper()
	base := appserver.WriteBase{OperationID: "foreign", SessionID: sid, ExpectedRevision: &state.Revision, ExpectedControllerEpoch: state.Controller.EpochID}
	checks := []struct {
		name   string
		invoke func() error
	}{
		{"status", func() error { _, err := client.SessionStatus(ctx, appserver.StatusRequest{SessionID: sid}); return err }},
		{"presentation", func() error {
			_, err := client.PresentationSnapshot(ctx, appserver.PresentationRequest{SessionID: sid})
			return err
		}},
		{"read", func() error { _, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: sid}); return err }},
		{"compact", func() error {
			_, err := client.CompactSession(ctx, appserver.CompactSessionRequest{WriteBase: base})
			return err
		}},
		{"model", func() error {
			_, err := client.UseSessionModel(ctx, appserver.SessionModelRequest{WriteBase: base, Model: "openai/gpt-5.4"})
			return err
		}},
		{"participants", func() error { _, err := client.Handles(ctx, sid); return err }},
		{"participant-start", func() error {
			_, err := client.StartParticipant(ctx, appserver.StartParticipantRequest{WriteBase: base, Handle: "reviewer", Input: "review"})
			return err
		}},
		{"session-mode", func() error {
			_, err := client.ConfigureSessionMode(ctx, appserver.SessionModeRequest{WriteBase: base, Mode: "manual"})
			return err
		}},
		{"controller-mode", func() error {
			_, err := client.ConfigureSessionControllerMode(ctx, appserver.SessionControllerModeRequest{WriteBase: base, Mode: "manual"})
			return err
		}},
		{"presentation-config", func() error {
			_, err := client.ConfigureSessionPresentation(ctx, appserver.SessionPresentationConfigRequest{WriteBase: base, ConfigID: "mode", Value: "manual"})
			return err
		}},
		{"controller-handoff", func() error {
			_, err := client.HandoffAgent(ctx, appserver.HandoffAgentRequest{WriteBase: base, Target: "local"})
			return err
		}},
		{"session-close", func() error {
			_, err := client.CloseSession(ctx, appserver.CloseSessionRequest{WriteBase: base})
			return err
		}},
		{"input", func() error {
			_, err := client.SubmitSubagentInput(ctx, appserver.SubagentInputRequest{SessionID: sid, OperationID: "foreign-child", ParticipantID: "missing", TaskID: "missing", Input: "continue"})
			return err
		}},
		{"input-status", func() error {
			_, err := client.SubagentInputStatuses(ctx, appserver.SubagentInputStatusRequest{SessionID: sid})
			return err
		}},
		{"file-completion", func() error {
			_, err := client.CompleteFile(ctx, appserver.CompletionRequest{SessionID: sid})
			return err
		}},
		{"terminal-read", func() error {
			_, err := client.TerminalOutput(ctx, appserver.TerminalRequest{SessionID: sid, TerminalID: "missing"})
			return err
		}},
		{"terminal-wait", func() error {
			_, err := client.WaitTerminal(ctx, appserver.TerminalRequest{SessionID: sid, TerminalID: "missing"})
			return err
		}},
		{"terminal-kill", func() error {
			return client.KillTerminal(ctx, appserver.TerminalRequest{SessionID: sid, TerminalID: "missing"})
		}},
		{"terminal-release", func() error {
			return client.ReleaseTerminal(ctx, appserver.TerminalRequest{SessionID: sid, TerminalID: "missing"})
		}},
	}
	for _, check := range checks {
		if err := check.invoke(); errorcode.CodeOf(err) != errorcode.PermissionDenied && errorcode.CodeOf(err) != errorcode.NotFound {
			t.Fatalf("foreign %s = %v", check.name, err)
		}
	}
}

func TestApplicationMainHTTPProfileProjectionAndRecoveryArePassive(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	provider := newRevisionProvider()
	host := startApplicationHTTPHost(t, filepath.Join(root, "store"), root, provider)
	defer func() { host.close(t) }()
	connectRevisionHTTPModel(t, ctx, host, "gpt-5.4")
	client, _ := registerApplicationHTTP(t, ctx, host, "main", filepath.Join(root, "main.token"))
	other, _ := registerApplicationHTTP(t, ctx, host, "other", filepath.Join(root, "other.token"))
	selectedSkill := filepath.Join(root, "selected")
	writeAtomicSkill(t, selectedSkill, "selected-skill", "EXPLICIT_SKILL")
	profile := applicationHTTPProfile()
	profile.Model, profile.ReasoningEffort = "openai/gpt-5.4", "low"
	profile.SkillRoots = []string{selectedSkill}
	created, err := client.CreateApplicationSession(ctx, appserver.CreateApplicationSessionRequest{WriteBase: appserver.WriteBase{OperationID: "main"}, Profile: profile})
	if err != nil {
		t.Fatal(err)
	}
	sid := created.SessionID
	checkProjection := func(effort string) {
		t.Helper()
		status, err := client.SessionStatus(ctx, appserver.StatusRequest{SessionID: sid, IncludeDiagnostics: true})
		if err != nil || status.ModelStatus.ReasoningEffort != effort || status.SandboxStatus.Route != "none" || status.SandboxStatus.SecuritySummary != "tools-only" || status.Diagnostics.ConfigPath != "" || len(status.Runtime.ActiveSessions) != 0 {
			t.Fatalf("explicit passive status: %+v, %v", status, err)
		}
		presentation, err := client.PresentationSnapshot(ctx, appserver.PresentationRequest{SessionID: sid})
		if err != nil || presentation.Models == nil || presentation.Models.CurrentModelID != profile.Model || presentation.Modes == nil || len(presentation.Modes.AvailableModes) != 0 || len(presentation.Commands) != 1 || presentation.Commands[0].Name != "compact" {
			t.Fatalf("explicit passive presentation: %+v, %v", presentation, err)
		}
		candidates, err := client.CompleteSkill(ctx, appserver.CompletionRequest{SessionID: sid})
		if err != nil || len(candidates) != 1 || candidates[0].Value != "selected-skill" {
			t.Fatalf("explicit skill projection: %+v, %v", candidates, err)
		}
		if agents, err := client.ListAgents(ctx, appserver.AgentRequest{SessionID: sid}); err != nil || len(agents) != 0 {
			t.Fatalf("main profile borrowed Host Agent catalog: %+v, %v", agents, err)
		}
		if agent, err := client.AgentStatus(ctx, appserver.AgentRequest{SessionID: sid}); err != nil || agent.SessionID != sid || agent.HasActiveTurn {
			t.Fatalf("native passive Agent state: %+v, %v", agent, err)
		}
		for _, command := range []string{"model", "model openai/gpt-5.4", "model openai/gpt-5.4 high"} {
			items, err := client.CompleteSlashArg(ctx, appserver.CompletionRequest{SessionID: sid, Command: command})
			if err != nil || len(items) == 0 {
				t.Fatalf("explicit model completion %q: %+v, %v", command, items, err)
			}
		}
		if _, err := client.CompleteFile(ctx, appserver.CompletionRequest{SessionID: sid}); errorcode.CodeOf(err) != errorcode.PermissionDenied {
			t.Fatalf("tools-only scanned incidental Host workspace: %v", err)
		}
		if got := len(provider.snapshot()); got != 0 {
			t.Fatalf("observation dispatched %d provider calls", got)
		}
	}
	checkProjection("low")
	effort := "high"
	if _, err := client.UpdateApplicationConfiguration(ctx, sid, application.UpdateConfigurationRequest{OperationID: "new-effort", ExpectedConfigurationRevision: 1, Patch: application.ConfigurationPatch{ReasoningEffort: &effort}}); err != nil {
		t.Fatal(err)
	}
	checkProjection("high")
	state, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	assertForeignSessionControlDenied(t, ctx, other, sid, state)
	info, err := client.Initialize(ctx)
	if err != nil || !slices.Contains(info.Capabilities, appserver.CapabilityApplicationTerminalWait) || !slices.Contains(info.Capabilities, appserver.CapabilityApplicationTerminalControl) {
		t.Fatalf("negotiation: %+v, %v", info, err)
	}
	host.close(t)
	host = startApplicationHTTPHost(t, filepath.Join(root, "store"), root, provider)
	token, err := os.ReadFile(filepath.Join(root, "main.token"))
	if err != nil {
		t.Fatal(err)
	}
	client = host.app(string(token))
	checkProjection("high")
	state, err = client.InspectSession(ctx, appserver.StateRequest{SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	closed, err := client.CloseSession(ctx, appserver.CloseSessionRequest{WriteBase: appserver.WriteBase{
		OperationID: "main-native-close", SessionID: sid, ExpectedRevision: &state.Revision, ExpectedControllerEpoch: state.Controller.EpochID}})
	if err != nil || closed.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("HTTP same-object close did not reach original Archive: %+v, %v", closed, err)
	}
	binding, err := client.ApplicationSession(ctx, sid)
	if err != nil || !binding.Archived {
		t.Fatalf("HTTP same-object close omitted Archive binding: %+v, %v", binding, err)
	}
	if receipt, err := client.ApplicationOperation(ctx, closed.OperationID); err != nil || receipt.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("HTTP original Archive receipt: %+v, %v", receipt, err)
	}
}

func TestScopedEmbeddedClientsUseTheSameOwnedObjectsAsHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	host := startApplicationHTTPHost(t, filepath.Join(root, "store"), root, &nativeModelScript{})
	defer host.close(t)
	connectRevisionHTTPModel(t, ctx, host, "gpt-5.4")
	client, connection := registerApplicationHTTP(t, ctx, host, "embedded", filepath.Join(root, "embedded.token"))
	_, foreign := registerApplicationHTTP(t, ctx, host, "foreign", filepath.Join(root, "foreign.token"))
	server, err := local.NewAppServer(host.stack)
	if err != nil {
		t.Fatal(err)
	}
	principal := appserver.Principal{ID: connection.PrincipalID, ApplicationID: connection.ApplicationID, ConnectionID: connection.ConnectionID}
	bound, err := server.Bind(principal)
	if err != nil {
		t.Fatal(err)
	}
	wrong := principal
	wrong.ConnectionID += "-foreign"
	if _, err := server.Bind(wrong); err == nil {
		t.Fatal("wrong ConnectionID bound embedded clients")
	}
	foreignBound, err := server.Bind(appserver.Principal{ID: foreign.PrincipalID, ApplicationID: foreign.ApplicationID, ConnectionID: foreign.ConnectionID})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := client.CreateWorker(ctx, appserver.CreateWorkerRequest{WriteBase: appserver.WriteBase{OperationID: "embedded-worker"}, CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	sid := worker.SessionID
	state, err := bound.Sessions.InspectSession(ctx, appserver.StateRequest{SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	base := appserver.WriteBase{OperationID: "embedded-model", SessionID: sid, ExpectedRevision: &state.Revision, ExpectedControllerEpoch: state.Controller.EpochID}
	selected, err := bound.Configuration.UseSessionModel(ctx, appserver.SessionModelRequest{WriteBase: base, Model: "openai/gpt-5.4", ReasoningEffort: "high"})
	if err != nil || selected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("embedded native model: %+v, %v", selected, err)
	}
	for _, observed := range []appserver.AppServerClients{bound, foreignBound} {
		owned := observed.Sessions == bound.Sessions
		for _, check := range []struct {
			name   string
			invoke func() error
		}{
			{"state", func() error {
				_, err := observed.Sessions.InspectSession(ctx, appserver.StateRequest{SessionID: sid})
				return err
			}},
			{"status", func() error {
				_, err := observed.Status.SessionStatus(ctx, appserver.StatusRequest{SessionID: sid})
				return err
			}},
			{"presentation", func() error {
				_, err := observed.Presentation.PresentationSnapshot(ctx, appserver.PresentationRequest{SessionID: sid})
				return err
			}},
			{"participants", func() error { _, err := observed.Participants.Handles(ctx, sid); return err }},
			{"tasks", func() error { _, err := observed.Tasks.List(ctx, taskstream.ListRequest{SessionID: sid}); return err }},
			{"input-status", func() error {
				_, err := observed.SubagentInputs.SubagentInputStatuses(ctx, appserver.SubagentInputStatusRequest{SessionID: sid})
				return err
			}},
			{"agents", func() error {
				_, err := observed.Agents.ListAgents(ctx, appserver.AgentRequest{SessionID: sid})
				return err
			}},
		} {
			err := check.invoke()
			if owned && err != nil || !owned && errorcode.CodeOf(err) != errorcode.PermissionDenied && errorcode.CodeOf(err) != errorcode.NotFound {
				t.Fatalf("embedded %s own=%v: %v", check.name, owned, err)
			}
		}
	}
	hostRevision := uint64(0)
	for _, check := range []func() error{
		func() error { _, err := bound.UIPreferences.LoadUIPreferences(ctx); return err },
		func() error {
			return bound.UIPreferences.SaveUIPreferences(ctx, uipreferences.Preferences{Theme: "catppuccin"})
		},
		func() error {
			_, err := bound.Plugins.ListPlugins(ctx, appserver.PluginRequest{SessionID: sid})
			return err
		},
		func() error { _, err := bound.Plugins.ListMarketplaces(ctx, appserver.PluginRequest{}); return err },
		func() error { _, err := bound.Agents.AgentBindingStatus(ctx, appserver.AgentRequest{}); return err },
		func() error { _, err := bound.Agents.DisconnectCandidates(ctx, appserver.AgentRequest{}); return err },
		func() error {
			_, err := bound.Agents.ACPPreparation(ctx, appserver.ACPPreparationRequest{Ref: "missing"})
			return err
		},
		func() error { _, err := bound.Agents.ListAgents(ctx, appserver.AgentRequest{}); return err },
		func() error { _, err := bound.Presentation.PresentationCapabilities(ctx); return err },
		func() error { _, err := bound.Status.SessionStatus(ctx, appserver.StatusRequest{}); return err },
		func() error { _, err := bound.Sessions.ListSessions(ctx, appserver.ListSessionsRequest{}); return err },
		func() error {
			_, err := bound.Configuration.UseModel(ctx, appserver.UseModelRequest{WriteBase: appserver.WriteBase{OperationID: "host-model", ExpectedRevision: &hostRevision}, Model: "openai/gpt-5.4"})
			return err
		},
	} {
		if err := check(); errorcode.CodeOf(err) != errorcode.PermissionDenied {
			t.Fatalf("scoped embedded clients escaped into Host-global object: %v", err)
		}
	}
	profile := applicationHTTPProfile()
	profile.Model = "openai/gpt-5.4"
	main, err := client.CreateApplicationSession(ctx, appserver.CreateApplicationSessionRequest{WriteBase: appserver.WriteBase{OperationID: "embedded-main"}, Profile: profile})
	if err != nil {
		t.Fatal(err)
	}
	mainState, err := bound.Sessions.InspectSession(ctx, appserver.StateRequest{SessionID: main.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	closeBase := appserver.WriteBase{OperationID: "embedded-main-archive", SessionID: main.SessionID, ExpectedRevision: &mainState.Revision, ExpectedControllerEpoch: mainState.Controller.EpochID}
	archived, err := bound.Sessions.CloseSession(ctx, appserver.CloseSessionRequest{WriteBase: closeBase})
	if err != nil || archived.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("embedded main Archive: %+v, %v", archived, err)
	}
	binding, err := client.ApplicationSession(ctx, main.SessionID)
	if err != nil || !binding.Archived {
		t.Fatalf("embedded close omitted original binding Archive: %+v, %v", binding, err)
	}
	if receipt, err := client.ApplicationOperation(ctx, closeBase.OperationID); err != nil || receipt.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("original Archive receipt: %+v, %v", receipt, err)
	}
	if err := client.RevokeApplicationConnection(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Bind(principal); err != nil {
		t.Fatalf("revoked observation binding: %v", err)
	}
	if _, err := bound.Tasks.List(ctx, taskstream.ListRequest{SessionID: sid}); err != nil {
		t.Fatalf("revoked own Task read: %v", err)
	}
	if _, err := bound.SubagentInputs.SubagentInputStatuses(ctx, appserver.SubagentInputStatusRequest{SessionID: sid}); err != nil {
		t.Fatalf("revoked own input receipt read: %v", err)
	}
	base.OperationID = "revoked-embedded-model"
	if _, err := bound.Configuration.UseSessionModel(ctx, appserver.SessionModelRequest{WriteBase: base, Model: "openai/gpt-5.4"}); err == nil {
		t.Fatal("revoked embedded Worker mutation admitted")
	}
	if err := bound.Terminal.KillTerminal(ctx, appserver.TerminalRequest{SessionID: sid, TerminalID: "missing"}); err == nil {
		t.Fatal("revoked embedded Terminal mutation admitted")
	}
}
