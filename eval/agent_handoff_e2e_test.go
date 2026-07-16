//go:build e2e

package eval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	controlassembly "github.com/caelis-labs/caelis/internal/controlassembly"
	controlpromptrouter "github.com/caelis-labs/caelis/internal/controlpromptrouter"
	controlprompt "github.com/caelis-labs/caelis/ports/controlprompt"
	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/protocol/acp/control"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/surfaces/headless"
)

// TestAgentHandoffProductFlowE2E exercises the user-visible command through the
// production local adapter, a real stdio ACP process, the Control handoff
// coordinator, the next controller-owned prompt, and the durable Session log.
func TestAgentHandoffProductFlowE2E(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test launcher uses a POSIX shell script")
	}
	repo := repoRootForGatewayAppTest(t)
	root := t.TempDir()
	workdir := t.TempDir()
	childRoot := filepath.Join(root, "controller-sessions")
	launcher := writeAgentHandoffLauncher(t, repo, childRoot)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	stackConfig := gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "user-1",
		StoreDir:     root,
		WorkspaceKey: workdir,
		WorkspaceCWD: workdir,
		ApprovalMode: "auto-review",
		Model:        gatewayapp.ModelConfig{Provider: "minimax", Model: "MiniMax-M2"},
	}
	stack, err := gatewayapp.NewLocalStack(stackConfig)
	if err != nil {
		t.Fatalf("gatewayapp.NewLocalStack() error = %v", err)
	}
	connectReq := controlagents.ConnectRequest{
		AdapterID:   "custom",
		Launcher:    controlagents.LauncherChoiceCommand,
		CommandLine: launcher,
		CWD:         workdir,
	}
	catalog, err := stack.DiscoverACPConnection(ctx, connectReq)
	if err != nil {
		t.Fatalf("DiscoverACPConnection(catalog) error = %v", err)
	}
	if !hasRemoteModel(catalog.Models, "opus") {
		t.Fatalf("catalog models = %#v, want opus", catalog.Models)
	}
	connectReq.ModelID = "opus"
	selected, err := stack.DiscoverACPConnection(ctx, connectReq)
	if err != nil {
		t.Fatalf("DiscoverACPConnection(opus) error = %v", err)
	}
	if selected.SelectedModelID != "opus" || selected.CurrentModelID != "opus" || !hasConfigChoice(selected.ConfigOptions, "effort", "max") {
		t.Fatalf("selected discovery = %#v, want model opus and effort max", selected)
	}
	connectReq.ConfigValues = map[string]string{"effort": "max"}
	connectReq.Discovery = &selected
	connected, err := stack.ConnectACP(ctx, connectReq)
	if err != nil {
		t.Fatalf("ConnectACP(opus) error = %v", err)
	}
	if len(connected.Agents) != 1 || connected.Agents[0].ID != "caelis-acp-e2e-agent" || connected.Agents[0].Name != "caelis-acp-e2e-agent(opus)" {
		t.Fatalf("ConnectACP(opus) agents = %#v, want provider-scoped Agent name with model detail", connected.Agents)
	}
	agentID := connected.Agents[0].ID
	persisted, err := gatewayapp.LoadAppConfig(root)
	if err != nil {
		t.Fatalf("LoadAppConfig(after connect) error = %v", err)
	}
	opus, ok := controlagents.LookupAgent(persisted.AgentRoster, agentID)
	if !ok || opus.Defaults.ModelID != "opus" || opus.Defaults.ConfigValues["effort"] != "max" {
		t.Fatalf("persisted opus Agent = %#v, found=%v", opus, ok)
	}
	discoverySessions := childSessionIDs(t, ctx, childRoot, workdir)
	if err := stack.Close(); err != nil {
		t.Fatalf("Close(stack after connect) error = %v", err)
	}
	stack, err = gatewayapp.NewLocalStack(stackConfig)
	if err != nil {
		t.Fatalf("gatewayapp.NewLocalStack(after connect) error = %v", err)
	}
	t.Cleanup(func() { _ = stack.Close() })

	active, err := stack.StartSession(ctx, "agent-handoff-e2e", "surface-handoff-e2e")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	driver, err := local.NewLocalAdapterForSession(ctx, stack, active, "headless-agent-handoff-e2e", "")
	if err != nil {
		t.Fatalf("NewLocalAdapterForSession() error = %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = driver.HandoffAgent(cleanupCtx, "local")
	})
	routed := routedHandoffStarter{router: controlpromptrouter.New(controlprompt.RouterConfig{Service: driver})}

	directCommand := "/" + agentID
	directOutput, err := runScopedAgentOnce(ctx, routed, control.Submission{Text: directCommand + " inspect side"})
	if err != nil {
		t.Fatalf("%s direct run error = %v", directCommand, err)
	}
	if got := strings.TrimSpace(directOutput); got != "opus owns this turn" {
		t.Fatalf("direct Agent output = %q, want %q", got, "opus owns this turn")
	}
	continuedCommand := directCommand + "-1"
	continuedOutput, err := runScopedAgentOnce(ctx, routed, control.Submission{Text: continuedCommand + " continue side"})
	if err != nil {
		t.Fatalf("%s continuation error = %v", continuedCommand, err)
	}
	if got := strings.TrimSpace(continuedOutput); got != "opus owns this turn" {
		t.Fatalf("continued Agent output = %q, want %q", got, "opus owns this turn")
	}
	directSessionID := assertOneNewChildSession(t, ctx, childRoot, workdir, discoverySessions, map[string]string{
		"model":  "opus",
		"effort": "max",
	}, []string{"inspect side", "continue side"})
	afterDirectSessions := childSessionIDs(t, ctx, childRoot, workdir)
	if _, ok := afterDirectSessions[directSessionID]; !ok {
		t.Fatalf("direct Agent session %q disappeared after continuation", directSessionID)
	}

	if _, err := headless.RunOnce(ctx, routed, control.Submission{Text: "/lead " + agentID}, headless.Options{}); err != nil {
		t.Fatalf("/lead %s error = %v", agentID, err)
	}
	state, err := stack.KernelControlPlane().ControlPlaneState(ctx, gateway.ControlPlaneStateRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatalf("ControlPlaneState(after handoff) error = %v", err)
	}
	if state.Controller.Kind != session.ControllerKindACP || !strings.EqualFold(state.Controller.AgentName, agentID) || strings.TrimSpace(state.Controller.EpochID) == "" {
		t.Fatalf("controller after /lead %s = %+v", agentID, state.Controller)
	}
	controllerSessionID := assertOneNewChildSession(t, ctx, childRoot, workdir, afterDirectSessions, map[string]string{
		"model":  "opus",
		"effort": "max",
	}, nil)
	afterHandoffSessions := childSessionIDs(t, ctx, childRoot, workdir)

	result, err := headless.RunOnce(ctx, routed, control.Submission{Text: "who owns this turn?"}, headless.Options{})
	if err != nil {
		t.Fatalf("prompt after /lead %s error = %v", agentID, err)
	}
	if got := strings.TrimSpace(result.Output); got != "opus owns this turn" {
		t.Fatalf("ACP controller output = %q, want %q", got, "opus owns this turn")
	}
	assertChildSessionHasUserPrompt(t, ctx, childRoot, workdir, controllerSessionID, "who owns this turn?")
	assertChildSessionIDsEqual(t, afterHandoffSessions, childSessionIDs(t, ctx, childRoot, workdir))

	if _, err := headless.RunOnce(ctx, routed, control.Submission{Text: "/lead local"}, headless.Options{}); err != nil {
		t.Fatalf("/lead local error = %v", err)
	}
	state, err = stack.KernelControlPlane().ControlPlaneState(ctx, gateway.ControlPlaneStateRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatalf("ControlPlaneState(after return) error = %v", err)
	}
	if state.Controller.Kind != session.ControllerKindKernel {
		t.Fatalf("controller after /lead local = %+v", state.Controller)
	}

	loaded, err := stack.Sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	var handoffs int
	var sawACPReply bool
	for _, event := range loaded.Events {
		if event == nil {
			continue
		}
		if session.EventTypeOf(event) == session.EventTypeHandoff {
			handoffs++
		}
		if session.EventTypeOf(event) == session.EventTypeAssistant && event.Scope != nil &&
			event.Scope.Controller.Kind == session.ControllerKindACP &&
			strings.TrimSpace(session.EventText(event)) == "opus owns this turn" {
			sawACPReply = true
		}
	}
	if handoffs != 2 || !sawACPReply {
		t.Fatalf("durable product flow: handoffs=%d saw_acp_reply=%v events=%#v", handoffs, sawACPReply, loaded.Events)
	}
}

