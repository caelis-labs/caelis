package acpagentbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/projection"
	controlstatus "github.com/caelis-labs/caelis/control/status"
	spoolfile "github.com/caelis-labs/caelis/control/streamspool/file"
	"github.com/caelis-labs/caelis/internal/acpdraft"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

// The fixture uses physical Session persistence and the production feed broker.
// Only model execution is held at explicit canonical insertion safe points.
type draftNativeFixture struct {
	appserver.SessionClient
	t            *testing.T
	store        *sessionfile.Store
	spool        *spoolfile.Store
	mu           sync.Mutex
	states       map[string]appserver.SessionState
	refs         map[string]session.SessionRef
	feeds        map[string]*appserver.FeedBroker
	next         int
	inserts      chan draftNativeInput
	resolved     chan appserver.ResolveApprovalRequest
	beforePrompt func()
	// The terminal hooks model native state becoming inactive before its
	// notification is accepted by the broker. They are protected by mu.
	beforeTerminalPublish func()
	afterTerminalPublish  func()
}

type draftNativeInput struct {
	sessionID, text, operationID string
	target                       appserver.TurnTarget
}

func newDraftNativeFixture(t *testing.T) *draftNativeFixture {
	spool, err := spoolfile.New(t.Context(), spoolfile.Config{RootDir: t.TempDir(), GCInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	f := &draftNativeFixture{t: t, store: sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()}), spool: spool, states: make(map[string]appserver.SessionState), refs: make(map[string]session.SessionRef), feeds: make(map[string]*appserver.FeedBroker), inserts: make(chan draftNativeInput, 20), resolved: make(chan appserver.ResolveApprovalRequest, 20)}
	t.Cleanup(func() {
		for _, feed := range f.feeds {
			_ = feed.Close()
		}
		_ = spool.Close()
	})
	return f
}

func (f *draftNativeFixture) Initialize(context.Context) (appserver.ServerInfo, error) {
	return appserver.ServerInfo{Capabilities: []string{appserver.CapabilityConversationInteractions}}, nil
}

func (f *draftNativeFixture) CreateSession(ctx context.Context, request appserver.CreateSessionRequest) (appserver.CommandResult, error) {
	created, err := f.store.StartSession(ctx, session.StartSessionRequest{AppName: "draft-fixture", UserID: "owner", Workspace: session.WorkspaceRef{Key: request.CWD, CWD: request.CWD}})
	if err != nil {
		return appserver.CommandResult{}, err
	}
	created, err = f.store.BindController(ctx, session.BindControllerRequest{SessionRef: created.SessionRef, Binding: session.ControllerBinding{Kind: session.ControllerKindKernel, Source: "acp_ingress", ControllerID: "native"}})
	if err != nil {
		return appserver.CommandResult{}, err
	}
	codec, err := appserver.NewCursorCodec(appserver.CursorCodecConfig{Secret: make([]byte, 32)})
	if err != nil {
		return appserver.CommandResult{}, err
	}
	feed, err := appserver.NewFeedBroker(appserver.FeedBrokerConfig{SessionRef: created.SessionRef, Reader: f.store, Spool: f.spool, CursorCodec: codec})
	if err != nil {
		return appserver.CommandResult{}, err
	}
	f.mu.Lock()
	f.refs[created.SessionID] = created.SessionRef
	f.feeds[created.SessionID] = feed
	f.states[created.SessionID] = appserver.SessionState{SessionID: created.SessionID, CWD: request.CWD, Revision: created.Revision, Controller: created.Controller}
	f.mu.Unlock()
	return appserver.CommandResult{OperationID: request.OperationID, SessionID: created.SessionID, Outcome: appserver.OutcomeCommitted}, nil
}

func (f *draftNativeFixture) InspectSession(_ context.Context, request appserver.StateRequest) (appserver.SessionState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	state, exists := f.states[request.SessionID]
	if !exists {
		return state, session.ErrSessionNotFound
	}
	_, state.BoundaryCursor = f.feeds[request.SessionID].Boundary()
	return state, nil
}

func (f *draftNativeFixture) Reconnect(ctx context.Context, request appserver.ReconnectRequest) (appserver.ReconnectResult, error) {
	state, err := f.InspectSession(ctx, appserver.StateRequest{SessionID: request.SessionID})
	if err != nil {
		return appserver.ReconnectResult{}, err
	}
	f.mu.Lock()
	feed := f.feeds[request.SessionID]
	f.mu.Unlock()
	sub, err := feed.Subscribe(ctx, appserver.SubscribeRequest{SessionID: request.SessionID, Cursor: request.Cursor})
	return appserver.ReconnectResult{State: state, Subscription: sub.Subscription}, err
}

