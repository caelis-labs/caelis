package gatewayapp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/collaboration"
	"github.com/caelis-labs/caelis/surfaces/headless"
)

func TestCollaborationMailboxWakesIdleParentAndReplaysCanonicalContext(t *testing.T) {
	provider := newHostedChildInputTestProvider(t, false)
	host := newHostedChildInputTestStack(t, provider)
	parent, _, _ := newHostedChildInputTestTopology(t, host, "mailbox")
	service := host.CollaborationService()
	m, err := service.Send(t.Context(), collaboration.Identity{Session: parent.SessionID, Member: "orbit"}, "parent", "mailbox-roundtrip-evidence", "")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(m)
	event := waitHostedChildInputEvent(t, host, parent.SessionRef, string(body))
	assertHostedChildInputEvent(t, event)
	waitHostedChildParentIdle(t, host, parent.SessionID)
	if got, err := service.Receive(t.Context(), collaboration.Identity{Session: parent.SessionID, Member: "parent"}); err != nil || len(got) != 0 {
		t.Fatalf("delivered mail remained: %v %v", got, err)
	}
	storeDir, workspace := host.composition.authorities.storeDir, host.composition.workspace
	if err = host.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newGatewayAppTestStack(t, Config{StoreDir: storeDir, WorkspaceKey: workspace.Key, WorkspaceCWD: workspace.CWD, ResolveProviderHTTPClient: func(context.Context, ModelConfig) (*http.Client, error) { return provider.Client(), nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if _, err = runHeadlessOnceForGatewayAppTest(t.Context(), reopened, parent, parent.SessionID, "continue", headless.Options{}); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	messages := string(provider.lastMessages)
	provider.mu.Unlock()
	if strings.Count(messages, "mailbox-roundtrip-evidence") != 1 {
		t.Fatalf("canonical model context did not retain exactly one delivered message: %s", messages)
	}
	if provider.CallCount() != 2 {
		t.Fatalf("model calls %d, want delivery plus resumed turn", provider.CallCount())
	}
}

func TestCollaborationCredentialRejectsClosedWorkSession(t *testing.T) {
	provider := newHostedChildInputTestProvider(t, false)
	host := newHostedChildInputTestStack(t, provider)
	parent, child, _ := newHostedChildInputTestTopology(t, host, "grant-close")
	service := host.CollaborationService()
	grant := service.Prepare(collaboration.Identity{Session: parent.SessionID, Member: "orbit"}, "task-grant-close")
	if err := grant.Bind(child.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Authenticate(t.Context(), grant.Token()); err != nil {
		t.Fatal(err)
	}
	active, err := host.composition.sessions.Session(t.Context(), parent.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = appserver.CloseSession(t.Context(), host.composition.sessions, active, "test"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.Authenticate(t.Context(), grant.Token()); err == nil {
		t.Fatal("closed Session retained collaboration authority")
	}
	if _, err = host.composition.sessions.Session(t.Context(), child.SessionRef); err != nil {
		t.Fatal("child history removed")
	}
}

func TestCollaborationRemovalPreservesHistoryAndRevokesGrant(t *testing.T) {
	provider := newHostedChildInputTestProvider(t, false)
	host := newHostedChildInputTestStack(t, provider)
	parent, child, _ := newHostedChildInputTestTopology(t, host, "remove")
	if err := host.composition.authorities.taskStore.Upsert(t.Context(), &task.Entry{TaskID: "task-remove", Handle: "orbit", Kind: task.KindSubagent, Session: parent.SessionRef, State: task.StateCompleted}); err != nil {
		t.Fatal(err)
	}
	service := host.CollaborationService()
	grant := service.Prepare(collaboration.Identity{Session: parent.SessionID, Member: "orbit"}, "task-remove")
	if err := grant.Bind(child.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := service.Remove(t.Context(), collaboration.Identity{Session: parent.SessionID, Member: "parent"}, "orbit"); err != nil {
		t.Fatal(err)
	}
	threads, err := service.List(t.Context(), collaboration.Identity{Session: parent.SessionID, Member: "parent"})
	if err != nil || len(threads) != 1 {
		t.Fatalf("threads %v %v", threads, err)
	}
	if _, _, err = service.Authenticate(t.Context(), grant.Token()); err == nil {
		t.Fatal("removed participant retained access")
	}
	if _, err = host.composition.sessions.Session(t.Context(), child.SessionRef); err != nil {
		t.Fatal("child history removed")
	}
}

func TestCollaborationReadsCompletedResultAndSuppressesSeenCursor(t *testing.T) {
	provider := newHostedChildInputTestProvider(t, false)
	host := newHostedChildInputTestStack(t, provider)
	parent, _, _ := newHostedChildInputTestTopology(t, host, "read-result")
	entry := &task.Entry{TaskID: "task-read-result", Handle: "orbit", Kind: task.KindSubagent, Session: parent.SessionRef, State: task.StateCompleted, Result: map[string]any{"final_message": "producer final answer"}}
	if err := host.composition.authorities.taskStore.Upsert(t.Context(), entry); err != nil {
		t.Fatal(err)
	}
	service := host.CollaborationService()
	identity := collaboration.Identity{Session: parent.SessionID, Member: "parent"}
	target := collaboration.Target{Handle: "orbit"}
	read, err := service.Read(t.Context(), identity, target)
	if err != nil || read.Output != "producer final answer" || read.Cursor == 0 {
		t.Fatalf("Read = %#v, %v", read, err)
	}
	waited, err := service.WaitThreads(t.Context(), identity, []collaboration.Target{target}, 0)
	if err != nil || waited.Reason != "thread" || len(waited.Threads) != 1 || waited.Threads[0].Output != read.Output {
		t.Fatalf("WaitThreads = %#v, %v", waited, err)
	}
	target.After = read.Cursor
	read, err = service.Read(t.Context(), identity, target)
	if err != nil || read.Output != "" {
		t.Fatalf("seen Read = %#v, %v", read, err)
	}
	waited, err = service.WaitThreads(t.Context(), identity, []collaboration.Target{target}, 0)
	if err != nil || waited.Reason != "timeout" {
		t.Fatalf("seen WaitThreads = %#v, %v", waited, err)
	}
}