func runScopedAgentOnce(ctx context.Context, starter headless.Starter, submission control.Submission) (string, error) {
	turn, err := starter.Submit(ctx, submission)
	if err != nil {
		return "", err
	}
	if turn == nil {
		return "", nil
	}
	defer turn.Close()
	var assistant schema.FinalAssistantAccumulator
	output := ""
	for envelope := range turn.Events() {
		if envelope.Err != nil {
			return output, envelope.Err
		}
		if envelope.Kind == eventstream.KindError && strings.TrimSpace(envelope.Error) != "" {
			return output, fmt.Errorf("Agent run: %s", strings.TrimSpace(envelope.Error))
		}
		if envelope.Kind != eventstream.KindSessionUpdate || envelope.Update == nil {
			continue
		}
		update := assistant.ObserveUpdate(envelope.Update)
		if update.Assistant && update.Text != "" {
			output = update.Text
		}
	}
	return output, nil
}

func writeAgentHandoffLauncher(t *testing.T, repo string, childRoot string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "caelis-acp-e2e-agent")
	script := fmt.Sprintf(`#!/bin/sh
export SDK_ACP_STUB_REPLY=%s
export SDK_ACP_SESSION_ROOT=%s
export SDK_ACP_ENABLE_MODEL_CONFIG=1
export SDK_ACP_ENABLE_SPAWN=0
export SDK_ACP_CHILD_NO_SPAWN=1
cd %s
exec go run ./internal/acpe2eagent
`, shellQuote("opus owns this turn"), shellQuote(childRoot), shellQuote(repo))
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("WriteFile(ACP E2E launcher) error = %v", err)
	}
	return path
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func hasRemoteModel(models []controlagents.RemoteModel, id string) bool {
	for _, model := range models {
		if model.ID == id {
			return true
		}
	}
	return false
}