func (f *draftNativeFixture) Prompt(_ context.Context, request appserver.PromptRequest) (appserver.CommandResult, error) {
	if f.beforePrompt != nil {
		f.beforePrompt()
	}
	f.mu.Lock()
	state := f.states[request.SessionID]
	if state.Run.Active {
		f.mu.Unlock()
		return appserver.CommandResult{Outcome: appserver.OutcomeConflicted}, fmt.Errorf("Turn active")
	}
	f.next++
	id := fmt.Sprint(f.next)
	target := appserver.TurnTarget{HandleID: "handle-" + id, RunID: "run-" + id, TurnID: "turn-" + id}
	state.Run = appserver.RunState{Active: true, Kind: appserver.RunKindKernel, HandleID: target.HandleID, RunID: target.RunID, TurnID: target.TurnID}
	f.states[request.SessionID] = state
	feed := f.feeds[request.SessionID]
	f.mu.Unlock()
	env := eventstream.TurnLifecycle(target.HandleID, target.RunID, target.TurnID, "running", "", "", time.Now())
	env.SessionID = request.SessionID
	if err := feed.Publish(env); err != nil {
		return appserver.CommandResult{}, err
	}
	f.inserts <- draftNativeInput{sessionID: request.SessionID, text: request.Input, target: target}
	return appserver.CommandResult{Outcome: appserver.OutcomeCommitted, OperationID: request.OperationID, SessionID: request.SessionID, Target: target}, nil
}

func (f *draftNativeFixture) Steer(_ context.Context, request appserver.SteerRequest) (appserver.CommandResult, error) {
	f.mu.Lock()
	state := f.states[request.SessionID]
	f.mu.Unlock()
	if !state.Run.Active || draftRunTarget(state.Run) != request.Target {
		return appserver.CommandResult{Outcome: appserver.OutcomeConflicted}, fmt.Errorf("stale Turn")
	}
	f.inserts <- draftNativeInput{sessionID: request.SessionID, text: request.Input, target: request.Target, operationID: request.OperationID}
	return appserver.CommandResult{Outcome: appserver.OutcomeCommitted, OperationID: request.OperationID, SessionID: request.SessionID, Target: request.Target, InputStatus: "accepted"}, nil
}

func (f *draftNativeFixture) insert(input draftNativeInput) string {
	f.t.Helper()
	f.mu.Lock()
	ref := f.refs[input.sessionID]
	feed := f.feeds[input.sessionID]
	f.mu.Unlock()
	message := model.NewTextMessage(model.RoleUser, input.text)
	event := &session.Event{Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Message: &message, Text: input.text, Scope: &session.EventScope{TurnID: input.target.TurnID}}
	if input.operationID != "" {
		event.Meta = map[string]any{"steering_operation_id": input.operationID}
	}
	stored, err := f.store.AppendEvent(f.t.Context(), session.AppendEventRequest{SessionRef: ref, Event: event})
	if err != nil {
		f.t.Fatal(err)
	}
	base := projection.EnvelopeBaseFromSessionEvent(ref, stored, projection.SessionEventTransport{HandleID: input.target.HandleID, RunID: input.target.RunID, TurnID: input.target.TurnID})
	for _, env := range projection.ProjectSessionEventEnvelope(base, stored) {
		if err := feed.Publish(env); err != nil {
			f.t.Fatal(err)
		}
	}
	return stored.ID
}

func (f *draftNativeFixture) Cancel(_ context.Context, request appserver.CancelRequest) (appserver.CommandResult, error) {
	f.finish(request.SessionID, true)
	return appserver.CommandResult{Outcome: appserver.OutcomeCommitted, OperationID: request.OperationID}, nil
}

func (f *draftNativeFixture) finish(id string, canceled bool) {
	f.t.Helper()
	f.mu.Lock()
	state := f.states[id]
	target := draftRunTarget(state.Run)
	state.Run.Active = false
	state.Run.WaitingApproval = false
	if state.Approval.Active != nil && state.Approval.Active.Target == target {
		state.Approval.Active = nil
	}
	f.states[id] = state
	feed := f.feeds[id]
	beforePublish, afterPublish := f.beforeTerminalPublish, f.afterTerminalPublish
	f.mu.Unlock()
	if beforePublish != nil {
		beforePublish()
	}
	env := eventstream.TurnCompleted(target.HandleID, target.RunID, target.TurnID, time.Now())
	if canceled {
		env = eventstream.TurnCancelled(target.HandleID, target.RunID, target.TurnID, "client", time.Now())
	}
	env.SessionID = id
	if err := feed.Publish(env); err != nil {
		f.t.Fatal(err)
	}
	if afterPublish != nil {
		afterPublish()
	}
}

