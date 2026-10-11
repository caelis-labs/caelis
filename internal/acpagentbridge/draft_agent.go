package acpagentbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/acpdraft"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

// DraftCallbacks belongs to one experimental connection. It conveys no
// principal or permission authority; those remain on the bound Control client.
type DraftCallbacks interface {
	SessionUpdate(context.Context, v2.UpdateSessionNotification) error
	RequestPermission(context.Context, v2.RequestPermissionRequest) (v2.RequestPermissionResponse, error)
}

// DraftAgent projects the pinned asynchronous ACP protocol over the same
// principal-bound Control clients as product v1. It never invokes v1 Prompt.
type DraftAgent struct {
	inner       *RuntimeAgent
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	initialized bool
	callbacks   DraftCallbacks
	ready       chan struct{}
	sessions    map[string]*draftSession
}

type draftSession struct {
	agent             *DraftAgent
	id                string
	ctx               context.Context
	cancel            context.CancelFunc
	sub               appserver.FeedSubscription
	done              chan struct{}
	promptMu          sync.Mutex
	projectionMu      sync.Mutex
	mu                sync.Mutex
	state             appserver.SessionState
	users             map[string]eventstream.Envelope
	waiter            *draftReceipt
	pending           *draftPromptAdmission
	stateVersion      uint64                // protected by projectionMu
	terminalActivity  string                // protected by projectionMu
	lastForeground    *draftForegroundState // protected by projectionMu
	err               error
	approvalID        eventstream.ApprovalRequestID
	approvalObserved  bool
	approvalCancel    context.CancelFunc
	mux               *acpTaskStreamMux
	childTerminal     *acpChildTerminalProjector
	terminalResets    map[string]string
	narrativeResets   map[string]string // projection-only replay epochs, protected by projectionMu
	childReplacements map[string]string
}

type draftForegroundState struct {
	activityID string
	active     bool
	action     bool
	stopReason string
}

type draftPromptAdmission struct {
	cancel context.CancelCauseFunc
	target appserver.TurnTarget // protected by the Session mutex
}

type draftReceipt struct {
	operationID   string
	interactionID string
	target        appserver.TurnTarget
	steer         bool
	message       chan string
	failed        chan error
}

// NewGatewayDraftAgent constructs the experimental facade from the existing
// product client assembly. Exact draft selection is performed by its transport.
func NewGatewayDraftAgent(ctx context.Context, cfg GatewayAgentConfig) (*DraftAgent, error) {
	inner, err := NewGatewayAgent(cfg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	return &DraftAgent{inner: inner, ctx: ctx, cancel: cancel, ready: make(chan struct{}), sessions: make(map[string]*draftSession)}, nil
}

// BindDraftCallbacks binds the exact submitting connection before admission.
func (a *DraftAgent) BindDraftCallbacks(callbacks DraftCallbacks) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.callbacks == nil && callbacks != nil {
		a.callbacks = callbacks
		close(a.ready)
	}
}

// Close detaches this connection's observations without closing durable Sessions.
func (a *DraftAgent) Close() error {
	a.cancel()
	a.mu.Lock()
	sessions := make([]*draftSession, 0, len(a.sessions))
	for _, s := range a.sessions {
		sessions = append(sessions, s)
	}
	a.mu.Unlock()
	for _, s := range sessions {
		s.cancel()
		_ = s.sub.Close()
		<-s.done
	}
	return nil
}

