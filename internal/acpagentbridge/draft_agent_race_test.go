package acpagentbridge

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpmeta"
	"github.com/caelis-labs/caelis/internal/acpdraft"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

type draftBlockedInspection struct {
	appserver.SessionClient
	state            appserver.SessionState
	started, release chan struct{}
}

func (c *draftBlockedInspection) InspectSession(context.Context, appserver.StateRequest) (appserver.SessionState, error) {
	close(c.started)
	<-c.release
	return c.state, nil
}

type draftStateCollector struct {
	updates chan v2.UpdateSessionNotification
}

func (c draftStateCollector) SessionUpdate(_ context.Context, update v2.UpdateSessionNotification) error {
	c.updates <- update
	return nil
}
func (draftStateCollector) RequestPermission(context.Context, v2.RequestPermissionRequest) (v2.RequestPermissionResponse, error) {
	panic("unexpected permission")
}

func draftCollectedState(t *testing.T, n v2.UpdateSessionNotification) (string, string) {
	t.Helper()
	raw, err := json.Marshal(n.Update)
	if err != nil {
		t.Fatal(err)
	}
	var fields struct {
		State      string `json:"state"`
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	return fields.State, fields.StopReason
}

func TestDraftApprovalSnapshotCannotReopenSettledActivity(t *testing.T) {
	active := appserver.SessionState{SessionID: "session", Run: appserver.RunState{Active: true, Kind: appserver.RunKindKernel, TurnID: "turn"}}
	inspector := &draftBlockedInspection{state: active, started: make(chan struct{}), release: make(chan struct{})}
	callbacks := draftStateCollector{updates: make(chan v2.UpdateSessionNotification, 4)}
	s := &draftSession{agent: &DraftAgent{inner: &RuntimeAgent{sessionClient: inspector}, callbacks: callbacks}, id: "session", ctx: t.Context(), state: active}
	s.refreshApprovalState()
	<-inspector.started
	terminal := eventstream.TurnCompleted("handle", "run", "turn", time.Now())
	terminal.SessionID = s.id
	if err := s.emitEnvelope(terminal, false, false); err != nil {
		t.Fatal(err)
	}
	if state, reason := draftCollectedState(t, <-callbacks.updates); state != "idle" || reason != "end_turn" {
		t.Fatalf("terminal=%s/%s", state, reason)
	}
	close(inspector.release)
	select {
	case n := <-callbacks.updates:
		state, reason := draftCollectedState(t, n)
		if state == "running" || state == "requires_action" {
			t.Fatalf("terminal reopened: %s/%s", state, reason)
		}
	case <-time.After(30 * time.Millisecond):
	}
}

func TestDraftApprovalInactiveSnapshotWaitsForActualTerminalReason(t *testing.T) {
	inspector := &draftBlockedInspection{state: appserver.SessionState{SessionID: "session", Run: appserver.RunState{TurnID: "turn"}}, started: make(chan struct{}), release: make(chan struct{})}
	callbacks := draftStateCollector{updates: make(chan v2.UpdateSessionNotification, 4)}
	s := &draftSession{agent: &DraftAgent{inner: &RuntimeAgent{sessionClient: inspector}, callbacks: callbacks}, id: "session", ctx: t.Context(), state: appserver.SessionState{SessionID: "session", Run: appserver.RunState{Active: true, TurnID: "turn"}}}
	s.refreshApprovalState()
	<-inspector.started
	close(inspector.release)
	select {
	case n := <-callbacks.updates:
		if state, reason := draftCollectedState(t, n); state == "idle" {
			t.Fatalf("snapshot settled without native terminal: idle(%s)", reason)
		}
	case <-time.After(time.Second):
		t.Fatal("snapshot inspection did not complete")
	}
	terminal := eventstream.TurnCancelled("handle", "run", "turn", "client", time.Now())
	terminal.SessionID = s.id
	if err := s.emitEnvelope(terminal, false, false); err != nil {
		t.Fatal(err)
	}
	if state, reason := draftCollectedState(t, <-callbacks.updates); state != "idle" || reason != "cancelled" {
		t.Fatalf("terminal=%s/%s", state, reason)
	}
}

func TestDraftCancelPendingAssemblyFencesOnlyThatAdmission(t *testing.T) {
	f := newDraftNativeFixture(t)
	peer, agent, _ := connectDraftNative(t, f)
	created, err := peer.NewSession(t.Context(), v2.NewSessionRequest{Cwd: v2.AbsolutePath(t.TempDir())})
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	agent.inner.promptRouterFactory = func(ctx context.Context, native session.Session) (controlprompt.Router, error) {
		close(started)
		<-release
		return draftNativeRouter{fixture: f, id: native.SessionID}, nil
	}
	result := make(chan error, 1)
	go func() {
		_, err := peer.Prompt(t.Context(), draftNativePrompt(string(created.SessionId), "pending input"))
		result <- err
	}()
	<-started
	if err := agent.Cancel(t.Context(), v2.CancelSessionNotification{SessionId: created.SessionId}); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-result:
		var cancelled *acp.RequestError
		if !errors.As(err, &cancelled) || cancelled.Code != -32800 {
			t.Fatalf("pending cancellation=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pending cancellation did not settle")
	}
	select {
	case input := <-f.inserts:
		t.Fatalf("cancelled admission started: %#v", input)
	default:
	}
	agent.inner.promptRouterFactory = func(ctx context.Context, native session.Session) (controlprompt.Router, error) {
		return draftNativeRouter{fixture: f, id: native.SessionID}, nil
	}
	go func() {
		_, err := peer.Prompt(t.Context(), draftNativePrompt(string(created.SessionId), "fresh input"))
		result <- err
	}()
	input := <-f.inserts
	f.insert(input)
	if err := <-result; err != nil {
		t.Fatalf("previous cancel polluted next admission: %v", err)
	}
	f.finish(input.sessionID, true)
}

func TestDraftProductionOutboundKeepsApprovalTerminalReasonAndNextPrompt(t *testing.T) {
	for _, reason := range []string{"cancelled", "error"} {
		t.Run(reason, func(t *testing.T) {
			f := newDraftNativeFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			inner := &RuntimeAgent{sessionClient: f, presentationClient: draftNativePresentation{}, appName: "draft-fixture", userID: "owner", agentInfo: &acp.Implementation{Name: "fixture", Version: "test"}, managedSessions: make(map[string]struct{})}
			inner.promptRouterFactory = func(_ context.Context, native session.Session) (controlprompt.Router, error) {
				return draftNativeRouter{fixture: f, id: native.SessionID}, nil
			}
			agent := &DraftAgent{inner: inner, ctx: ctx, cancel: cancel, ready: make(chan struct{}), sessions: make(map[string]*draftSession)}
			serverPipe, clientPipe := net.Pipe()
			server, err := v2.NewAgentSideConnection(agent, serverPipe, serverPipe)
			if err != nil {
				t.Fatal(err)
			}
			agent.BindDraftCallbacks(server)
			permissions := make(chan v2.RequestPermissionRequest, 1)
			reply := make(chan struct{})
			outbound, err := client.NewStreamClient(clientPipe, clientPipe, client.Config{ACPDraftSchema: acpdraft.SchemaTag, OnDraftPermissionRequest: func(ctx context.Context, request v2.RequestPermissionRequest) (v2.RequestPermissionResponse, error) {
				permissions <- request
				select {
				case <-reply:
					return v2.RequestPermissionResponse{Outcome: v2.RequestPermissionOutcome{Selected: &v2.RequestPermissionOutcomeSelected{Outcome: "selected", OptionId: "allow-original"}}}, nil
				case <-ctx.Done():
					return v2.RequestPermissionResponse{}, ctx.Err()
				}
			}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = outbound.Close(context.Background()); _ = server.Close(); _ = agent.Close() })
			if _, err := outbound.Initialize(ctx); err != nil {
				t.Fatal(err)
			}
			created, err := outbound.NewSession(ctx, t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			type completion struct {
				response client.PromptResponse
				err      error
			}
			completed := make(chan completion, 1)
			go func() {
				response, err := outbound.Prompt(ctx, created.SessionID, "first", nil)
				completed <- completion{response, err}
			}()
			input := <-f.inserts
			f.insert(input)
			f.mu.Lock()
			state := f.states[input.sessionID]
			state.Run.WaitingApproval = true
			state.Approval.Active = &appserver.ActiveApproval{RequestID: "native-approval", Target: input.target, Permission: &session.ProtocolApproval{ToolCall: session.ProtocolToolCall{ID: "native-tool", Title: "Approval"}, Options: []session.ProtocolApprovalOption{{ID: "allow-original", Name: "Allow", Kind: "allow_once"}}}}
			f.states[input.sessionID] = state
			feed := f.feeds[input.sessionID]
			f.mu.Unlock()
			if err := feed.Publish(eventstream.Envelope{Kind: eventstream.KindRequestPermission, SessionID: input.sessionID, ApprovalRequestID: "native-approval"}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-permissions:
			case <-ctx.Done():
				t.Fatal("permission was not presented")
			}
			// Native execution becomes inactive before its terminal delivery reaches
			// this observer. Resolving the approval must not fabricate idle(none).
			f.mu.Lock()
			state = f.states[input.sessionID]
			state.Run.Active = false
			f.states[input.sessionID] = state
			f.mu.Unlock()
			close(reply)
			select {
			case resolution := <-f.resolved:
				if resolution.Target != input.target || resolution.ApprovalRequestID != "native-approval" {
					t.Fatalf("resolution=%#v", resolution)
				}
			case <-ctx.Done():
				t.Fatal("approval did not resolve")
			}
			select {
			case result := <-completed:
				t.Fatalf("passive snapshot completed prompt early: %#v", result)
			case <-time.After(30 * time.Millisecond):
			}
			terminal := eventstream.TurnCancelled(input.target.HandleID, input.target.RunID, input.target.TurnID, "client", time.Now())
			if reason == "error" {
				terminal = eventstream.TurnFailed(input.target.HandleID, input.target.RunID, input.target.TurnID, "execution failed", time.Now())
			}
			terminal.SessionID = input.sessionID
			if err := feed.Publish(terminal); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-completed:
				if reason == "cancelled" && (result.err != nil || result.response.StopReason != "cancelled") {
					t.Fatalf("cancel completion=%#v", result)
				}
				if reason == "error" && (result.err == nil || !strings.Contains(result.err.Error(), "foreground Activity failed")) {
					t.Fatalf("error completion=%#v", result)
				}
			case <-ctx.Done():
				t.Fatal("native terminal did not complete prompt")
			}
			go func() {
				response, err := outbound.Prompt(ctx, input.sessionID, "next", nil)
				completed <- completion{response, err}
			}()
			next := <-f.inserts
			if next.target.TurnID == input.target.TurnID {
				t.Fatal("next Activity reused terminal identity")
			}
			f.insert(next)
			f.finish(next.sessionID, false)
			select {
			case result := <-completed:
				if result.err != nil || result.response.StopReason != "end_turn" {
					t.Fatalf("next completion=%#v", result)
				}
			case <-ctx.Done():
				t.Fatal("connection did not admit next prompt")
			}
		})
	}
}

func connectDraftProductionOutbound(t *testing.T, ctx context.Context, f *draftNativeFixture, cfg client.Config, configure func(*RuntimeAgent)) (*client.Client, *DraftAgent) {
	t.Helper()
	agentCtx, cancel := context.WithCancel(ctx)
	inner := &RuntimeAgent{sessionClient: f, presentationClient: draftNativePresentation{}, appName: "draft-fixture", userID: "owner", agentInfo: &acp.Implementation{Name: "fixture", Version: "test"}, managedSessions: make(map[string]struct{})}
	inner.promptRouterFactory = func(_ context.Context, native session.Session) (controlprompt.Router, error) {
		return draftNativeRouter{fixture: f, id: native.SessionID}, nil
	}
	if configure != nil {
		configure(inner)
	}
	agent := &DraftAgent{inner: inner, ctx: agentCtx, cancel: cancel, ready: make(chan struct{}), sessions: make(map[string]*draftSession)}
	serverPipe, clientPipe := net.Pipe()
	server, err := v2.NewAgentSideConnection(agent, serverPipe, serverPipe)
	if err != nil {
		t.Fatal(err)
	}
	agent.BindDraftCallbacks(server)
	cfg.ACPDraftSchema = acpdraft.SchemaTag
	outbound, err := client.NewStreamClient(clientPipe, clientPipe, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = outbound.Close(context.Background()); _ = server.Close(); _ = agent.Close() })
	if _, err := outbound.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	return outbound, agent
}