func (f *draftNativeFixture) ResolveApproval(_ context.Context, request appserver.ResolveApprovalRequest) (appserver.CommandResult, error) {
	f.mu.Lock()
	state := f.states[request.SessionID]
	if state.Approval.Active == nil || string(state.Approval.Active.RequestID) != request.ApprovalRequestID || state.Approval.Active.Target != request.Target {
		f.mu.Unlock()
		return appserver.CommandResult{Outcome: appserver.OutcomeRejected}, fmt.Errorf("wrong native request")
	}
	state.Approval.Active = nil
	state.Run.WaitingApproval = false
	f.states[request.SessionID] = state
	f.mu.Unlock()
	f.resolved <- request
	return appserver.CommandResult{Outcome: appserver.OutcomeCommitted, OperationID: request.OperationID}, nil
}

type draftNativePresentation struct{ appserver.PresentationClient }

func (draftNativePresentation) PresentationSnapshot(context.Context, appserver.PresentationRequest) (appserver.PresentationSnapshot, error) {
	return appserver.PresentationSnapshot{}, nil
}
func (draftNativePresentation) PresentationCapabilities(context.Context) (appserver.PresentationCapabilities, error) {
	return appserver.PresentationCapabilities{Image: true}, nil
}

type draftNativeRouter struct {
	fixture *draftNativeFixture
	id      string
}

func (r draftNativeRouter) Route(ctx context.Context, request controlprompt.Request) (controlprompt.Result, error) {
	result, err := r.fixture.Prompt(ctx, appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: r.id, OperationID: "router-owned"}, Input: request.Submission.Text})
	if err != nil {
		return controlprompt.Result{}, &appserver.CommandReceiptError{Receipt: result, Err: err}
	}
	return controlprompt.Result{Handled: true, Turn: draftNativeTurn{target: result.Target}}, nil
}

type draftNativeTurn struct{ target appserver.TurnTarget }

func (t draftNativeTurn) HandleID() string                  { return t.target.HandleID }
func (t draftNativeTurn) RunID() string                     { return t.target.RunID }
func (t draftNativeTurn) TurnID() string                    { return t.target.TurnID }
func (draftNativeTurn) Events() <-chan eventstream.Envelope { return nil }
func (draftNativeTurn) SubmitApproval(context.Context, controlprompt.ApprovalDecision) error {
	return nil
}
func (draftNativeTurn) Cancel()      {}
func (draftNativeTurn) Close() error { return nil }

type draftNativePeer struct {
	mu                 sync.Mutex
	updates            []v2.UpdateSessionNotification
	permissions        chan v2.RequestPermissionRequest
	permissionResponse chan v2.RequestPermissionResponse
	changed            chan struct{}
}

func (p *draftNativePeer) SessionUpdate(_ context.Context, n v2.UpdateSessionNotification) error {
	p.mu.Lock()
	p.updates = append(p.updates, n)
	p.mu.Unlock()
	select {
	case p.changed <- struct{}{}:
	default:
	}
	return nil
}
func (p *draftNativePeer) RequestPermission(ctx context.Context, r v2.RequestPermissionRequest) (v2.RequestPermissionResponse, error) {
	select {
	case p.permissions <- r:
	case <-ctx.Done():
		return v2.RequestPermissionResponse{}, ctx.Err()
	}
	select {
	case response := <-p.permissionResponse:
		return response, nil
	case <-ctx.Done():
		return v2.RequestPermissionResponse{}, ctx.Err()
	}
}