func (a *DraftAgent) Initialize(ctx context.Context, req v2.InitializeRequest) (v2.InitializeResponse, error) {
	if err := acpdraft.ValidateInitialize(req); err != nil {
		return v2.InitializeResponse{}, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	select {
	case <-a.ready:
	case <-ctx.Done():
		return v2.InitializeResponse{}, ctx.Err()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.initialized {
		return v2.InitializeResponse{}, acp.NewInvalidRequest(map[string]any{"error": "ACP connection is already initialized"})
	}
	caps, err := a.inner.presentationClient.PresentationCapabilities(ctx)
	if err != nil {
		return v2.InitializeResponse{}, err
	}
	info, err := a.inner.sessionClient.Initialize(ctx)
	if err != nil {
		return v2.InitializeResponse{}, err
	}
	if !slices.Contains(info.Capabilities, appserver.CapabilityConversationInteractions) {
		return v2.InitializeResponse{}, acp.NewInvalidRequest(map[string]any{"error": "ACP draft requires durable conversation interactions"})
	}
	var prompt v2.PromptCapabilities
	if caps.Image {
		prompt.Image = &v2.PromptImageCapabilities{}
	}
	a.initialized = true
	return v2.InitializeResponse{ProtocolVersion: 2, Info: v2.Implementation{Name: a.inner.agentInfo.Name, Title: a.inner.agentInfo.Title, Version: a.inner.agentInfo.Version}, Meta: acpdraft.InitializeMeta(), Capabilities: v2.AgentCapabilities{Session: &v2.SessionCapabilities{Prompt: &prompt}}}, nil
}

func (a *DraftAgent) admitted() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.initialized {
		return acp.NewInvalidRequest(map[string]any{"error": "initialize is required"})
	}
	if err := a.ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (a *DraftAgent) NewSession(ctx context.Context, req v2.NewSessionRequest) (v2.NewSessionResponse, error) {
	if err := a.admitted(); err != nil {
		return v2.NewSessionResponse{}, err
	}
	if len(req.AdditionalDirectories) != 0 || len(req.McpServers) != 0 {
		return v2.NewSessionResponse{}, acp.NewInvalidParams(map[string]any{"error": "Session workspace roots and MCP selection are not advertised"})
	}
	created, err := a.inner.NewSession(ctx, acp.NewSessionRequest{Cwd: string(req.Cwd), Meta: req.Meta})
	if err != nil {
		return v2.NewSessionResponse{}, err
	}
	if _, err = a.observe(ctx, string(created.SessionId), false); err != nil {
		return v2.NewSessionResponse{}, err
	}
	response := v2.NewSessionResponse{SessionId: v2.SessionId(created.SessionId)}
	response.ConfigOptions, err = draftConfigOptions(created.ConfigOptions)
	if err == nil {
		response.AvailableCommands, err = a.draftAvailableCommands(ctx, string(created.SessionId))
	}
	return response, err
}

func (a *DraftAgent) ResumeSession(ctx context.Context, req v2.ResumeSessionRequest) (v2.ResumeSessionResponse, error) {
	if err := a.admitted(); err != nil {
		return v2.ResumeSessionResponse{}, err
	}
	if len(req.AdditionalDirectories) != 0 || len(req.McpServers) != 0 {
		return v2.ResumeSessionResponse{}, acp.NewInvalidParams(map[string]any{"error": "Session workspace roots and MCP selection are not advertised"})
	}
	if req.ReplayFrom != nil && req.ReplayFrom.Start == nil {
		return v2.ResumeSessionResponse{}, acp.NewInvalidParams(map[string]any{"error": "only replayFrom start is supported"})
	}
	state, err := a.inner.sessionClient.InspectSession(ctx, appserver.StateRequest{SessionID: string(req.SessionId)})
	if err != nil {
		return v2.ResumeSessionResponse{}, err
	}
	if state.CWD != "" && canonicalACPWorkspacePath(state.CWD) != canonicalACPWorkspacePath(string(req.Cwd)) {
		return v2.ResumeSessionResponse{}, acp.NewInvalidParams(map[string]any{"error": "resume workspace differs from the native Session"})
	}
	response, err := a.inner.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: acp.SessionId(req.SessionId), Cwd: string(req.Cwd), Meta: req.Meta})
	if err != nil {
		return v2.ResumeSessionResponse{}, err
	}
	if _, err = a.observe(ctx, string(req.SessionId), req.ReplayFrom != nil); err != nil {
		return v2.ResumeSessionResponse{}, err
	}
	result := v2.ResumeSessionResponse{}
	result.ConfigOptions, err = draftConfigOptions(response.ConfigOptions)
	if err == nil {
		result.AvailableCommands, err = a.draftAvailableCommands(ctx, string(req.SessionId))
	}
	return result, err
}

func (a *DraftAgent) ListSessions(ctx context.Context, req v2.ListSessionsRequest) (v2.ListSessionsResponse, error) {
	if err := a.admitted(); err != nil {
		return v2.ListSessionsResponse{}, err
	}
	var request acp.ListSessionsRequest
	if err := draftJSONConvert(req, &request); err != nil {
		return v2.ListSessionsResponse{}, err
	}
	response, err := a.inner.ListSessions(ctx, request)
	if err != nil {
		return v2.ListSessionsResponse{}, err
	}
	var result v2.ListSessionsResponse
	err = draftJSONConvert(response, &result)
	return result, err
}