func TestDraftProductionOutboundBindsReceiptAfterAnotherCallersCompleteActivity(t *testing.T) {
	f := newDraftNativeFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	outbound, _ := connectDraftProductionOutbound(t, ctx, f, client.Config{}, func(inner *RuntimeAgent) {
		inner.promptRouterFactory = func(_ context.Context, native session.Session) (controlprompt.Router, error) {
			close(started)
			<-release
			return draftNativeRouter{fixture: f, id: native.SessionID}, nil
		}
	})
	created, err := outbound.NewSession(ctx, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	type completion struct {
		response client.PromptResponse
		err      error
	}
	completed := make(chan completion, 1)
	go func() {
		response, err := outbound.Prompt(ctx, created.SessionID, "this caller", nil)
		completed <- completion{response, err}
	}()
	<-started
	_, err = f.Prompt(ctx, appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: created.SessionID, OperationID: "other-owner"}, Input: "another caller"})
	if err != nil {
		t.Fatal(err)
	}
	other := <-f.inserts
	f.insert(other)
	f.finish(created.SessionID, false)
	select {
	case result := <-completed:
		t.Fatalf("another caller settled pending admission: %#v", result)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	input := <-f.inserts
	if input.target.TurnID == other.target.TurnID || input.text != "this caller" {
		t.Fatalf("request adopted ambient Activity: %#v", input)
	}
	select {
	case result := <-completed:
		t.Fatalf("ambient idle settled before true receipt: %#v", result)
	case <-time.After(30 * time.Millisecond):
	}
	f.insert(input)
	f.finish(input.sessionID, false)
	select {
	case result := <-completed:
		if result.err != nil || result.response.StopReason != "end_turn" {
			t.Fatalf("actual Activity did not settle: %#v", result)
		}
	case <-ctx.Done():
		t.Fatal("actual receipt-bound completion was lost")
	}
}