func connectDraftNative(t *testing.T, f *draftNativeFixture) (*v2.ClientSideConnection, *DraftAgent, *draftNativePeer) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	inner := &RuntimeAgent{sessionClient: f, presentationClient: draftNativePresentation{}, appName: "draft-fixture", userID: "owner", agentInfo: &acp.Implementation{Name: "fixture", Version: "test"}, managedSessions: make(map[string]struct{})}
	inner.promptRouterFactory = func(_ context.Context, s session.Session) (controlprompt.Router, error) {
		return draftNativeRouter{fixture: f, id: s.SessionID}, nil
	}
	agent := &DraftAgent{inner: inner, ctx: ctx, cancel: cancel, ready: make(chan struct{}), sessions: make(map[string]*draftSession)}
	serverPipe, clientPipe := net.Pipe()
	server, err := v2.NewAgentSideConnection(agent, serverPipe, serverPipe)
	if err != nil {
		t.Fatal(err)
	}
	agent.BindDraftCallbacks(server)
	peer := &draftNativePeer{permissions: make(chan v2.RequestPermissionRequest, 10), permissionResponse: make(chan v2.RequestPermissionResponse, 10), changed: make(chan struct{}, 1)}
	client, err := v2.NewClientSideConnection(peer, clientPipe, clientPipe)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(); _ = server.Close(); _ = agent.Close() })
	response, err := client.Initialize(ctx, v2.InitializeRequest{ProtocolVersion: 2, Info: v2.Implementation{Name: "peer", Version: "test"}, Meta: acpdraft.InitializeMeta()})
	if err != nil {
		t.Fatal(err)
	}
	if err := acpdraft.ValidatePeer(response); err != nil {
		t.Fatal(err)
	}
	return client, agent, peer
}

func draftNativePrompt(id, text string) v2.PromptRequest {
	return v2.PromptRequest{SessionId: v2.SessionId(id), Prompt: []v2.ContentBlock{{Text: &v2.ContentBlockText{Type: "text", Text: text}}}}
}

func TestDraftNativeReceiptWaitsForCanonicalInsertionAndSessionsStayIndependent(t *testing.T) {
	f := newDraftNativeFixture(t)
	client, _, peer := connectDraftNative(t, f)
	first, err := client.NewSession(t.Context(), v2.NewSessionRequest{Cwd: v2.AbsolutePath(t.TempDir())})
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.NewSession(t.Context(), v2.NewSessionRequest{Cwd: v2.AbsolutePath(t.TempDir())})
	if err != nil {
		t.Fatal(err)
	}
	type receipt struct {
		response v2.PromptResponse
		err      error
	}
	firstResult, secondResult := make(chan receipt, 1), make(chan receipt, 1)
	go func() {
		r, e := client.Prompt(t.Context(), draftNativePrompt(string(first.SessionId), "first"))
		firstResult <- receipt{r, e}
	}()
	go func() {
		r, e := client.Prompt(t.Context(), draftNativePrompt(string(second.SessionId), "second"))
		secondResult <- receipt{r, e}
	}()
	one, two := <-f.inserts, <-f.inserts
	select {
	case r := <-firstResult:
		t.Fatalf("receipt before insert: %#v", r)
	case r := <-secondResult:
		t.Fatalf("receipt before insert: %#v", r)
	case <-time.After(30 * time.Millisecond):
	}
	ids := map[string]string{one.sessionID: f.insert(one), two.sessionID: f.insert(two)}
	activities := map[string]string{one.sessionID: one.target.TurnID, two.sessionID: two.target.TurnID}
	for id, ch := range map[string]<-chan receipt{string(first.SessionId): firstResult, string(second.SessionId): secondResult} {
		select {
		case r := <-ch:
			if r.err != nil || string(r.response.MessageId) != ids[id] {
				t.Fatalf("receipt %#v, canonical %q", r, ids[id])
			}
			if activityID := acpdraft.ActivityID(r.response.Meta); activityID != activities[id] {
				t.Fatalf("receipt activity=%q, native=%q", activityID, activities[id])
			}
		case <-time.After(5 * time.Second):
			t.Fatal("receipt did not follow canonical insert")
		}
	}
	peer.mu.Lock()
	for id, messageID := range ids {
		found := false
		for _, update := range peer.updates {
			if string(update.SessionId) == id && update.Update.UserMessage != nil && string(update.Update.UserMessage.MessageId) == messageID {
				found = true
			}
		}
		if !found {
			t.Errorf("no live user echo for %s / %s", id, messageID)
		}
	}
	peer.mu.Unlock()
	// Cancel is a notification. An inactive native read can precede accepted
	// terminal publication, so it is not a safe fixture shutdown boundary.
	cancelPublishing, releasePublish, cancelPublished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseTerminal := sync.OnceFunc(func() { close(releasePublish) })
	f.mu.Lock()
	f.beforeTerminalPublish = func() { close(cancelPublishing); <-releasePublish }
	f.afterTerminalPublish = func() { close(cancelPublished) }
	f.mu.Unlock()
	t.Cleanup(func() {
		releaseTerminal()
		select {
		case <-cancelPublished:
		case <-time.After(5 * time.Second):
			t.Error("native cancel publication did not finish before fixture shutdown")
		}
	})
	if err := client.Cancel(t.Context(), v2.CancelSessionNotification{SessionId: first.SessionId}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelPublishing:
	case <-time.After(5 * time.Second):
		t.Fatal("native cancel did not reach terminal publication")
	}
	state, _ := f.InspectSession(t.Context(), appserver.StateRequest{SessionID: string(first.SessionId)})
	if state.Run.Active {
		t.Fatal("fixture did not expose the inactive-before-publication boundary")
	}
	cancelObserved := func() bool {
		peer.mu.Lock()
		defer peer.mu.Unlock()
		for _, update := range peer.updates {
			if update.SessionId != first.SessionId {
				continue
			}
			var fields struct {
				State      string                     `json:"state"`
				StopReason string                     `json:"stopReason"`
				Meta       map[string]json.RawMessage `json:"_meta"`
			}
			if err := draftJSONConvert(update.Update, &fields); err != nil {
				t.Fatal(err)
			}
			if fields.State == "idle" && fields.StopReason == "cancelled" && acpdraft.ActivityID(fields.Meta) == activities[string(first.SessionId)] {
				return true
			}
		}
		return false
	}
	if cancelObserved() {
		t.Fatal("cancel completed before its terminal was published")
	}
	state, _ = f.InspectSession(t.Context(), appserver.StateRequest{SessionID: string(second.SessionId)})
	if !state.Run.Active {
		t.Fatal("cancel crossed independent Session")
	}
	guarded := draftNativePrompt(string(first.SessionId), "must not start next Turn")
	guarded.Meta = acpdraft.ActiveOnlyMeta(one.target.TurnID)
	_, err = client.Prompt(t.Context(), guarded)
	if !acpdraft.IsInactivePromptError(err) {
		t.Fatalf("inactive guard = %v", err)
	}
	select {
	case input := <-f.inserts:
		t.Fatalf("guard started replacement work: %#v", input)
	default:
	}
	releaseTerminal()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for !cancelObserved() {
		select {
		case <-peer.changed:
		case <-deadline.C:
			t.Fatal("matching idle(cancelled) did not follow native publication")
		}
	}
	select {
	case <-cancelPublished:
	case <-deadline.C:
		t.Fatal("native cancel publication remained in flight")
	}
}