func (a *DraftAgent) CloseSession(ctx context.Context, req v2.CloseSessionRequest) (v2.CloseSessionResponse, error) {
	if err := a.admitted(); err != nil {
		return v2.CloseSessionResponse{}, err
	}
	_, err := a.inner.CloseSession(ctx, acp.CloseSessionRequest{SessionId: acp.SessionId(req.SessionId)})
	if err == nil {
		a.mu.Lock()
		s := a.sessions[string(req.SessionId)]
		delete(a.sessions, string(req.SessionId))
		a.mu.Unlock()
		if s != nil {
			s.cancel()
			_ = s.sub.Close()
			<-s.done
		}
	}
	return v2.CloseSessionResponse{}, err
}

func (a *DraftAgent) SetSessionConfigOption(ctx context.Context, req v2.SetSessionConfigOptionRequest) (v2.SetSessionConfigOptionResponse, error) {
	if err := a.admitted(); err != nil {
		return v2.SetSessionConfigOptionResponse{}, err
	}
	var request acp.SetSessionConfigOptionRequest
	if err := draftJSONConvert(req, &request); err != nil {
		return v2.SetSessionConfigOptionResponse{}, err
	}
	response, err := a.inner.SetSessionConfigOption(ctx, request)
	if err != nil {
		return v2.SetSessionConfigOptionResponse{}, err
	}
	var result v2.SetSessionConfigOptionResponse
	result.ConfigOptions, err = draftConfigOptions(response.ConfigOptions)
	return result, err
}

func (a *DraftAgent) Cancel(ctx context.Context, req v2.CancelSessionNotification) error {
	if err := a.admitted(); err != nil {
		return err
	}
	if _, err := a.inner.targetSession(ctx, string(req.SessionId)); err != nil {
		return err
	}
	a.mu.Lock()
	s := a.sessions[string(req.SessionId)]
	a.mu.Unlock()
	if s != nil {
		s.mu.Lock()
		pending := s.pending
		var target appserver.TurnTarget
		if pending != nil {
			target = pending.target
			if target.TurnID == "" {
				pending.cancel(controlprompt.ErrUserInterrupt)
			}
		}
		s.mu.Unlock()
		if pending != nil {
			if target.TurnID == "" {
				return nil
			}
			err := a.cancelDraftTarget(ctx, s.id, target)
			pending.cancel(controlprompt.ErrUserInterrupt)
			return err
		}
	}
	state, err := a.inner.sessionClient.InspectSession(ctx, appserver.StateRequest{SessionID: string(req.SessionId)})
	if err != nil || !state.Run.Active {
		return err
	}
	return a.cancelDraftTarget(ctx, state.SessionID, draftRunTarget(state.Run))
}

func (a *DraftAgent) cancelDraftTarget(ctx context.Context, id string, target appserver.TurnTarget) error {
	result, err := a.inner.sessionClient.Cancel(ctx, appserver.CancelRequest{WriteBase: appserver.WriteBase{OperationID: newACPSessionOperationID("draft-cancel"), SessionID: id}, Target: target, Reason: "ACP session/cancel"})
	return appserver.CommandMutationError(result, err)
}

