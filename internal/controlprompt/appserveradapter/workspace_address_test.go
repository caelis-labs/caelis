package appserveradapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	controlstatus "github.com/caelis-labs/caelis/control/status"
)

// Status and completion reads must carry one coherent workspace key/CWD pair
// across selection, reset, and failed resume. A persistent Host rejects a mixed
// pair as a workspace identity conflict.

func TestSessionClientAdapterWorkspaceAddressFollowsSelectedSession(t *testing.T) {
	launchDir := t.TempDir()
	botDir := t.TempDir()
	sessions := &sessionClientAdapterTestClient{
		state: appserver.SessionState{
			SessionID: "bot-conversation", WorkspaceKey: "bot-1", CWD: botDir, Revision: 3,
			Controller: session.ControllerBinding{EpochID: "epoch-1"},
		},
		reconnectSubscriptions: []*sessionClientAdapterTestSubscription{
			newSessionClientAdapterTestSubscription(),
		},
	}
	status := &recordingStatusClient{}
	completion := &recordingCompletionClient{}
	adapter, err := NewAppServerAdapter(AppServerAdapterConfig{
		WorkspaceKey: "workspace", WorkspaceDir: launchDir, Surface: "cli-bot",
		Sessions: sessions, Participants: &sessionClientAdapterTestParticipantClient{},
		Status: status, Configuration: sessionClientAdapterTestConfigurationClient{},
		Agents: &sessionClientAdapterTestAgentClient{}, Completion: completion,
		Plugins: &sessionClientAdapterTestPluginClient{}, RequireExistingSession: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })

	// No conversation is selected yet, so Host reads address the launch workspace.
	if _, err := adapter.LightweightStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status.request.SessionID != "" || status.request.WorkspaceKey != "workspace" || status.request.CWD != launchDir {
		t.Fatalf("status before attach = %#v", status.request)
	}

	if _, err := adapter.ResumeSession(context.Background(), "bot-conversation"); err != nil {
		t.Fatal(err)
	}

	// Once attached, every read must carry the conversation's own key *and* CWD.
	if _, err := adapter.LightweightStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status.request.SessionID != "bot-conversation" || status.request.WorkspaceKey != "bot-1" || status.request.CWD != botDir {
		t.Fatalf("status after attach = %#v", status.request)
	}
	if _, err := adapter.CompleteSlashArg(context.Background(), "model", "", 20); err != nil {
		t.Fatal(err)
	}
	if completion.slash.SessionID != "bot-conversation" ||
		completion.slash.WorkspaceKey != "bot-1" || completion.slash.CWD != botDir {
		t.Fatalf("slash completion after attach = %#v", completion.slash)
	}
	if got := adapter.WorkspaceDir(); got != botDir {
		t.Fatalf("WorkspaceDir() after attach = %q, want %q", got, botDir)
	}

	// Clearing the selection keeps the current workspace, so an unbound read
	// still describes the workspace the Session lifecycle last selected.
	if err := adapter.ResetSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.LightweightStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status.request.SessionID != "" || status.request.WorkspaceKey != "bot-1" || status.request.CWD != botDir {
		t.Fatalf("status after reset = %#v", status.request)
	}
	if got := adapter.WorkspaceDir(); got != botDir {
		t.Fatalf("WorkspaceDir() after reset = %q, want %q", got, botDir)
	}
}