func TestDraftNativeSteeringReceiptRequiresAppliedInputAndPermissionKeepsNativeIdentity(t *testing.T) {
	f := newDraftNativeFixture(t)
	client, _, peer := connectDraftNative(t, f)
	created, err := client.NewSession(t.Context(), v2.NewSessionRequest{Cwd: v2.AbsolutePath(t.TempDir())})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, e := client.Prompt(t.Context(), draftNativePrompt(string(created.SessionId), "start"))
		result <- e
	}()
	first := <-f.inserts
	f.insert(first)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	request := draftNativePrompt(string(created.SessionId), "steer")
	request.Meta = acpdraft.ActiveOnlyMeta(first.target.TurnID)
	go func() { _, e := client.Prompt(t.Context(), request); result <- e }()
	steered := <-f.inserts
	select {
	case err := <-result:
		t.Fatalf("queued steer became insertion: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if steered.operationID == "" || steered.target != first.target {
		t.Fatalf("steering target %#v", steered)
	}
	f.insert(steered)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	permission := &session.ProtocolApproval{ToolCall: session.ProtocolToolCall{ID: "tool-1", Title: "Write file", Kind: "edit"}, Options: []session.ProtocolApprovalOption{{ID: "native-option-7", Name: "Allow once", Kind: "allow_once"}}}
	f.mu.Lock()
	state := f.states[string(created.SessionId)]
	state.Run.WaitingApproval = true
	state.Approval.Active = &appserver.ActiveApproval{RequestID: "native-request-42", Target: first.target, Permission: permission}
	f.states[string(created.SessionId)] = state
	feed := f.feeds[string(created.SessionId)]
	f.mu.Unlock()
	if err := feed.Publish(eventstream.Envelope{Kind: eventstream.KindRequestPermission, SessionID: string(created.SessionId), ApprovalRequestID: "native-request-42"}); err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-peer.permissions:
		if len(request.Options) != 1 || string(request.Options[0].OptionId) != "native-option-7" {
			t.Fatalf("permission options %#v", request)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("missing permission")
	}
	peer.permissionResponse <- v2.RequestPermissionResponse{Outcome: v2.RequestPermissionOutcome{Selected: &v2.RequestPermissionOutcomeSelected{Outcome: "selected", OptionId: "native-option-7"}}}
	select {
	case resolution := <-f.resolved:
		if resolution.ApprovalRequestID != "native-request-42" || resolution.Target != first.target || resolution.OptionID != "native-option-7" || !resolution.Approved {
			t.Fatalf("resolution %#v", resolution)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native permission unresolved")
	}
}

type draftReceiptRouterService struct {
	controlprompt.RouterService
	fixture                *draftNativeFixture
	id                     string
	messageID              string
	recordErr, dispatchErr error
	effects                int
}

func (s *draftReceiptRouterService) RecordInteraction(ctx context.Context, text string) (string, error) {
	s.fixture.mu.Lock()
	ref, feed := s.fixture.refs[s.id], s.fixture.feeds[s.id]
	s.fixture.mu.Unlock()
	event, err := s.fixture.store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: ref, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeInteraction), Event: &session.Event{
		Type: session.EventTypeInteraction, Visibility: session.VisibilityMirror,
		Protocol: &session.EventProtocol{Method: session.ProtocolMethodSessionUpdate, Update: &session.ProtocolUpdate{SessionUpdate: string(session.ProtocolUpdateTypeUserMessage), Content: session.ProtocolTextContent(text)}},
	}})
	if err != nil {
		return "", err
	}
	s.messageID = event.ID
	base := projection.EnvelopeBaseFromSessionEvent(ref, event, projection.SessionEventTransport{})
	for _, env := range projection.ProjectSessionEventEnvelope(base, event) {
		if err := feed.Publish(env); err != nil {
			return event.ID, err
		}
	}
	return event.ID, s.recordErr
}

