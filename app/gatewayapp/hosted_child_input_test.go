package gatewayapp

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	"github.com/caelis-labs/caelis/internal/kernel"
	"github.com/caelis-labs/caelis/surfaces/headless"
)

func TestHostedChildInputSenderPreservesNeutralInputShape(t *testing.T) {
	t.Parallel()

	parent := session.Session{SessionRef: session.SessionRef{SessionID: "parent-1"}}
	child := session.Session{
		SessionRef: session.SessionRef{SessionID: "child-1"},
		Metadata: map[string]any{
			sessionvisibility.MetadataSystemManagedParent: parent.SessionID,
			sessionvisibility.MetadataSystemManagedTask:   "task-1",
		},
	}
	var gotParent, gotChild session.SessionRef
	var gotDelegation string
	var gotInput agent.AgentInput
	sender := hostedChildInputSender{
		parent: parent,
		child:  child,
		route: func(_ context.Context, parentRef, childRef session.SessionRef, delegationID string, input agent.AgentInput) error {
			gotParent, gotChild, gotDelegation = parentRef, childRef, delegationID
			gotInput = agent.CloneAgentInput(input)
			return nil
		},
	}
	if err := sender.SendAgentInput(context.Background(), agent.AgentInput{
		Target: " parent ", Input: " status update ", DisplayInput: " Status update ",
	}); err != nil {
		t.Fatal(err)
	}
	if gotParent.SessionID != parent.SessionID || gotChild.SessionID != child.SessionID || gotDelegation != "task-1" {
		t.Fatalf("route identity = (%#v, %#v, %q)", gotParent, gotChild, gotDelegation)
	}
	if gotInput.Target != "parent" || gotInput.Input != "status update" || gotInput.DisplayInput != "Status update" {
		t.Fatalf("route input = %#v", gotInput)
	}
}

func TestHostedChildInputStartsIdleParentTurnWithTrustedActor(t *testing.T) {
	provider := newHostedChildInputTestProvider(t, false)
	host := newHostedChildInputTestStack(t, provider)
	parent, _, sender := newHostedChildInputTestTopology(t, host, "idle")

	if err := sender.SendAgentInput(context.Background(), agent.AgentInput{Target: agent.AgentInputParent, Input: "child idle input"}); err != nil {
		t.Fatal(err)
	}
	event := waitHostedChildInputEvent(t, host, parent.SessionRef, "child idle input")
	assertHostedChildInputEvent(t, event)
	waitHostedChildParentIdle(t, host, parent.SessionID)
	if got := provider.CallCount(); got != 1 {
		t.Fatalf("model calls = %d, want one ordinary idle prompt", got)
	}
}

func TestHostedChildInputSubmitsToExactActiveParentTurn(t *testing.T) {
	provider := newHostedChildInputTestProvider(t, true)
	host := newHostedChildInputTestStack(t, provider)
	parent, _, sender := newHostedChildInputTestTopology(t, host, "active")

	runDone := make(chan error, 1)
	go func() {
		_, err := runHeadlessOnceForGatewayAppTest(context.Background(), host, parent, "", "initial parent input", headless.Options{})
		runDone <- err
	}()
	select {
	case <-provider.firstRequest:
	case <-time.After(5 * time.Second):
		t.Fatal("parent model request did not start")
	}
	parentGateway := waitHostedChildParentGateway(t, host, parent.SessionID)
	if _, ok := parentGateway.ActiveTurn(parent.SessionID); !ok {
		t.Fatal("parent Turn is not active while provider response is blocked")
	}
	if err := sender.SendAgentInput(context.Background(), agent.AgentInput{Target: agent.AgentInputParent, Input: "child active input"}); err != nil {
		t.Fatal(err)
	}
	close(provider.releaseFirst)
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parent Turn did not complete")
	}
	event := waitHostedChildInputEvent(t, host, parent.SessionRef, "child active input")
	assertHostedChildInputEvent(t, event)
	if got := provider.CallCount(); got != 2 {
		t.Fatalf("model calls = %d, want active submission consumed at the next safe boundary", got)
	}
}