func TestSessionClientAdapterFailedResumeKeepsSelectedWorkspaceAddress(t *testing.T) {
	firstDir := t.TempDir()
	sessions := &sessionClientAdapterTestClient{
		state: appserver.SessionState{
			SessionID: "session-1", WorkspaceKey: "workspace-1", CWD: firstDir, Revision: 4,
			Controller: session.ControllerBinding{EpochID: "epoch-1"},
		},
		reconnectSubscriptions: []*sessionClientAdapterTestSubscription{
			newSessionClientAdapterTestSubscription(),
		},
	}
	status := &recordingStatusClient{}
	adapter, err := NewAppServerAdapter(AppServerAdapterConfig{
		WorkspaceKey: "workspace", WorkspaceDir: t.TempDir(), Surface: "cli-tui",
		Sessions: sessions, Participants: &sessionClientAdapterTestParticipantClient{},
		Status: status, Configuration: sessionClientAdapterTestConfigurationClient{},
		Agents: &sessionClientAdapterTestAgentClient{}, Completion: &recordingCompletionClient{},
		Plugins: &sessionClientAdapterTestPluginClient{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })

	if _, err := adapter.ResumeSession(context.Background(), "session-1"); err != nil {
		t.Fatal(err)
	}

	// A failed transition must not publish a half-applied address. The committed
	// selection keeps answering with its own key and CWD.
	sessions.reconnectErr = errors.New("reconnect failed")
	if _, err := adapter.ResumeSession(context.Background(), "session-2"); err == nil {
		t.Fatal("failed resume reported success")
	}
	if got := adapter.clientSessionID(); got != "session-1" {
		t.Fatalf("selected Session after failed resume = %q, want session-1", got)
	}
	if _, err := adapter.LightweightStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status.request.SessionID != "session-1" || status.request.WorkspaceKey != "workspace-1" || status.request.CWD != firstDir {
		t.Fatalf("status after failed resume = %#v", status.request)
	}
}

func TestSessionClientAdapterRetainsWorkspaceAddressAcrossResetAndSessionCreation(t *testing.T) {
	resumedDir := t.TempDir()
	createdDir := t.TempDir()
	sessions := &sessionClientAdapterTestClient{
		state: appserver.SessionState{
			SessionID: "session-resumed", WorkspaceKey: "workspace-resumed", CWD: resumedDir, Revision: 4,
			Controller: session.ControllerBinding{EpochID: "epoch-1"},
		},
		createSessionID: "session-created",
		reconnectSubscriptions: []*sessionClientAdapterTestSubscription{
			newSessionClientAdapterTestSubscription(),
		},
	}
	status := &recordingStatusClient{}
	adapter, err := NewAppServerAdapter(AppServerAdapterConfig{
		WorkspaceKey: "workspace", WorkspaceDir: t.TempDir(), Surface: "cli-tui",
		Sessions: sessions, Participants: &sessionClientAdapterTestParticipantClient{},
		Status: status, Configuration: sessionClientAdapterTestConfigurationClient{},
		Agents: &sessionClientAdapterTestAgentClient{}, Completion: &recordingCompletionClient{},
		Plugins: &sessionClientAdapterTestPluginClient{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	ctx := context.Background()

	if _, err := adapter.ResumeSession(ctx, "session-resumed"); err != nil {
		t.Fatal(err)
	}
	// Clearing the selection keeps the workspace the Session lifecycle selected,
	// matching the navigation semantics an ordinary /new relies on.
	if err := adapter.ResetSession(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.LightweightStatus(ctx); err != nil {
		t.Fatal(err)
	}
	if status.request.SessionID != "" || status.request.WorkspaceKey != "workspace-resumed" || status.request.CWD != resumedDir {
		t.Fatalf("status after reset = %#v", status.request)
	}

	// The next ordinary Session is created in that same workspace, never as a
	// mixed pair with the launching workspace key.
	sessions.state = appserver.SessionState{
		SessionID: "session-created", WorkspaceKey: "workspace-created", CWD: createdDir, Revision: 5,
		Controller: session.ControllerBinding{EpochID: "epoch-2"},
	}
	if _, err := adapter.ensureSessionForMainPrompt(ctx); err != nil {
		t.Fatal(err)
	}
	if sessions.create.WorkspaceKey != "workspace-resumed" || sessions.create.CWD != resumedDir {
		t.Fatalf("CreateSession workspace = %#v, want workspace-resumed at %q", sessions.create, resumedDir)
	}
	if _, err := adapter.LightweightStatus(ctx); err != nil {
		t.Fatal(err)
	}
	if status.request.SessionID != "session-created" || status.request.WorkspaceKey != "workspace-created" || status.request.CWD != createdDir {
		t.Fatalf("status after new Session = %#v", status.request)
	}
}

func TestSessionClientAdapterWorkspaceAddressStaysCoherentUnderSessionTransitions(t *testing.T) {
	launchDir := t.TempDir()
	type workspaceAddress struct {
		sessionID string
		key       string
		cwd       string
	}
	targets := []workspaceAddress{
		{sessionID: "session-a", key: "workspace-a", cwd: t.TempDir()},
		{sessionID: "session-b", key: "workspace-b", cwd: t.TempDir()},
		{sessionID: "session-c", key: "workspace-c", cwd: t.TempDir()},
	}
	coherent := map[session.WorkspaceRef]bool{
		{Key: "workspace", CWD: launchDir}: true,
	}
	states := map[string]appserver.SessionState{}
	for _, target := range targets {
		coherent[session.WorkspaceRef{Key: target.key, CWD: target.cwd}] = true
		states[target.sessionID] = appserver.SessionState{
			SessionID: target.sessionID, WorkspaceKey: target.key, CWD: target.cwd, Revision: 5,
			Controller: session.ControllerBinding{EpochID: "epoch-" + target.sessionID},
		}
	}

	status := &workspaceAddressStatusProbe{}
	completion := &workspaceAddressCompletionProbe{}
	adapter, err := NewAppServerAdapter(AppServerAdapterConfig{
		WorkspaceKey: "workspace", WorkspaceDir: launchDir, Surface: "cli-tui",
		Sessions:     &workspaceAddressSessionClient{states: states},
		Participants: &sessionClientAdapterTestParticipantClient{}, Status: status,
		Configuration: sessionClientAdapterTestConfigurationClient{},
		Agents:        &sessionClientAdapterTestAgentClient{}, Completion: completion,
		Plugins: &sessionClientAdapterTestPluginClient{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })

	ctx := context.Background()
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		for i := 0; i < 200; i++ {
			target := targets[i%len(targets)]
			if _, err := adapter.ResumeSession(ctx, target.sessionID); err != nil {
				t.Errorf("ResumeSession(%s): %v", target.sessionID, err)
				return
			}
			if i%4 == 3 {
				if err := adapter.ResetSession(ctx); err != nil {
					t.Errorf("ResetSession: %v", err)
					return
				}
			}
		}
	}()
	go func() {
		defer wait.Done()
		for i := 0; i < 200; i++ {
			if _, err := adapter.LightweightStatus(ctx); err != nil {
				t.Errorf("LightweightStatus: %v", err)
				return
			}
			if _, err := adapter.CompleteSlashArg(ctx, "model", "", 5); err != nil {
				t.Errorf("CompleteSlashArg: %v", err)
				return
			}
			_ = adapter.WorkspaceDir()
		}
	}()
	wait.Wait()

	// Every read must describe one real workspace. A mixed address (one
	// workspace's key with another's CWD) is the rejection the Host reports.
	for _, address := range status.addresses() {
		if !coherent[address] {
			t.Fatalf("status addressed a mixed workspace %#v", address)
		}
	}
	for _, address := range completion.addresses() {
		if !coherent[address] {
			t.Fatalf("completion addressed a mixed workspace %#v", address)
		}
	}
	if len(status.addresses()) == 0 || len(completion.addresses()) == 0 {
		t.Fatal("no Host read observed")
	}
}

// workspaceAddressSessionClient serves one prepared state per Session ID so
// transitions can move the selected address between distinct workspaces.
type workspaceAddressSessionClient struct {
	appserver.SessionClient
	states map[string]appserver.SessionState
}

func (c *workspaceAddressSessionClient) Reconnect(_ context.Context, request appserver.ReconnectRequest) (appserver.ReconnectResult, error) {
	sessionID := strings.TrimSpace(request.SessionID)
	state, ok := c.states[sessionID]
	if !ok {
		return appserver.ReconnectResult{}, fmt.Errorf("address test client: unknown Session %q", sessionID)
	}
	return appserver.ReconnectResult{
		State: state, Subscription: newSessionClientAdapterTestSubscription(),
	}, nil
}

type workspaceAddressStatusProbe struct {
	mu    sync.Mutex
	reads []session.WorkspaceRef
}

func (p *workspaceAddressStatusProbe) SessionStatus(_ context.Context, request appserver.StatusRequest) (controlstatus.StatusSnapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reads = append(p.reads, session.WorkspaceRef{Key: request.WorkspaceKey, CWD: request.CWD})
	return controlstatus.StatusSnapshot{}, nil
}

func (p *workspaceAddressStatusProbe) addresses() []session.WorkspaceRef {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]session.WorkspaceRef(nil), p.reads...)
}

type workspaceAddressCompletionProbe struct {
	appserver.CompletionClient
	mu    sync.Mutex
	reads []session.WorkspaceRef
}

func (p *workspaceAddressCompletionProbe) CompleteSlashArg(_ context.Context, request appserver.CompletionRequest) ([]appserver.SlashArgCandidate, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reads = append(p.reads, session.WorkspaceRef{Key: request.WorkspaceKey, CWD: request.CWD})
	return []appserver.SlashArgCandidate{{Value: "mimo"}}, nil
}

func (p *workspaceAddressCompletionProbe) addresses() []session.WorkspaceRef {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]session.WorkspaceRef(nil), p.reads...)
}