func (s *draftReceiptRouterService) RecordInteractionIfIdle(ctx context.Context, text string) (string, error) {
	s.fixture.mu.Lock()
	active := s.fixture.states[s.id].Run.Active
	s.fixture.mu.Unlock()
	if active {
		return "", appserver.NewOutcomeError(appserver.OutcomeConflicted, errors.New("native foreground is busy"))
	}
	return s.RecordInteraction(ctx, text)
}

func (s *draftReceiptRouterService) UseModel(context.Context, string, string, bool) (controlstatus.StatusSnapshot, error) {
	s.effects++
	return controlstatus.StatusSnapshot{}, s.dispatchErr
}

func TestDraftNativeCommandReceiptsSurvivePostInsertionFailures(t *testing.T) {
	for _, failAt := range []string{"ledger_receipt", "dispatch"} {
		t.Run(failAt, func(t *testing.T) {
			f := newDraftNativeFixture(t)
			client, agent, peer := connectDraftNative(t, f)
			created, err := client.NewSession(t.Context(), v2.NewSessionRequest{Cwd: v2.AbsolutePath(t.TempDir())})
			if err != nil {
				t.Fatal(err)
			}
			svc := &draftReceiptRouterService{fixture: f, id: string(created.SessionId)}
			if failAt == "ledger_receipt" {
				svc.recordErr = errors.New("record completion failed after append")
			} else {
				svc.dispatchErr = errors.New("model selection failed after insertion")
			}
			agent.inner.promptRouterFactory = func(context.Context, session.Session) (controlprompt.Router, error) {
				return controlprompt.New(controlprompt.RouterConfig{Service: svc}), nil
			}
			receipt, err := client.Prompt(t.Context(), draftNativePrompt(svc.id, "/model missing high"))
			if err != nil || string(receipt.MessageId) != svc.messageID || svc.messageID == "" {
				t.Fatalf("receipt=%#v err=%v native=%q", receipt, err, svc.messageID)
			}
			if activityID := acpdraft.ActivityID(receipt.Meta); activityID != svc.messageID {
				t.Fatalf("command receipt activity=%q, canonical=%q", activityID, svc.messageID)
			}
			wantEffects := 1
			if failAt == "ledger_receipt" {
				wantEffects = 0
			}
			if svc.effects != wantEffects {
				t.Fatalf("effects=%d", svc.effects)
			}
			peer.mu.Lock()
			var echo, running, idle bool
			for _, n := range peer.updates {
				if n.Update.UserMessage != nil && n.Update.UserMessage.MessageId == receipt.MessageId {
					echo = true
				}
				var fields map[string]json.RawMessage
				_ = draftJSONConvert(n.Update, &fields)
				var meta map[string]json.RawMessage
				_ = json.Unmarshal(fields["_meta"], &meta)
				if n.Update.StateUpdate != nil && acpdraft.ActivityID(meta) == svc.messageID {
					state := n.Update.StateUpdate.StateUpdate
					running = running || state.Running != nil
					idle = idle || (state.Idle != nil && state.Idle.IdleStateUpdate.Error != nil)
				}
			}
			peer.mu.Unlock()
			if !echo || !running || !idle {
				t.Fatalf("echo=%v running=%v idle_error=%v", echo, running, idle)
			}
			guarded := draftNativePrompt(svc.id, "must not use command as a Turn")
			guarded.Meta = acpdraft.ActiveOnlyMeta(svc.messageID)
			if _, err = client.Prompt(t.Context(), guarded); !acpdraft.IsInactivePromptError(err) {
				t.Fatalf("command activity guard=%v", err)
			}
		})
	}
}