func (a *DraftAgent) Prompt(ctx context.Context, req v2.PromptRequest) (v2.PromptResponse, error) {
	if err := a.admitted(); err != nil {
		return v2.PromptResponse{}, err
	}
	guard, err := acpdraft.ParseActiveOnly(req.Meta)
	if err != nil {
		return v2.PromptResponse{}, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	input, contentParts, err := draftPromptContent(req)
	if err != nil {
		return v2.PromptResponse{}, err
	}
	s, err := a.observe(ctx, string(req.SessionId), false)
	if err != nil {
		return v2.PromptResponse{}, err
	}
	s.promptMu.Lock()
	defer s.promptMu.Unlock()
	admissionCtx, cancelAdmission := context.WithCancelCause(ctx)
	pending := &draftPromptAdmission{cancel: cancelAdmission}
	s.mu.Lock()
	s.pending = pending
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.pending == pending {
			s.pending = nil
		}
		s.mu.Unlock()
		cancelAdmission(nil)
	}()
	ctx = admissionCtx
	state, err := a.inner.sessionClient.InspectSession(ctx, appserver.StateRequest{SessionID: s.id})
	if err != nil {
		return v2.PromptResponse{}, err
	}
	if guard != nil {
		if guard.IdleOnly {
			if state.Run.Active {
				return v2.PromptResponse{}, acpdraft.InactivePromptError()
			}
		} else if !steerableMainRun(state.Run) || state.Run.TurnID != guard.ActivityID {
			return v2.PromptResponse{}, acpdraft.InactivePromptError()
		}
	}
	if err := ctx.Err(); err != nil {
		return v2.PromptResponse{}, err
	}
	operationID := newACPSessionOperationID("draft-prompt")
	waiter := &draftReceipt{operationID: operationID, steer: state.Run.Active, message: make(chan string, 1), failed: make(chan error, 1)}
	s.mu.Lock()
	clear(s.users)
	s.waiter = waiter
	if state.Run.Active {
		pending.target = draftRunTarget(state.Run)
		waiter.target = pending.target
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.waiter == waiter {
			s.waiter = nil
		}
		s.mu.Unlock()
	}()
	base := appserver.WriteBase{OperationID: operationID, SessionID: s.id, ExpectedControllerEpoch: state.Controller.EpochID}
	var result appserver.CommandResult
	if (guard == nil || guard.IdleOnly) && (!waiter.steer || strings.HasPrefix(strings.TrimSpace(input), "/")) {
		result, err = a.routePrompt(ctx, s, state, input, contentParts, waiter, guard != nil && guard.IdleOnly)
	} else if waiter.steer {
		if !steerableMainRun(state.Run) {
			return v2.PromptResponse{}, acpdraft.InactivePromptError()
		}
		s.mu.Lock()
		waiter.target = draftRunTarget(state.Run)
		s.mu.Unlock()
		result, err = a.inner.sessionClient.Steer(ctx, appserver.SteerRequest{WriteBase: base, Target: waiter.target, Input: input, ContentParts: contentParts})
	} else {
		revision := state.Revision
		base.ExpectedRevision = &revision
		result, err = a.inner.sessionClient.Prompt(ctx, appserver.PromptRequest{WriteBase: base, Input: input, ContentParts: contentParts})
	}
	if mutationErr := appserver.CommandMutationError(result, err); mutationErr != nil {
		if guard != nil && draftNoEffectOutcome(result, mutationErr) {
			return v2.PromptResponse{}, acpdraft.InactivePromptError()
		}
		if result.Target.TurnID == "" || result.Outcome != appserver.OutcomeUnknown {
			return v2.PromptResponse{}, mutationErr
		}
		// An admitted native target can outlive a failed ledger receipt. Keep
		// observing its real insertion rather than retrying or inventing an ID.
		if noticeErr := a.draftNotice(s.ctx, s.id, mutationErr.Error()); noticeErr != nil {
			return v2.PromptResponse{}, noticeErr
		}
	}
	s.mu.Lock()
	if result.Target.TurnID != "" {
		waiter.target = result.Target
		pending.target = result.Target
	}
	for _, env := range s.users {
		s.settleReceiptLocked(env)
	}
	if !s.state.Run.Active && s.state.Run.TurnID == waiter.target.TurnID && waiter.target.TurnID != "" {
		s.failUninsertedReceiptLocked(waiter.target.TurnID)
	}
	s.mu.Unlock()
	if ctx.Err() != nil && result.Target.TurnID != "" && errors.Is(context.Cause(ctx), controlprompt.ErrUserInterrupt) {
		cancelCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		_ = a.cancelDraftTarget(cancelCtx, s.id, result.Target)
	}
	select {
	case messageID := <-waiter.message:
		return s.promptReceipt(waiter, messageID), nil
	case insertionErr := <-waiter.failed:
		select {
		case messageID := <-waiter.message:
			return s.promptReceipt(waiter, messageID), nil
		default:
			return v2.PromptResponse{}, insertionErr
		}
	case <-ctx.Done():
		s.mu.Lock()
		insertedCommand := waiter.interactionID != ""
		cancelledAdmittedTurn := waiter.target.TurnID != "" && errors.Is(context.Cause(ctx), controlprompt.ErrUserInterrupt)
		s.mu.Unlock()
		if insertedCommand || cancelledAdmittedTurn {
			// Session cancellation cannot erase an inserted input whose echo is
			// still being published. An admitted Turn's native terminal also
			// proves when the pending input was never inserted.
			select {
			case messageID := <-waiter.message:
				return s.promptReceipt(waiter, messageID), nil
			case insertionErr := <-waiter.failed:
				select {
				case messageID := <-waiter.message:
					return s.promptReceipt(waiter, messageID), nil
				default:
					return v2.PromptResponse{}, insertionErr
				}
			case <-s.done:
				return v2.PromptResponse{}, errors.New("ACP observation ended before canonical input echo")
			}
		}
		select {
		case messageID := <-waiter.message:
			return s.promptReceipt(waiter, messageID), nil
		default:
			return v2.PromptResponse{}, ctx.Err()
		}
	case <-s.done:
		s.mu.Lock()
		err := s.err
		s.mu.Unlock()
		if err == nil {
			err = errors.New("ACP observation ended before canonical user insertion")
		}
		return v2.PromptResponse{}, err
	}
}