func hasConfigChoice(options []controlagents.ConfigOption, id string, value string) bool {
	for _, option := range options {
		if option.ID != id {
			continue
		}
		for _, choice := range option.Options {
			if choice.Value == value {
				return true
			}
		}
	}
	return false
}

func childSessionIDs(t *testing.T, ctx context.Context, root string, workspace string) map[string]struct{} {
	t.Helper()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	listed, err := store.ListSessions(ctx, session.ListSessionsRequest{
		AppName: "caelis", UserID: "acp", WorkspaceKey: workspace, Limit: 100,
	})
	if err != nil {
		t.Fatalf("ListSessions(child ACP) error = %v", err)
	}
	ids := make(map[string]struct{}, len(listed.Sessions))
	for _, summary := range listed.Sessions {
		ids[summary.SessionID] = struct{}{}
	}
	return ids
}

func assertOneNewChildSession(
	t *testing.T,
	ctx context.Context,
	root string,
	workspace string,
	baseline map[string]struct{},
	wantDefaults map[string]string,
	wantUserPrompts []string,
) string {
	t.Helper()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	listed, err := store.ListSessions(ctx, session.ListSessionsRequest{
		AppName: "caelis", UserID: "acp", WorkspaceKey: workspace, Limit: 100,
	})
	if err != nil {
		t.Fatalf("ListSessions(child ACP after run) error = %v", err)
	}
	newSessions := make([]session.SessionSummary, 0, 1)
	for _, summary := range listed.Sessions {
		if _, existed := baseline[summary.SessionID]; !existed {
			newSessions = append(newSessions, summary)
		}
	}
	if len(newSessions) != 1 {
		t.Fatalf("new child ACP sessions = %#v, want exactly one beyond %#v", newSessions, baseline)
	}
	created := newSessions[0]
	state, err := store.SnapshotState(ctx, created.SessionRef)
	if err != nil {
		t.Fatalf("SnapshotState(child ACP %s) error = %v", created.SessionID, err)
	}
	values := controlassembly.CurrentConfigValues(state)
	for id, value := range wantDefaults {
		if values[id] != value {
			t.Fatalf("child ACP %s defaults = %#v, want %s=%s", created.SessionID, values, id, value)
		}
	}
	if wantUserPrompts != nil {
		loaded, loadErr := store.LoadSession(ctx, session.LoadSessionRequest{SessionRef: created.SessionRef})
		if loadErr != nil {
			t.Fatalf("LoadSession(child ACP %s) error = %v", created.SessionID, loadErr)
		}
		got := userEventTexts(loaded.Events)
		if !userPromptsEndWith(got, wantUserPrompts) {
			t.Fatalf("child ACP %s user prompts = %#v, want %#v", created.SessionID, got, wantUserPrompts)
		}
	}
	return created.SessionID
}