func TestDraftProductionOutboundCommandReceiptSurvivesReplayAndFailure(t *testing.T) {
	for _, failAt := range []string{"none", "ledger_receipt", "dispatch"} {
		t.Run(failAt, func(t *testing.T) {
			f := newDraftNativeFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			svc := &draftReceiptRouterService{fixture: f}
			switch failAt {
			case "ledger_receipt":
				svc.recordErr = errors.New("completion failed after append")
			case "dispatch":
				svc.dispatchErr = errors.New("command effect failed")
			}
			updates := make(chan client.UpdateEnvelope, 100)
			outbound, _ := connectDraftProductionOutbound(t, ctx, f, client.Config{OnUpdate: func(update client.UpdateEnvelope) { updates <- update }}, func(inner *RuntimeAgent) {
				inner.promptRouterFactory = func(_ context.Context, native session.Session) (controlprompt.Router, error) {
					svc.id = native.SessionID
					return controlprompt.New(controlprompt.RouterConfig{Service: svc}), nil
				}
			})
			cwd := t.TempDir()
			created, err := outbound.NewSession(ctx, cwd, nil)
			if err != nil {
				t.Fatal(err)
			}
			result, err := outbound.Prompt(ctx, created.SessionID, "/model fixture high", nil)
			if failAt == "none" {
				if err != nil || result.StopReason != "end_turn" {
					t.Fatalf("command completion=%#v, %v", result, err)
				}
			} else if err == nil || client.PromptOutcomeUnknown(err) || !strings.Contains(err.Error(), "foreground Activity failed") {
				t.Fatalf("inserted failure lost conclusive receipt=%#v, %v", result, err)
			}
			firstID := svc.messageID
			if firstID == "" {
				t.Fatal("command has no canonical persisted input")
			}
			if failAt == "ledger_receipt" && svc.effects != 0 {
				t.Fatalf("failed ledger dispatched command effect=%d", svc.effects)
			}
			if _, err := outbound.LoadSession(ctx, created.SessionID, cwd, nil); err != nil {
				t.Fatalf("command replay=%v", err)
			}
			var echoCount int
			for len(updates) != 0 {
				update := <-updates
				var fields struct {
					SessionUpdate string `json:"sessionUpdate"`
					MessageID     string `json:"messageId"`
				}
				if err := json.Unmarshal(update.Raw, &fields); err != nil {
					t.Fatal(err)
				}
				if strings.HasPrefix(fields.SessionUpdate, "user_message") && fields.MessageID == firstID {
					echoCount++
				}
			}
			if echoCount != 2 {
				t.Fatalf("live+replay canonical command echo count=%d", echoCount)
			}
			svc.recordErr, svc.dispatchErr = nil, nil
			result, err = outbound.Prompt(ctx, created.SessionID, "/model fixture high", nil)
			if err != nil || result.StopReason != "end_turn" || svc.messageID == firstID {
				t.Fatalf("next command after replay=%#v, %v; IDs=%q/%q", result, err, firstID, svc.messageID)
			}
		})
	}
}