// A receipt identifies the Activity that owns this accepted input. Ambient
// Session state can belong to another caller by the time this response arrives.
func (s *draftSession) promptReceipt(waiter *draftReceipt, messageID string) v2.PromptResponse {
	s.mu.Lock()
	activityID := waiter.target.TurnID
	if activityID == "" {
		activityID = waiter.interactionID
	}
	s.mu.Unlock()
	return v2.PromptResponse{MessageId: v2.MessageId(messageID), Meta: acpdraft.ActivityMeta(activityID)}
}

func (a *DraftAgent) routePrompt(ctx context.Context, s *draftSession, state appserver.SessionState, input string, parts []model.ContentPart, waiter *draftReceipt, requireIdle bool) (appserver.CommandResult, error) {
	active, err := a.inner.targetSession(ctx, s.id)
	if err != nil {
		return appserver.CommandResult{}, err
	}
	router, err := a.inner.promptRouterFactory(ctx, active)
	if err != nil {
		return appserver.CommandResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return appserver.CommandResult{}, err
	}
	commandStarted := false
	result, routeErr := router.Route(ctx, controlprompt.Request{
		Submission:                controlprompt.Submission{Text: input, Attachments: promptRouterAttachmentsFromContentParts(input, parts)},
		RequireInteractionReceipt: true,
		RequireIdleRun:            requireIdle,
		InteractionObserver: func(ctx context.Context, id string) error {
			s.mu.Lock()
			waiter.interactionID, waiter.steer = id, false
			s.mu.Unlock()
			if state.Run.Active {
				return nil
			}
			commandStarted = true
			return s.emitCommandState(id, true, "")
		},
	})
	if result.Turn != nil {
		defer result.Turn.Close()
	}
	if result.Reconnect != nil {
		defer result.Reconnect.Close()
	}
	if routeErr != nil && result.InteractionMessageID == "" {
		return appserver.CommandResult{}, routeErr
	}
	projectionCtx := ctx
	if result.InteractionMessageID != "" {
		projectionCtx = s.ctx
	}
	s.mu.Lock()
	waiter.interactionID = result.InteractionMessageID
	waiter.steer = false
	s.mu.Unlock()
	if result.InteractionMessageID != "" && !state.Run.Active && !commandStarted {
		if err := s.emitCommandState(result.InteractionMessageID, true, ""); err != nil {
			return appserver.CommandResult{}, err
		}
	}
	if result.ParticipantTask != nil {
		s.mu.Lock()
		mux := s.mux
		s.mu.Unlock()
		if mux != nil {
			mux.ObserveTask(result.ParticipantTask.TaskID)
		}
	}
	for _, env := range result.Events {
		if env.Update != nil {
			if err := s.emitEnvelope(env, false, false); err != nil {
				return appserver.CommandResult{}, err
			}
		}
		if env.Notice != "" {
			if err := a.draftNotice(projectionCtx, s.id, env.Notice); err != nil {
				return appserver.CommandResult{}, err
			}
		}
	}
	if result.SlashResult != nil {
		if err := a.draftNotice(projectionCtx, s.id, a.inner.slashResultFormatter(*result.SlashResult)); err != nil {
			return appserver.CommandResult{}, err
		}
	}
	if result.StatusUpdate != nil || result.ClearHistory || result.RefreshStatus || result.RefreshCommands {
		targetID := promptRouterResultSessionID(active, result)
		if err := a.draftRefreshPresentation(projectionCtx, targetID, result.RefreshCommands); err != nil {
			if routeErr == nil {
				routeErr = err
			}
		}
	}
	if routeErr != nil {
		if err := a.draftNotice(projectionCtx, s.id, routeErr.Error()); err != nil {
			return appserver.CommandResult{}, err
		}
	}
	target := appserver.TurnTarget{}
	if result.Turn != nil {
		target = appserver.TurnTarget{HandleID: result.Turn.HandleID(), RunID: result.Turn.RunID(), TurnID: result.Turn.TurnID()}
	}
	if result.InteractionMessageID == "" && target.TurnID == "" {
		return appserver.CommandResult{}, errors.New("ACP draft command did not admit a canonical conversation message")
	}
	if result.InteractionMessageID != "" && !state.Run.Active {
		stopReason := "end_turn"
		if routeErr != nil {
			stopReason = "error"
			if errors.Is(context.Cause(ctx), controlprompt.ErrUserInterrupt) {
				stopReason = "cancelled"
			}
		}
		current, inspectErr := a.inner.sessionClient.InspectSession(projectionCtx, appserver.StateRequest{SessionID: s.id})
		if inspectErr != nil {
			stopReason = "error"
			if err := a.draftNotice(projectionCtx, s.id, inspectErr.Error()); err != nil {
				return appserver.CommandResult{}, err
			}
		}
		if !current.Run.Active && target.TurnID == "" {
			if err := s.emitCommandState(result.InteractionMessageID, false, stopReason); err != nil {
				return appserver.CommandResult{}, err
			}
		}
	}
	return appserver.CommandResult{OperationID: waiter.operationID, Outcome: appserver.OutcomeCommitted, SessionID: s.id, Target: target}, nil
}