func assertChildSessionHasUserPrompt(t *testing.T, ctx context.Context, root string, workspace string, sessionID string, want string) {
	t.Helper()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	loaded, err := store.LoadSession(ctx, session.LoadSessionRequest{SessionRef: session.SessionRef{
		AppName: "caelis", UserID: "acp", WorkspaceKey: workspace, SessionID: sessionID,
	}})
	if err != nil {
		t.Fatalf("LoadSession(child ACP %s) error = %v", sessionID, err)
	}
	for _, text := range userEventTexts(loaded.Events) {
		if strings.HasSuffix(strings.TrimSpace(text), strings.TrimSpace(want)) {
			return
		}
	}
	t.Fatalf("child ACP %s user prompts = %#v, want %q", sessionID, userEventTexts(loaded.Events), want)
}

func userEventTexts(events []*session.Event) []string {
	out := make([]string, 0)
	for _, event := range events {
		if event != nil && session.EventTypeOf(event) == session.EventTypeUser {
			out = append(out, strings.TrimSpace(session.EventText(event)))
		}
	}
	return out
}

func userPromptsEndWith(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if !strings.HasSuffix(strings.TrimSpace(got[i]), strings.TrimSpace(want[i])) {
			return false
		}
	}
	return true
}

func assertChildSessionIDsEqual(t *testing.T, want map[string]struct{}, got map[string]struct{}) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("child ACP session IDs = %#v, want %#v", got, want)
	}
	for id := range want {
		if _, ok := got[id]; !ok {
			t.Fatalf("child ACP session IDs = %#v, want %#v", got, want)
		}
	}
}

// routedHandoffStarter is the headless equivalent of the TUI/ACP prompt
// boundary: slash commands are consumed by the shared Control router, while
// ordinary prompts return the controller-owned live turn to the surface.
type routedHandoffStarter struct {
	router controlprompt.Router
}

func (s routedHandoffStarter) Submit(ctx context.Context, submission control.Submission) (control.Turn, error) {
	result, err := s.router.Route(ctx, controlprompt.Request{Submission: submission})
	if err != nil {
		return nil, err
	}
	return result.Turn, nil
}
