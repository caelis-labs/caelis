package gatewayapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/projection"
	"github.com/caelis-labs/caelis/control/collaboration"
	"github.com/caelis-labs/caelis/surfaces/headless"
)

func TestBuiltInCollaborationToolsFollowSessionRole(t *testing.T) {
	host := newHostedChildInputTestStack(t, newHostedChildInputTestProvider(t, false))
	parent, child, _ := newHostedChildInputTestTopology(t, host, "tool-roles")
	for _, active := range []session.Session{parent, child} {
		var names []string
		for _, configured := range host.composition.collaborationTools(active) {
			names = append(names, configured.Definition().Name)
		}
		want := []string{"ReadMessages", "ListThreads", "SendMessage"}
		if active.SessionID == parent.SessionID {
			want = append(want, "ReadThread", "WaitThread")
		}
		if !slices.Equal(names, want) {
			t.Fatalf("Session %s collaboration tools = %v, want %v", active.SessionID, names, want)
		}
	}
}

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
	assertHostedChildInputEvent(t, event, m.ID)
	assertProjectedMailIdentity(t, event, m.ID)
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
	// Observe the production backend admission without consuming the mailbox.
	initialTurn := hostedChildCanonicalTurn(t, host, parent.SessionRef, "initial parent input")
	backend := &collaborationAdmissionProbe{collaborationBackend: &collaborationBackend{
		sessions: host.composition.sessions, tasks: host.composition.authorities.taskStore,
		router: &hostedChildInputRouter{runtimes: host.sessionRuntimes},
	}, admitted: make(chan int, 8)}
	service, err := collaboration.Open(filepath.Join(t.TempDir(), "mail.sqlite"), backend)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(t.Context())
	mailDone := make(chan struct{})
	errors := make(chan error, 8)
	go func() { defer close(mailDone); service.Run(runCtx, func(err error) { errors <- err }) }()
	t.Cleanup(func() { cancel(); <-mailDone; _ = service.Close() })
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
	for admitted := 0; admitted < len(mail); {
		select {
		case count := <-backend.admitted:
			admitted += count
		case err := <-errors:
			t.Fatal(err)
		case <-time.After(5 * time.Second):
			t.Fatal("mailbox did not steer the active parent")
		}
	}
	close(provider.releaseFirst)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	turn := initialTurn
	for _, m := range mail {
		event := waitHostedChildInputEvent(t, host, parent.SessionRef, m.Text)
		assertProjectedMailIdentity(t, event, m.ID)
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
		if !strings.Contains(string(before), "Message-ID: "+m.ID) || !strings.Contains(string(before), "From: "+m.From) {
			t.Fatalf("model context lost mail references or sender: %s", before)
		}
		if m.ReplyTo != "" && !strings.Contains(string(before), "In-Reply-To: "+m.ReplyTo) {
			t.Fatalf("model context lost reply reference: %s", before)
		}
	}
	if strings.Contains(string(before), "Internal agent message") || strings.Contains(string(before), `\"from\"`) {
		t.Fatalf("model context repeated sender JSON: %s", before)
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
	// The file store retains each admitted mail record identity, so a reloaded
	// Session projects the same identity the live delivery carried.
	persisted, err := reopened.composition.sessions.Events(t.Context(), session.EventsRequest{SessionRef: parent.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	correlated := map[string]bool{}
	for _, event := range persisted {
		if event == nil || session.EventTypeOf(event) != session.EventTypeContext {
			continue
		}
		for _, m := range mail {
			if session.EventMessageID(event) == m.ID {
				correlated[m.ID] = true
			}
		}
	}
	if len(correlated) != len(mail) {
		t.Fatalf("reloaded correlation identities = %v, want all %d mail records", correlated, len(mail))
	}
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

// assertProjectedMailIdentity requires the canonical event's standard ACP
// projection to carry the mailbox record ID. That projected MessageID is the
// identity the TUI compares across delivery, shared-log reads, and returned mail.
func assertProjectedMailIdentity(t *testing.T, event *session.Event, messageID string) {
	t.Helper()
	updates, err := projection.ProjectEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 {
		t.Fatalf("projected updates = %#v", updates)
	}
	chunk, ok := updates[0].(eventstream.ContentChunk)
	if !ok || chunk.MessageID != messageID {
		t.Fatalf("projected mail identity = %#v, want %q", updates[0], messageID)
	}
}

// collaborationAdmissionProbe observes acceptance through the real Runtime
// backend while leaving Send/claim ordering and removal with the mailbox owner.
type collaborationAdmissionProbe struct {
	*collaborationBackend
	admitted chan int
}

func (b *collaborationAdmissionProbe) Deliver(ctx context.Context, id string, messages []collaboration.Message) error {
	err := b.collaborationBackend.Deliver(ctx, id, messages)
	if err == nil {
		b.admitted <- len(messages)
	}
	return err
}
