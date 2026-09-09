package gatewayapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

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
	event := waitHostedChildInputEvent(t, host, parent.SessionRef, m.Text)
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
	if _, _, err = service.Authenticate(t.Context(), grant.Token()); !errors.Is(err, collaboration.ErrSessionClosed) {
		t.Fatalf("closed Session did not provide permanent closure evidence: %v", err)
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

func TestCollaborationQueuedMultipleSendersUseOneTurnAndPersistModelContext(t *testing.T) {
	provider := newHostedChildInputTestProvider(t, true)
	host := newHostedChildInputTestStack(t, provider)
	parent, _, _ := newHostedChildInputTestTopology(t, host, "mailbox-batch")
	source := session.ParticipantBinding{ID: "sibling-batch", Kind: session.ParticipantKindSubagent, Role: session.ParticipantRoleDelegated, SessionID: "sibling-session-batch", DelegationID: "sibling-task-batch", AgentName: "nova", Label: "@nova"}
	if _, err := host.composition.sessions.PutParticipant(t.Context(), session.PutParticipantRequest{SessionRef: parent.SessionRef, Binding: source}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := runHeadlessOnceForGatewayAppTest(t.Context(), host, parent, "", "initial parent input", headless.Options{})
		done <- err
	}()
	select {
	case <-provider.firstRequest:
	case <-time.After(5 * time.Second):
		t.Fatal("initial turn not running")
	}
	// Automatic parent delivery is unavailable while this first Turn is active.
	service := host.CollaborationService()
	var mail []collaboration.Message
	for i, from := range []string{"orbit", "nova", "orbit"} {
		replyTo := ""
		if len(mail) > 0 {
			replyTo = mail[len(mail)-1].ID
		}
		m, err := service.Send(t.Context(), collaboration.Identity{Session: parent.SessionID, Member: from}, "parent", fmt.Sprintf("batch-roundtrip-%d", i), replyTo)
		if err != nil {
			t.Fatal(err)
		}
		mail = append(mail, m)
	}
	close(provider.releaseFirst)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var turn string
	for _, m := range mail {
		event := waitHostedChildInputEvent(t, host, parent.SessionRef, m.Text)
		if communication := session.ProtocolAgentCommunicationOf(event); communication == nil || communication.Text != m.Text {
			t.Fatalf("display exposed mailbox encoding: %#v", event)
		}
		if event.Scope == nil {
			t.Fatal("missing Turn identity")
		}
		if turn == "" {
			turn = event.Scope.TurnID
		} else if event.Scope.TurnID != turn {
			t.Fatalf("batch split across Turns: %s and %s", turn, event.Scope.TurnID)
		}
		if event.Actor.Name != m.From {
			t.Fatalf("sender changed: %#v for %#v", event.Actor, m)
		}
	}
	waitHostedChildParentIdle(t, host, parent.SessionID)
	if got := provider.CallCount(); got != 2 {
		t.Fatalf("model calls %d, want initial plus one batch", got)
	}
	provider.mu.Lock()
	before := append(json.RawMessage(nil), provider.lastMessages...)
	provider.mu.Unlock()
	previous := -1
	for _, m := range mail {
		// A reply reference may repeat an earlier ID, so compare the ordered message bodies.
		marker := m.Text
		position := strings.Index(string(before), marker)
		if position <= previous || strings.Count(string(before), marker) != 1 {
			t.Fatalf("model context lost mail order: %s", before)
		}
		previous = position
	}
	storeDir, workspace := host.composition.authorities.storeDir, host.composition.workspace
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newGatewayAppTestStack(t, Config{StoreDir: storeDir, WorkspaceKey: workspace.Key, WorkspaceCWD: workspace.CWD, ResolveProviderHTTPClient: func(context.Context, ModelConfig) (*http.Client, error) { return provider.Client(), nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := runHeadlessOnceForGatewayAppTest(t.Context(), reopened, parent, parent.SessionID, "continue", headless.Options{}); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	after := append(json.RawMessage(nil), provider.lastMessages...)
	provider.mu.Unlock()
	var beforeMessages, afterMessages []json.RawMessage
	if err := json.Unmarshal(before, &beforeMessages); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after, &afterMessages); err != nil {
		t.Fatal(err)
	}
	// Match actual provider-visible mail messages before and after file-store reload.
	extract := func(messages []json.RawMessage) []string {
		var result []string
		for _, message := range messages {
			if strings.Contains(string(message), "batch-roundtrip-") {
				result = append(result, string(message))
			}
		}
		return result
	}
	if got, want := extract(afterMessages), extract(beforeMessages); len(want) != 3 || !reflect.DeepEqual(got, want) {
		t.Fatalf("model-context roundtrip changed ordered mail: before=%s after=%s", before, after)
	}
	if provider.CallCount() != 3 {
		t.Fatalf("unexpected turns: %d", provider.CallCount())
	}
}