type draftCancelledCommandService struct {
	*draftReceiptRouterService
	started chan struct{}
}

func (s draftCancelledCommandService) UseModel(ctx context.Context, _, _ string, _ bool) (controlstatus.StatusSnapshot, error) {
	close(s.started)
	<-ctx.Done()
	return controlstatus.StatusSnapshot{}, ctx.Err()
}

func TestDraftProductionOutboundCancelledCommandKeepsDurableReceipt(t *testing.T) {
	f := newDraftNativeFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	svc := draftCancelledCommandService{draftReceiptRouterService: &draftReceiptRouterService{fixture: f}, started: make(chan struct{})}
	outbound, agent := connectDraftProductionOutbound(t, ctx, f, client.Config{}, func(inner *RuntimeAgent) {
		inner.promptRouterFactory = func(_ context.Context, native session.Session) (controlprompt.Router, error) {
			svc.id = native.SessionID
			return controlprompt.New(controlprompt.RouterConfig{Service: svc}), nil
		}
	})
	created, err := outbound.NewSession(ctx, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	type completion struct {
		response client.PromptResponse
		err      error
	}
	completed := make(chan completion, 1)
	go func() {
		response, err := outbound.Prompt(ctx, created.SessionID, "/model fixture high", nil)
		completed <- completion{response, err}
	}()
	select {
	case <-svc.started:
	case <-ctx.Done():
		t.Fatal("command did not reach its controlled dispatch")
	}
	if err := agent.Cancel(ctx, v2.CancelSessionNotification{SessionId: v2.SessionId(created.SessionID)}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-completed:
		if result.err != nil || result.response.StopReason != "cancelled" || svc.messageID == "" {
			t.Fatalf("post-insertion command cancellation=%#v, canonical=%q", result, svc.messageID)
		}
	case <-ctx.Done():
		t.Fatal("cancelled durable command did not settle")
	}
	if svc.effects != 0 {
		t.Fatalf("cancelled command committed a model effect=%d", svc.effects)
	}
}

func TestDraftProductionOutboundMainTerminalPreservesDetachedChildApproval(t *testing.T) {
	f := newDraftNativeFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	requested, interrupted, reply := make(chan v2.RequestPermissionRequest, 2), make(chan struct{}, 1), make(chan struct{})
	outbound, _ := connectDraftProductionOutbound(t, ctx, f, client.Config{OnDraftPermissionRequest: func(ctx context.Context, request v2.RequestPermissionRequest) (v2.RequestPermissionResponse, error) {
		requested <- request
		select {
		case <-reply:
			return v2.RequestPermissionResponse{Outcome: v2.RequestPermissionOutcome{Selected: &v2.RequestPermissionOutcomeSelected{Outcome: "selected", OptionId: "child-original-option"}}}, nil
		case <-ctx.Done():
			interrupted <- struct{}{}
			return v2.RequestPermissionResponse{}, ctx.Err()
		}
	}}, nil)
	created, err := outbound.NewSession(ctx, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	completed := make(chan error, 1)
	go func() {
		result, err := outbound.Prompt(ctx, created.SessionID, "main", nil)
		if err == nil && result.StopReason != "end_turn" {
			err = errors.New("incorrect main completion")
		}
		completed <- err
	}()
	input := <-f.inserts
	f.insert(input)
	childTarget := appserver.TurnTarget{HandleID: "child-handle", RunID: "child-run", TurnID: "child-turn"}
	f.mu.Lock()
	state := f.states[input.sessionID]
	state.Approval.Active = &appserver.ActiveApproval{RequestID: "child-original-request", Target: childTarget, Scope: eventstream.ScopeSubagent, ScopeID: "child-task", Permission: &session.ProtocolApproval{ToolCall: session.ProtocolToolCall{ID: "child-original-tool", Title: "Child write"}, Options: []session.ProtocolApprovalOption{{ID: "child-original-option", Name: "Allow once", Kind: "allow_once"}}}}
	f.states[input.sessionID] = state
	feed := f.feeds[input.sessionID]
	f.mu.Unlock()
	if err := feed.Publish(eventstream.Envelope{Kind: eventstream.KindRequestPermission, SessionID: input.sessionID, ApprovalRequestID: "child-original-request"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-requested:
	case <-ctx.Done():
		t.Fatal("child approval was not presented")
	}
	f.finish(input.sessionID, false)
	select {
	case err := <-completed:
		if err != nil {
			t.Fatalf("background approval changed foreground completion: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("main terminal did not complete")
	}
	select {
	case <-interrupted:
		t.Fatal("main terminal cancelled independent child approval")
	case request := <-requested:
		t.Fatalf("main terminal duplicated independent child approval: %#v", request)
	case <-time.After(30 * time.Millisecond):
	}
	close(reply)
	select {
	case resolution := <-f.resolved:
		if resolution.Target != childTarget || resolution.ApprovalRequestID != "child-original-request" || resolution.OptionID != "child-original-option" {
			t.Fatalf("child native resolve changed=%#v", resolution)
		}
	case <-ctx.Done():
		t.Fatal("child approval was orphaned after main completion")
	}
}

func TestDraftProductionOutboundDisconnectRecoversOriginalApprovalAndCanonicalInput(t *testing.T) {
	f := newDraftNativeFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	firstRequest := make(chan v2.RequestPermissionRequest, 1)
	first, firstAgent := connectDraftProductionOutbound(t, ctx, f, client.Config{OnDraftPermissionRequest: func(ctx context.Context, request v2.RequestPermissionRequest) (v2.RequestPermissionResponse, error) {
		firstRequest <- request
		<-ctx.Done()
		return v2.RequestPermissionResponse{}, ctx.Err()
	}}, nil)
	cwd := t.TempDir()
	created, err := first.NewSession(ctx, cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	completed := make(chan error, 1)
	go func() { _, err := first.Prompt(ctx, created.SessionID, "before disconnect", nil); completed <- err }()
	input := <-f.inserts
	canonicalID := f.insert(input)
	f.mu.Lock()
	state := f.states[input.sessionID]
	state.Run.WaitingApproval = true
	state.Approval.Active = &appserver.ActiveApproval{RequestID: "recover-original-request", Target: input.target, Permission: &session.ProtocolApproval{ToolCall: session.ProtocolToolCall{ID: "recover-original-tool", Title: "Write"}, Options: []session.ProtocolApprovalOption{{ID: "recover-original-option", Name: "Allow once", Kind: "allow_once"}}}}
	f.states[input.sessionID] = state
	feed := f.feeds[input.sessionID]
	f.mu.Unlock()
	if err := feed.Publish(eventstream.Envelope{Kind: eventstream.KindRequestPermission, SessionID: input.sessionID, ApprovalRequestID: "recover-original-request"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstRequest:
	case <-ctx.Done():
		t.Fatal("first observer did not present native approval")
	}
	_ = first.Close(ctx)
	_ = firstAgent.Close()
	select {
	case err := <-completed:
		if err == nil || !client.PromptOutcomeUnknown(err) {
			t.Fatalf("disconnect falsely settled durable input: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("old connection did not settle as unknown")
	}
	f.mu.Lock()
	pending := f.states[input.sessionID]
	f.mu.Unlock()
	if !pending.Run.Active || pending.Approval.Active == nil || pending.Approval.Active.Target != input.target {
		t.Fatalf("disconnect mutated native execution or approval: %#v", pending)
	}
	recoveredRequest := make(chan v2.RequestPermissionRequest, 1)
	updates := make(chan client.UpdateEnvelope, 100)
	second, _ := connectDraftProductionOutbound(t, ctx, f, client.Config{OnUpdate: func(update client.UpdateEnvelope) { updates <- update }, OnDraftPermissionRequest: func(_ context.Context, request v2.RequestPermissionRequest) (v2.RequestPermissionResponse, error) {
		recoveredRequest <- request
		return v2.RequestPermissionResponse{Outcome: v2.RequestPermissionOutcome{Selected: &v2.RequestPermissionOutcomeSelected{Outcome: "selected", OptionId: "recover-original-option"}}}, nil
	}}, nil)
	if _, err := second.LoadSession(ctx, created.SessionID, cwd, nil); err != nil {
		t.Fatalf("resume original Session=%v", err)
	}
	select {
	case request := <-recoveredRequest:
		if request.Subject == nil || request.Subject.ToolCall == nil || request.Subject.ToolCall.ToolCall.ToolCallId != "recover-original-tool" {
			t.Fatalf("recovered native approval changed=%#v", request)
		}
	case <-ctx.Done():
		t.Fatal("reconnect orphaned original pending approval")
	}
	select {
	case resolution := <-f.resolved:
		if resolution.Target != input.target || resolution.ApprovalRequestID != "recover-original-request" || resolution.OptionID != "recover-original-option" {
			t.Fatalf("recovered resolve changed native authority=%#v", resolution)
		}
	case <-ctx.Done():
		t.Fatal("original pending approval did not resolve")
	}
	found := false
	for len(updates) != 0 {
		update := <-updates
		var fields struct {
			SessionUpdate string `json:"sessionUpdate"`
			MessageID     string `json:"messageId"`
		}
		_ = json.Unmarshal(update.Raw, &fields)
		found = found || strings.HasPrefix(fields.SessionUpdate, "user_message") && fields.MessageID == canonicalID
	}
	if !found {
		t.Fatalf("reconnect lost canonical user input=%q", canonicalID)
	}
	f.finish(created.SessionID, false)
}

type draftNativeTaskService struct{ *acpMuxReconnectService }

func (s draftNativeTaskService) List(ctx context.Context, principal taskstream.Principal, request taskstream.ListRequest) (taskstream.ListResult, error) {
	result, err := s.acpMuxReconnectService.List(ctx, principal, request)
	for i := range result.Tasks {
		result.Tasks[i].SessionID = request.SessionID
	}
	return result, err
}

func TestDraftProductionOutboundTaskDisconnectCommitsStandardReplacement(t *testing.T) {
	f := newDraftNativeFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	first := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 2)}
	replacement := &acpMuxTestSubscription{deliveries: make(chan taskstream.Delivery, 4)}
	service := draftNativeTaskService{newACPMuxReconnectService([]acpMuxSubscribeStep{{sub: first}, {sub: replacement}})}
	tasks, err := taskstream.BindClient(service, taskstream.Principal{ID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	type output struct {
		acpmeta.TerminalOutput
		raw json.RawMessage
	}
	outputs := make(chan output, 20)
	outbound, _ := connectDraftProductionOutbound(t, ctx, f, client.Config{OnUpdate: func(update client.UpdateEnvelope) {
		if tool, ok := update.Update.(client.ToolCallUpdate); ok {
			if value, ok := acpmeta.ReadTerminalOutput(tool.Meta); ok {
				outputs <- output{value, update.Raw}
			}
		}
	}}, func(inner *RuntimeAgent) { inner.taskStreamClient = tasks })
	created, err := outbound.NewSession(ctx, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	anchor := acpMuxCommandAnchor("command")
	anchor.SessionID = created.SessionID
	f.mu.Lock()
	feed := f.feeds[created.SessionID]
	f.mu.Unlock()
	if err := feed.Publish(anchor); err != nil {
		t.Fatal(err)
	}
	initial := receiveACPTaskStreamRequest(t, service.requests)
	if initial.SessionID != created.SessionID || initial.TaskID != "task-1" {
		t.Fatalf("Task native attachment changed scope=%#v", initial)
	}
	prefix := acpMuxCommandOutputEnvelope("native-prefix-cursor", "old prefix")
	prefix.SessionID = created.SessionID
	first.events <- prefix
	var rendered string
	var replaced bool
	readOutput := func(want string) output {
		t.Helper()
		for {
			select {
			case got := <-outputs:
				if strings.Contains(string(got.raw), `"terminal_output"`) || strings.Contains(string(got.raw), `"terminal_info"`) {
					t.Fatalf("legacy terminal extension escaped draft wire=%s", got.raw)
				}
				if got.Replacement {
					rendered, replaced = got.Data, true
				} else {
					rendered += got.Data
				}
				if got.Data == want {
					return got
				}
			case <-ctx.Done():
				t.Fatalf("Task standard output %q was lost", want)
				return output{}
			}
		}
	}
	if got := readOutput("old prefix"); got.Replacement || got.TerminalID != "command-1" {
		t.Fatalf("live Task output semantics=%#v", got)
	}
	first.finish(errorcode.New(errorcode.Unavailable, "native Task trace expired"))
	resumed := receiveACPTaskStreamRequest(t, service.requests)
	if resumed.Cursor != "native-prefix-cursor" || resumed.TaskID != initial.TaskID || resumed.SessionID != initial.SessionID {
		t.Fatalf("Task reconnect changed native identity/cursor=%#v", resumed)
	}
	snapshot := acpMuxCommandOutputEnvelope("", "replacement output")
	snapshot.SessionID, snapshot.Position = created.SessionID, nil
	replacement.deliveries <- taskstream.Delivery{Kind: taskstream.DeliveryReplaceBegin, Source: taskstream.SourceReplacement, SnapshotID: "native-task-snapshot"}
	replacement.deliveries <- taskstream.Delivery{Kind: taskstream.DeliveryReplacePage, Source: taskstream.SourceReplacement, SnapshotID: "native-task-snapshot", Events: []eventstream.Envelope{snapshot}}
	select {
	case got := <-outputs:
		t.Fatalf("uncommitted Task replacement was presented=%#v", got)
	case <-time.After(30 * time.Millisecond):
	}
	replacement.deliveries <- taskstream.Delivery{Kind: taskstream.DeliveryReplaceEnd, Source: taskstream.SourceReplacement, SnapshotID: "native-task-snapshot", Page: 1}
	if got := readOutput("replacement output"); !replaced || rendered != "replacement output" || got.TerminalID != "command-1" {
		t.Fatalf("Task replacement retained stale prefix: rendered=%q, replaced=%v, frame=%#v", rendered, replaced, got)
	}
	completed := make(chan error, 1)
	go func() {
		_, err := outbound.Prompt(ctx, created.SessionID, "independent foreground", nil)
		completed <- err
	}()
	input := <-f.inserts
	f.insert(input)
	f.finish(created.SessionID, false)
	select {
	case err := <-completed:
		if err != nil {
			t.Fatalf("Task trace changed foreground completion=%v", err)
		}
	case <-ctx.Done():
		t.Fatal("foreground completion was lost")
	}
	after := acpMuxCommandOutputEnvelope("native-after-cursor", "after foreground")
	after.SessionID = created.SessionID
	after = acpMuxExactEnvelope(after, 2)
	replacement.deliveries <- taskstream.Delivery{Kind: taskstream.DeliveryAppendPage, Source: taskstream.SourceExact, Events: []eventstream.Envelope{after}, NextCursor: after.Cursor}
	if got := readOutput("after foreground"); got.Replacement {
		t.Fatalf("foreground completion reset independent Task output=%#v", got)
	}
}

type draftHeldUserEcho struct {
	DraftCallbacks
	started, release chan struct{}
}

func (c draftHeldUserEcho) SessionUpdate(ctx context.Context, update v2.UpdateSessionNotification) error {
	var fields struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	raw, _ := json.Marshal(update.Update)
	_ = json.Unmarshal(raw, &fields)
	if fields.SessionUpdate == "user_message" {
		close(c.started)
		select {
		case <-c.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return c.DraftCallbacks.SessionUpdate(ctx, update)
}

func TestDraftCancelAdmittedTurnCannotEraseInsertedReceiptWhileEchoIsPublishing(t *testing.T) {
	f := newDraftNativeFixture(t)
	peer, agent, _ := connectDraftNative(t, f)
	started, release := make(chan struct{}), make(chan struct{})
	agent.callbacks = draftHeldUserEcho{DraftCallbacks: agent.callbacks, started: started, release: release}
	created, err := peer.NewSession(t.Context(), v2.NewSessionRequest{Cwd: v2.AbsolutePath(t.TempDir())})
	if err != nil {
		t.Fatal(err)
	}
	type completion struct {
		receipt v2.PromptResponse
		err     error
	}
	completed := make(chan completion, 1)
	go func() {
		receipt, err := peer.Prompt(t.Context(), draftNativePrompt(string(created.SessionId), "inserted before cancel"))
		completed <- completion{receipt, err}
	}()
	input := <-f.inserts
	canonicalID := f.insert(input)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("canonical input did not reach its held echo")
	}
	if err := agent.Cancel(t.Context(), v2.CancelSessionNotification{SessionId: created.SessionId}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-completed:
		t.Fatalf("cancel erased inserted input before same-ID echo: %#v", result)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case result := <-completed:
		if result.err != nil || string(result.receipt.MessageId) != canonicalID || acpdraft.ActivityID(result.receipt.Meta) != input.target.TurnID {
			t.Fatalf("canonical cancelled input receipt=%#v, native=%q/%q", result, canonicalID, input.target.TurnID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("true inserted receipt did not follow its echo")
	}
}