func draftNoEffectOutcome(result appserver.CommandResult, err error) bool {
	if result.Resource != nil || result.Target.TurnID != "" {
		return false
	}
	outcome := result.Outcome
	var classified *appserver.OutcomeError
	_ = errors.As(err, &classified)
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		var receipt *appserver.CommandReceiptError
		if errors.As(cause, &receipt) {
			if receipt.Receipt.Resource != nil || receipt.Receipt.Target.TurnID != "" {
				return false
			}
			if outcome == "" {
				outcome = receipt.Receipt.Outcome
			}
		}
	}
	if outcome == "" && classified != nil {
		outcome = classified.Outcome
	}
	return outcome == appserver.OutcomeConflicted || outcome == appserver.OutcomeRejected
}

func (a *DraftAgent) draftNotice(ctx context.Context, sessionID, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var update v2.SessionUpdate
	if err := draftJSONConvert(map[string]any{"sessionUpdate": "notice", "severity": "info", "title": text}, &update); err != nil {
		return err
	}
	return a.callbacks.SessionUpdate(ctx, v2.UpdateSessionNotification{SessionId: v2.SessionId(sessionID), Update: update})
}

func (a *DraftAgent) LoginAuth(context.Context, v2.LoginAuthRequest) (v2.LoginAuthResponse, error) {
	return v2.LoginAuthResponse{}, acp.NewMethodNotFound(v2.AgentMethodAuthLogin)
}

func (a *DraftAgent) LogoutAuth(context.Context, v2.LogoutAuthRequest) (v2.LogoutAuthResponse, error) {
	return v2.LogoutAuthResponse{}, acp.NewMethodNotFound(v2.AgentMethodAuthLogout)
}

func draftRunTarget(run appserver.RunState) appserver.TurnTarget {
	return appserver.TurnTarget{HandleID: run.HandleID, RunID: run.RunID, TurnID: run.TurnID}
}

func draftJSONConvert(value any, target any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("ACP draft projection: %w", err)
	}
	return nil
}

var _ v2.Agent = (*DraftAgent)(nil)
var _ v2.AgentSessionResumer = (*DraftAgent)(nil)
var _ v2.AgentSessionCloser = (*DraftAgent)(nil)
var _ v2.AgentSessionLister = (*DraftAgent)(nil)
var _ v2.AgentSessionConfig = (*DraftAgent)(nil)