func TestHostedChildInputWaitsForClosingParentBeforeStartingIdleTurn(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sessions := sessionmemory.NewStore(sessionmemory.Config{})
	active, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "owner", PreferredSessionID: "closing-parent",
	})
	if err != nil {
		t.Fatal(err)
	}
	closing := &hostedChildClosingRunner{release: make(chan struct{})}
	runtime := &hostedChildHandoffRuntime{
		session: active, closing: closing, requests: make(chan agent.RunRequest, 2),
	}
	gateway, err := kernel.New(kernel.Config{
		Sessions: sessions, Runtime: runtime, Resolver: hostedChildInputResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	composition := &runtimeComposition{sessions: sessions, gateway: gateway}
	initial, err := gateway.BeginTurn(ctx, kernel.BeginTurnRequest{SessionRef: active.SessionRef, Input: "initial"})
	if err != nil {
		t.Fatal(err)
	}
	defer initial.Handle.Close()
	select {
	case <-runtime.requests:
	case <-ctx.Done():
		t.Fatal("initial parent Runtime did not start")
	}

	routed := make(chan error, 1)
	source := session.ActorRef{Kind: session.ActorKindParticipant, ID: "child-1", Name: "@child"}
	go func() {
		routed <- routeHostedChildInputToParent(ctx, composition, active, source, agent.AgentInput{
			Target: agent.AgentInputParent, Input: "after closing edge",
		})
	}()
	select {
	case err := <-routed:
		t.Fatalf("route returned while the closing Turn still owned admission: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(closing.release)
	if err := <-routed; err != nil {
		t.Fatal(err)
	}
	select {
	case req := <-runtime.requests:
		if req.InputKind != agent.SubmissionKindAgentCommunication ||
			req.Input != "after closing edge" || req.InputActor.ID != source.ID {
			t.Fatalf("replacement idle prompt = %#v, want child input with trusted Actor", req)
		}
	case <-ctx.Done():
		t.Fatal("child input did not start a new idle parent Turn")
	}
}

func newHostedChildInputTestStack(t *testing.T, provider *hostedChildInputTestProvider) *Stack {
	t.Helper()
	root := t.TempDir()
	host, err := newGatewayAppTestStack(t, Config{
		AppName: "caelis-test", UserID: "owner", StoreDir: filepath.Join(root, "store"),
		WorkspaceKey: "workspace", WorkspaceCWD: root, SkillDirs: []string{t.TempDir()},
		Sandbox: SandboxConfig{RequestedType: "host"},
		Model: ModelConfig{
			Provider: "openai-compatible", API: providers.APIOpenAICompatible,
			Model: "hosted-child-input", BaseURL: provider.URL, HTTPClient: provider.Client(),
			Token: "test-token", AuthType: providers.AuthBearerToken,
			ContextWindowTokens: 128000, MaxOutputTok: 1024, Timeout: 5 * time.Second,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	return host
}

func newHostedChildInputTestTopology(t *testing.T, host *Stack, suffix string) (session.Session, session.Session, agent.AgentInputSender) {
	t.Helper()
	ctx := context.Background()
	parent, err := startGatewayAppTestSession(ctx, host, "parent-input-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	child, err := host.composition.sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: host.composition.authorities.appName, UserID: host.composition.authorities.userID,
		Workspace: session.WorkspaceRef{Key: host.composition.workspace.Key, CWD: host.composition.workspace.CWD},
		Metadata: map[string]any{
			sessionvisibility.MetadataSystemManagedAgent:  sessionvisibility.SystemManagedAgentSubagent,
			sessionvisibility.MetadataSystemManagedParent: parent.SessionID,
			sessionvisibility.MetadataSystemManagedTask:   "task-" + suffix,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.composition.sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: parent.SessionRef,
		Binding: session.ParticipantBinding{
			ID: "child-agent-" + suffix, Kind: session.ParticipantKindSubagent, Role: session.ParticipantRoleDelegated,
			SessionID: child.SessionID, DelegationID: "task-" + suffix, AgentName: "orbit", Label: "@orbit",
		},
	}); err != nil {
		t.Fatal(err)
	}
	childRuntime := activateSessionRuntime(t, host, child.SessionID)
	sender := agent.AgentInputSenderFromContext(childRuntime.instance.controlRuntimeContext(ctx, child))
	if sender == nil {
		t.Fatal("hosted child Runtime has no Agent input sender")
	}
	return parent, child, sender
}

func waitHostedChildInputEvent(t *testing.T, host *Stack, ref session.SessionRef, text string) *session.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		events, err := host.composition.sessions.Events(context.Background(), session.EventsRequest{SessionRef: ref})
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event == nil || session.EventTypeOf(event) != session.EventTypeContext {
				continue
			}
			communication := session.ProtocolAgentCommunicationOf(event)
			got := ""
			if communication != nil {
				got = strings.TrimSpace(communication.Text)
			}
			if got == text {
				return event
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("parent Session did not persist input %q", text)
	return nil
}

func assertHostedChildInputEvent(t *testing.T, event *session.Event) {
	t.Helper()
	if event == nil || event.Actor.Kind != session.ActorKindParticipant || !strings.HasPrefix(event.Actor.ID, "child-agent-") {
		t.Fatalf("input event actor = %#v, want trusted child participant", event)
	}
	if session.EventTypeOf(event) != session.EventTypeContext || session.ProtocolUpdateOf(event) != nil {
		t.Fatalf("input event = %#v, want Agent Context without user_message projection", event)
	}
	communication := session.ProtocolAgentCommunicationOf(event)
	if communication == nil || communication.Text == "" || !session.IsClientReplayEvent(event) {
		t.Fatalf("input event protocol = %#v, want replayable Agent communication", event.Protocol)
	}
	if event.Message == nil || !strings.Contains(event.Message.TextContent(), "[Internal agent message]") ||
		!strings.Contains(event.Message.TextContent(), event.Actor.Name) ||
		!strings.Contains(event.Message.TextContent(), communication.Text) {
		t.Fatalf("model message = %#v, want trusted sender header plus original text", event.Message)
	}
	if event.MessageID != "" || strings.Contains(event.IdempotencyKey, "agent-message") {
		t.Fatalf("input event identity = message %q idempotency %q, want ordinary Turn input", event.MessageID, event.IdempotencyKey)
	}
}

func waitHostedChildParentIdle(t *testing.T, host *Stack, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	seenRuntime := false
	for time.Now().Before(deadline) {
		host.sessionRuntimes.mu.RLock()
		active := host.sessionRuntimes.sessions[sessionID]
		host.sessionRuntimes.mu.RUnlock()
		if active != nil && active.instance != nil {
			seenRuntime = true
			if _, ok := active.instance.currentGateway().ActiveTurn(sessionID); !ok {
				return
			}
		}
		if seenRuntime && active == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("parent Session %q remained active", sessionID)
}

func waitHostedChildParentGateway(t *testing.T, host *Stack, sessionID string) *kernel.Gateway {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		host.sessionRuntimes.mu.RLock()
		active := host.sessionRuntimes.sessions[sessionID]
		host.sessionRuntimes.mu.RUnlock()
		if active != nil && active.instance != nil && active.instance.currentGateway() != nil {
			return active.instance.currentGateway()
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("parent Session %q Runtime was not activated", sessionID)
	return nil
}

type hostedChildInputTestProvider struct {
	*gatewayTestHTTPServer
	firstRequest chan struct{}
	releaseFirst chan struct{}
	blockFirst   bool
	firstOnce    sync.Once
	mu           sync.Mutex
	calls        int
}

func newHostedChildInputTestProvider(t *testing.T, blockFirst bool) *hostedChildInputTestProvider {
	t.Helper()
	provider := &hostedChildInputTestProvider{
		firstRequest: make(chan struct{}), releaseFirst: make(chan struct{}), blockFirst: blockFirst,
	}
	provider.gatewayTestHTTPServer = newGatewayTestHTTPServer(http.HandlerFunc(provider.handle))
	if !blockFirst {
		close(provider.releaseFirst)
	}
	t.Cleanup(provider.Close)
	return provider
}

func (p *hostedChildInputTestProvider) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/chat/completions" {
		http.NotFound(w, r)
		return
	}
	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	if call == 1 {
		p.firstOnce.Do(func() { close(p.firstRequest) })
		select {
		case <-p.releaseFirst:
		case <-r.Context().Done():
			return
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	writePluginSystemE2ESSE(w, map[string]any{
		"id": fmt.Sprintf("hosted-child-input-%d", call), "object": "chat.completion.chunk", "model": "hosted-child-input",
		"choices": []map[string]any{{
			"index": 0, "delta": map[string]any{"role": "assistant", "content": fmt.Sprintf("reply-%d", call)}, "finish_reason": "stop",
		}},
	})
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

func (p *hostedChildInputTestProvider) CallCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

type hostedChildInputResolver struct{}

func (hostedChildInputResolver) ResolveTurn(context.Context, kernel.TurnIntent) (kernel.ResolvedTurn, error) {
	return kernel.ResolvedTurn{}, nil
}

type hostedChildHandoffRuntime struct {
	mu       sync.Mutex
	calls    int
	session  session.Session
	closing  *hostedChildClosingRunner
	requests chan agent.RunRequest
}

func (r *hostedChildHandoffRuntime) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	r.requests <- req
	r.mu.Lock()
	r.calls++
	call := r.calls
	r.mu.Unlock()
	if call == 1 {
		return agent.RunResult{Session: r.session, Handle: r.closing}, nil
	}
	return agent.RunResult{Session: r.session, Handle: hostedChildTerminalRunner{}}, nil
}

func (*hostedChildHandoffRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

type hostedChildClosingRunner struct {
	release chan struct{}
}

func (*hostedChildClosingRunner) RunID() string { return "closing-parent-run" }

func (r *hostedChildClosingRunner) Events() iter.Seq2[*session.Event, error] {
	return func(func(*session.Event, error) bool) { <-r.release }
}

func (*hostedChildClosingRunner) Submit(agent.Submission) error {
	return agent.ErrRunInputClosed
}

func (*hostedChildClosingRunner) Cancel() agent.CancelResult {
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}

func (*hostedChildClosingRunner) Close() error { return nil }

type hostedChildTerminalRunner struct{}

func (hostedChildTerminalRunner) RunID() string { return "replacement-parent-run" }

func (hostedChildTerminalRunner) Events() iter.Seq2[*session.Event, error] {
	return func(func(*session.Event, error) bool) {}
}

func (hostedChildTerminalRunner) Submit(agent.Submission) error { return nil }

func (hostedChildTerminalRunner) Cancel() agent.CancelResult {
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}

func (hostedChildTerminalRunner) Close() error { return nil }