func TestDraftNativeApprovalProjectsSplitSubjectWithoutChangingResolution(t *testing.T) {
	for _, kind := range []string{"known", "reported", "sensitive", "opaque"} {
		t.Run(kind, func(t *testing.T) {
			f := newDraftNativeFixture(t)
			client, _, peer := connectDraftNative(t, f)
			created, err := client.NewSession(t.Context(), v2.NewSessionRequest{Cwd: v2.AbsolutePath(t.TempDir())})
			if err != nil {
				t.Fatal(err)
			}
			id := string(created.SessionId)
			target := appserver.TurnTarget{HandleID: "original-handle", RunID: "original-run", TurnID: "original-turn"}
			title := session.KnownApprovalField("Write configuration")
			subject := &session.ApprovalSubject{Title: &title, Kind: "tool_call", Tool: session.KnownApprovalField("write_file"), Operation: session.KnownApprovalField("write"), Targets: []session.ApprovalField{session.KnownApprovalField("config.json")}, Effect: session.KnownApprovalField("replace file"), Scope: session.KnownApprovalField("workspace"), Grants: []session.ApprovalGrant{{OptionID: "original-once", Scope: session.KnownApprovalField("this operation")}}}
			switch kind {
			case "reported":
				subject.Reported = true
			case "sensitive":
				subject.Targets[0] = session.ApprovalField{State: session.ApprovalFieldKnown, Text: "PRIVATE-TARGET", Sensitive: true}
				subject.SensitiveInput = true
			case "opaque":
				subject.Operation = session.ApprovalField{State: session.ApprovalFieldOpaque}
				subject.OpaqueInput = true
			}
			permission := &session.ProtocolApproval{Context: &session.PermissionContext{RawSubject: json.RawMessage(`{"secret":"PRIVATE-RAW-SUBJECT"}`), NativeRequestID: json.RawMessage(`"PRIVATE-NATIVE-ID"`)}, ToolCall: session.ProtocolToolCall{ID: "original-tool", Title: "Write", Kind: "edit"}, Options: []session.ProtocolApprovalOption{{ID: "original-once", Name: "Allow once", Kind: "allow_once"}}}
			f.mu.Lock()
			state := f.states[id]
			state.Run = appserver.RunState{Active: true, WaitingApproval: true, Kind: appserver.RunKindKernel, HandleID: target.HandleID, RunID: target.RunID, TurnID: target.TurnID}
			state.Approval.Active = &appserver.ActiveApproval{RequestID: "original-request", Target: target, Permission: permission, Subject: subject}
			f.states[id] = state
			feed := f.feeds[id]
			f.mu.Unlock()
			running := eventstream.TurnLifecycle(target.HandleID, target.RunID, target.TurnID, eventstream.LifecycleStateRunning, "", "", time.Now())
			running.SessionID = id
			if err := feed.Publish(running); err != nil {
				t.Fatal(err)
			}
			if err := feed.Publish(eventstream.Envelope{Kind: eventstream.KindRequestPermission, SessionID: id, ApprovalRequestID: "original-request"}); err != nil {
				t.Fatal(err)
			}
			select {
			case request := <-peer.permissions:
				var display session.ApprovalSubject
				if err := json.Unmarshal(request.Meta["caelis/approval_subject"], &display); err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(request)
				if strings.Contains(string(raw), "PRIVATE-") || request.Title != title.Text || len(request.Options) != 1 || request.Options[0].OptionId != "original-once" || len(display.Grants) != 1 || display.Grants[0].OptionID != "original-once" || display.Grants[0].Scope.Text != "this operation" {
					t.Fatalf("bounded display lost original facts: %s", raw)
				}
				if display.Reported != (kind == "reported") || display.SensitiveInput != (kind == "sensitive") || display.OpaqueInput != (kind == "opaque") {
					t.Fatalf("display flags: %#v", display)
				}
				if kind == "sensitive" && (display.Targets[0].State != session.ApprovalFieldOpaque || !display.Targets[0].Sensitive || display.Targets[0].Text != "") {
					t.Fatalf("sensitive presence: %#v", display.Targets)
				}
				if kind == "opaque" && display.Operation.State != session.ApprovalFieldOpaque {
					t.Fatalf("opaque operation: %#v", display.Operation)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("split approval was not projected")
			}
			if permission.Subject != nil || subject.Targets[0].Text == "" {
				t.Fatal("projection mutated the canonical request or split subject")
			}
			peer.permissionResponse <- v2.RequestPermissionResponse{Outcome: v2.RequestPermissionOutcome{Selected: &v2.RequestPermissionOutcomeSelected{Outcome: "selected", OptionId: "original-once"}}}
			select {
			case resolution := <-f.resolved:
				if resolution.ApprovalRequestID != "original-request" || resolution.Target != target || resolution.OptionID != "original-once" || !resolution.Approved {
					t.Fatalf("resolution changed native identity: %#v", resolution)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("original approval was not resolved")
			}
		})
	}
}

func TestDraftNativeIdleOnlyRejectsRacedPromptAndCommandBeforeInsertion(t *testing.T) {
	for _, command := range []bool{false, true} {
		t.Run(fmt.Sprintf("command_%t", command), func(t *testing.T) {
			f := newDraftNativeFixture(t)
			client, agent, _ := connectDraftNative(t, f)
			created, err := client.NewSession(t.Context(), v2.NewSessionRequest{Cwd: v2.AbsolutePath(t.TempDir())})
			if err != nil {
				t.Fatal(err)
			}
			id := string(created.SessionId)
			foreign := appserver.RunState{Active: true, Kind: appserver.RunKindKernel, HandleID: "other-handle", RunID: "other-run", TurnID: "other-turn"}
			startOther := func() {
				f.mu.Lock()
				state := f.states[id]
				state.Run = foreign
				f.states[id] = state
				f.mu.Unlock()
			}
			text := "new foreground input"
			var svc *draftReceiptRouterService
			if command {
				text = "/model missing high"
				svc = &draftReceiptRouterService{fixture: f, id: id}
				agent.inner.promptRouterFactory = func(context.Context, session.Session) (controlprompt.Router, error) {
					startOther()
					return controlprompt.New(controlprompt.RouterConfig{Service: svc}), nil
				}
			} else {
				f.beforePrompt = startOther
			}
			request := draftNativePrompt(id, text)
			request.Meta = acpdraft.IdleOnlyMeta()
			response, err := client.Prompt(t.Context(), request)
			if !acpdraft.IsInactivePromptError(err) || response.MessageId != "" {
				t.Fatalf("race response=%#v err=%v", response, err)
			}
			if svc != nil && (svc.effects != 0 || svc.messageID != "") {
				t.Fatalf("raced command inserted or dispatched: id=%q effects=%d", svc.messageID, svc.effects)
			}
			select {
			case input := <-f.inserts:
				t.Fatalf("raced input admitted: %#v", input)
			default:
			}
			state, err := f.InspectSession(t.Context(), appserver.StateRequest{SessionID: id})
			if err != nil || state.Run != foreign {
				t.Fatalf("race retargeted original work: %#v %v", state.Run, err)
			}
		})
	}
}

func TestDraftNativeCancelSettlesUninsertedSteeringWithoutInventingReceipt(t *testing.T) {
	f := newDraftNativeFixture(t)
	client, _, _ := connectDraftNative(t, f)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	created, err := client.NewSession(ctx, v2.NewSessionRequest{Cwd: v2.AbsolutePath(t.TempDir())})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 1)
	go func() {
		_, err := client.Prompt(ctx, draftNativePrompt(string(created.SessionId), "start"))
		results <- err
	}()
	initial := <-f.inserts
	f.insert(initial)
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	request := draftNativePrompt(string(created.SessionId), "queued steering")
	request.Meta = acpdraft.ActiveOnlyMeta(initial.target.TurnID)
	go func() {
		receipt, err := client.Prompt(ctx, request)
		if receipt.MessageId != "" {
			err = fmt.Errorf("invented receipt: %s", receipt.MessageId)
		}
		results <- err
	}()
	<-f.inserts // accepted by the native owner, still before canonical insertion
	if err := client.Cancel(ctx, v2.CancelSessionNotification{SessionId: created.SessionId}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-results:
		if err == nil || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("uninserted cancellation=%v", err)
		}
	case <-ctx.Done():
		t.Fatal("cancel left the uninserted prompt blocked")
	}
}
