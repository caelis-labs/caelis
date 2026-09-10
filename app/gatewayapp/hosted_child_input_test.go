package gatewayapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
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

func TestHostedChildInputBatchStartsIdleParentTurnWithTrustedSources(t *testing.T) {
	provider := newHostedChildInputTestProvider(t, false)
	host := newHostedChildInputTestStack(t, provider)
	parent, _, _ := newHostedChildInputTestTopology(t, host, "batch-idle")
	second := addHostedChildParticipant(t, host, parent, "batch-idle-b", "zenith")
	parent = hostedChildReloadSession(t, host, parent.SessionRef)
	orbit := hostedChildBinding(t, parent, "child-agent-batch-idle")
	zenith := hostedChildBinding(t, parent, second.ID)
	parentRuntime := activateSessionRuntime(t, host, parent.SessionID)

	err := parentRuntime.instance.engine.SubmitAgentInputBatch(
		context.Background(),
		parent.SessionRef,
		agent.AgentInputParent,
		[]agent.AgentInputBatchEntry{
			{Message: agent.AgentCommunicationInput{Input: "from orbit"}, Participant: &orbit},
			{Message: agent.AgentCommunicationInput{Input: "from zenith"}, Participant: &zenith},
		},
		func(ctx context.Context, current session.Session, messages []agent.AgentCommunicationInput) error {
			return routeHostedChildInputBatchToParent(ctx, &parentRuntime.instance.runtimeComposition, current, messages)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	first := waitHostedChildInputEvent(t, host, parent.SessionRef, "from orbit")
	secondEvent := waitHostedChildInputEvent(t, host, parent.SessionRef, "from zenith")
	assertHostedChildInputEvent(t, first)
	assertHostedChildInputEvent(t, secondEvent)
	if first.Actor.ID != orbit.ID || secondEvent.Actor.ID != zenith.ID {
		t.Fatalf("durable sources = (%#v, %#v), want orbit then zenith", first.Actor, secondEvent.Actor)
	}
	if first.Scope == nil || secondEvent.Scope == nil || first.Scope.TurnID != secondEvent.Scope.TurnID {
		t.Fatalf("batch used different Turns: (%#v, %#v)", first.Scope, secondEvent.Scope)
	}
	waitHostedChildParentIdle(t, host, parent.SessionID)
	if got := provider.CallCount(); got != 1 {
		t.Fatalf("model calls = %d, want one follow-up Turn", got)
	}
	payload := string(provider.LastMessages())
	if !strings.Contains(payload, "from orbit") || !strings.Contains(payload, "from zenith") ||
		!strings.Contains(payload, "orbit") || !strings.Contains(payload, "zenith") {
		t.Fatalf("model request = %s, want both ordered source-attributed messages", payload)
	}
}

func TestHostedChildInputBatchSteersActiveParentWithTrustedSources(t *testing.T) {
	provider := newHostedChildInputTestProvider(t, true)
	host := newHostedChildInputTestStack(t, provider)
	parent, _, _ := newHostedChildInputTestTopology(t, host, "batch-wait")
	second := addHostedChildParticipant(t, host, parent, "batch-wait-b", "zenith")
	parent = hostedChildReloadSession(t, host, parent.SessionRef)
	orbit := hostedChildBinding(t, parent, "child-agent-batch-wait")
	zenith := hostedChildBinding(t, parent, second.ID)

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
	firstPayload := string(provider.LastMessages())
	initialTurn := hostedChildCanonicalTurn(t, host, parent.SessionRef, "initial parent input")
	parentRuntime := activateSessionRuntime(t, host, parent.SessionID)
	routed := make(chan error, 1)
	go func() {
		routed <- parentRuntime.instance.engine.SubmitAgentInputBatch(
			context.Background(),
			parent.SessionRef,
			agent.AgentInputParent,
			[]agent.AgentInputBatchEntry{
				{Message: agent.AgentCommunicationInput{Input: "idle from orbit"}, Participant: &orbit},
				{Message: agent.AgentCommunicationInput{Input: "idle from zenith"}, Participant: &zenith},
			},
			func(ctx context.Context, current session.Session, messages []agent.AgentCommunicationInput) error {
				return routeHostedChildInputBatchToParent(ctx, &parentRuntime.instance.runtimeComposition, current, messages)
			},
		)
	}()
	select {
	case err := <-routed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("batch waited for parent completion instead of admitting steering")
	}
	if strings.Contains(firstPayload, "idle from orbit") || strings.Contains(firstPayload, "idle from zenith") {
		t.Fatalf("active Turn received bulk mail: %s", firstPayload)
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
	first := waitHostedChildInputEvent(t, host, parent.SessionRef, "idle from orbit")
	secondEvent := waitHostedChildInputEvent(t, host, parent.SessionRef, "idle from zenith")
	assertHostedChildInputEvent(t, first)
	assertHostedChildInputEvent(t, secondEvent)
	if first.Actor.ID != orbit.ID || secondEvent.Actor.ID != zenith.ID {
		t.Fatalf("durable sources = (%#v, %#v), want orbit then zenith", first.Actor, secondEvent.Actor)
	}
	if first.Scope == nil || secondEvent.Scope == nil || first.Scope.TurnID != secondEvent.Scope.TurnID || first.Scope.TurnID != initialTurn {
		t.Fatalf("batch changed active Turn: (%#v, %#v), original %s", first.Scope, secondEvent.Scope, initialTurn)
	}
	waitHostedChildParentIdle(t, host, parent.SessionID)
	if got := provider.CallCount(); got != 2 {
		t.Fatalf("model calls = %d, want initial response and continuation in the same Turn", got)
	}
	payload := string(provider.LastMessages())
	orbitAt := strings.Index(payload, "idle from orbit")
	zenithAt := strings.Index(payload, "idle from zenith")
	if orbitAt < 0 || zenithAt < 0 || zenithAt < orbitAt {
		t.Fatalf("idle batch prompt = %s, want orbit then zenith", payload)
	}
}

func TestHostedChildInputDetachedSourceDoesNotPersistParentContext(t *testing.T) {
	provider := newHostedChildInputTestProvider(t, false)
	host := newHostedChildInputTestStack(t, provider)
	parent, _, sender := newHostedChildInputTestTopology(t, host, "detached")
	if _, err := host.composition.sessions.RemoveParticipant(context.Background(), session.RemoveParticipantRequest{
		SessionRef: parent.SessionRef, ParticipantID: "child-agent-detached",
	}); err != nil {
		t.Fatal(err)
	}
	err := sender.SendAgentInput(context.Background(), agent.AgentInput{
		Target: agent.AgentInputParent, Input: "must not enter parent context",
	})
	if !errorcode.Is(err, errorcode.PermissionDenied) {
		t.Fatalf("SendAgentInput() error = %v, want permission denied", err)
	}
	events, err := host.composition.sessions.Events(context.Background(), session.EventsRequest{SessionRef: parent.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event != nil && session.EventTypeOf(event) == session.EventTypeContext {
			t.Fatalf("detached child persisted parent Context event: %#v", event)
		}
	}
	if got := provider.CallCount(); got != 0 {
		t.Fatalf("detached child triggered %d parent model calls", got)
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

func TestHostedChildInputBatchRejectsEmptyOrUntrustedAdmission(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	composition, active, runtime := newHostedChildRouteFixture(t, ctx)
	source := session.ActorRef{Kind: session.ActorKindParticipant, ID: "child-1", Name: "@child"}

	if err := routeHostedChildInputBatchToParent(ctx, composition, active, nil); !errorcode.Is(err, errorcode.InvalidArgument) {
		t.Fatalf("empty batch error = %v, want invalid argument", err)
	}
	if err := routeHostedChildInputBatchToParent(ctx, composition, active, []agent.AgentCommunicationInput{
		{Source: source, Input: "ok"},
		{Source: source},
	}); !errorcode.Is(err, errorcode.InvalidArgument) {
		t.Fatalf("empty member error = %v, want invalid argument", err)
	}
	if err := routeHostedChildInputBatchToParent(ctx, composition, active, []agent.AgentCommunicationInput{
		{Source: session.ActorRef{Kind: session.ActorKindUser, ID: "user-1", Name: "user"}, Input: "nope"},
	}); !errorcode.Is(err, errorcode.InvalidArgument) {
		t.Fatalf("user source error = %v, want invalid argument", err)
	}
	select {
	case req := <-runtime.requests:
		t.Fatalf("rejected batch started a parent Turn: %#v", req)
	default:
	}
}

func TestHostedChildInputBatchDetachedMemberDoesNotAdmitPartialParentTurn(t *testing.T) {
	provider := newHostedChildInputTestProvider(t, false)
	host := newHostedChildInputTestStack(t, provider)
	parent, _, _ := newHostedChildInputTestTopology(t, host, "batch-partial")
	second := addHostedChildParticipant(t, host, parent, "batch-partial-b", "zenith")
	parent = hostedChildReloadSession(t, host, parent.SessionRef)
	orbit := hostedChildBinding(t, parent, "child-agent-batch-partial")
	zenith := hostedChildBinding(t, parent, second.ID)
	if _, err := host.composition.sessions.RemoveParticipant(context.Background(), session.RemoveParticipantRequest{
		SessionRef: parent.SessionRef, ParticipantID: zenith.ID,
	}); err != nil {
		t.Fatal(err)
	}
	parentRuntime := activateSessionRuntime(t, host, parent.SessionID)
	err := parentRuntime.instance.engine.SubmitAgentInputBatch(
		context.Background(),
		parent.SessionRef,
		agent.AgentInputParent,
		[]agent.AgentInputBatchEntry{
			{Message: agent.AgentCommunicationInput{Input: "from orbit"}, Participant: &orbit},
			{Message: agent.AgentCommunicationInput{Input: "from detached zenith"}, Participant: &zenith},
		},
		func(ctx context.Context, current session.Session, messages []agent.AgentCommunicationInput) error {
			return routeHostedChildInputBatchToParent(ctx, &parentRuntime.instance.runtimeComposition, current, messages)
		},
	)
	if !errorcode.Is(err, errorcode.PermissionDenied) {
		t.Fatalf("SubmitAgentInputBatch() error = %v, want permission denied for detached member", err)
	}
	events, err := host.composition.sessions.Events(context.Background(), session.EventsRequest{SessionRef: parent.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event != nil && session.EventTypeOf(event) == session.EventTypeContext {
			t.Fatalf("partial batch persisted parent Context event: %#v", event)
		}
	}
	if got := provider.CallCount(); got != 0 {
		t.Fatalf("partial batch triggered %d parent model calls", got)
	}
}

func TestHostedChildInputWaitsForClosingParentBeforeStartingIdleTurn(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch-%v", batch), func(t *testing.T) {
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
				if batch {
					routed <- routeHostedChildInputBatchToParent(ctx, composition, active, []agent.AgentCommunicationInput{
						{Source: source, Input: "after closing edge"}, {Source: source, Input: "second message"},
					})
				} else {
					routed <- routeHostedChildInputToParent(ctx, composition, active, source, agent.AgentInput{Target: agent.AgentInputParent, Input: "after closing edge"})
				}
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
				if batch {
					if req.InputKind != agent.SubmissionKindAgentCommunication || len(req.Inputs) != 2 || req.Inputs[0].Input != "after closing edge" || req.Inputs[1].Input != "second message" || req.Inputs[0].Source.ID != source.ID {
						t.Fatalf("replacement batch = %#v", req)
					}
					return
				}
				if req.InputKind != agent.SubmissionKindAgentCommunication ||
					req.Input != "after closing edge" || req.InputActor.ID != source.ID {
					t.Fatalf("replacement idle prompt = %#v, want child input with trusted Actor", req)
				}
			case <-ctx.Done():
				t.Fatal("child input did not start a new idle parent Turn")
			}
		})
	}
}

func newHostedChildRouteFixture(t *testing.T, ctx context.Context) (*runtimeComposition, session.Session, *hostedChildHandoffRuntime) {
	t.Helper()
	sessions := sessionmemory.NewStore(sessionmemory.Config{})
	active, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "owner", PreferredSessionID: "admission-parent",
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &hostedChildHandoffRuntime{
		session: active, requests: make(chan agent.RunRequest, 1),
	}
	gateway, err := kernel.New(kernel.Config{
		Sessions: sessions, Runtime: runtime, Resolver: hostedChildInputResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &runtimeComposition{sessions: sessions, gateway: gateway}, active, runtime
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

func addHostedChildParticipant(t *testing.T, host *Stack, parent session.Session, suffix, name string) session.ParticipantBinding {
	t.Helper()
	binding := session.ParticipantBinding{
		ID: "child-agent-" + suffix, Kind: session.ParticipantKindSubagent, Role: session.ParticipantRoleDelegated,
		SessionID: "child-" + suffix, DelegationID: "task-" + suffix, AgentName: name, Label: "@" + name,
	}
	if _, err := host.composition.sessions.PutParticipant(context.Background(), session.PutParticipantRequest{
		SessionRef: parent.SessionRef, Binding: binding,
	}); err != nil {
		t.Fatal(err)
	}
	return binding
}

func hostedChildReloadSession(t *testing.T, host *Stack, ref session.SessionRef) session.Session {
	t.Helper()
	active, err := host.composition.sessions.Session(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return active
}

func hostedChildBinding(t *testing.T, parent session.Session, id string) session.ParticipantBinding {
	t.Helper()
	for _, binding := range parent.Participants {
		if binding.ID == id {
			return session.CloneParticipantBinding(binding)
		}
	}
	t.Fatalf("participant %q is not attached", id)
	return session.ParticipantBinding{}
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
	update := session.ProtocolUpdateOf(event)
	if session.EventTypeOf(event) != session.EventTypeContext || update == nil ||
		update.SessionUpdate != string(session.ProtocolUpdateTypeUserMessage) {
		t.Fatalf("input event = %#v, want Agent Context with standard user_message projection", event)
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
	for time.Now().Before(deadline) {
		host.sessionRuntimes.mu.RLock()
		active := host.sessionRuntimes.sessions[sessionID]
		host.sessionRuntimes.mu.RUnlock()
		// Callers have already observed the input or completed Turn. The idle
		// Runtime may have been released before this helper first observes it.
		if active == nil {
			return
		}
		if active.instance != nil {
			if _, ok := active.instance.currentGateway().ActiveTurn(sessionID); !ok {
				return
			}
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
	lastMessages json.RawMessage
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
	p.lastMessages, _ = json.Marshal(payload["messages"])
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

func (p *hostedChildInputTestProvider) LastMessages() json.RawMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append(json.RawMessage(nil), p.lastMessages...)
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

func (*hostedChildClosingRunner) Submit(agent.Submission) error {
	return agent.ErrRunInputClosed
}

func (*hostedChildClosingRunner) SubmitBatch(context.Context, []agent.AgentCommunicationInput) error {
	return agent.ErrRunInputClosed
}

func (*hostedChildClosingRunner) Cancel() agent.CancelResult {
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}

func (*hostedChildClosingRunner) Close() error { return nil }
func (r *hostedChildClosingRunner) WaitCompletion(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.release:
		return nil
	}
}

type hostedChildTerminalRunner struct{}

func (hostedChildTerminalRunner) RunID() string { return "replacement-parent-run" }

func (hostedChildTerminalRunner) Submit(agent.Submission) error { return nil }

func (hostedChildTerminalRunner) Cancel() agent.CancelResult {
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}

func (hostedChildTerminalRunner) Close() error                         { return nil }
func (hostedChildTerminalRunner) WaitCompletion(context.Context) error { return nil }

func hostedChildCanonicalTurn(t *testing.T, host *Stack, ref session.SessionRef, input string) string {
	t.Helper()
	events, err := host.composition.sessions.Events(t.Context(), session.EventsRequest{SessionRef: ref})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == session.EventTypeUser && session.EventText(event) == input && event.Scope != nil {
			return event.Scope.TurnID
		}
	}
	t.Fatal("initial canonical Turn input missing")
	return ""
}
