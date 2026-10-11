package acpagentbridge

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func (a *DraftAgent) observe(ctx context.Context, sessionID string, replay bool) (*draftSession, error) {
	if _, err := a.inner.targetSession(ctx, sessionID); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var previousForeground *draftForegroundState
	if previous := a.sessions[sessionID]; previous != nil {
		if !replay {
			return previous, nil
		}
		previous.cancel()
		_ = previous.sub.Close()
		<-previous.done
		previous.projectionMu.Lock()
		if previous.lastForeground != nil {
			retained := *previous.lastForeground
			previousForeground = &retained
		}
		previous.projectionMu.Unlock()
		delete(a.sessions, sessionID)
	}
	state, err := a.inner.sessionClient.InspectSession(ctx, appserver.StateRequest{SessionID: sessionID})
	if err != nil {
		return nil, err
	}
	feedCtx, cancel := context.WithCancel(a.ctx)
	request := appserver.ReconnectRequest{SessionID: sessionID}
	if !replay {
		request.Cursor = state.BoundaryCursor
	}
	connected, err := a.inner.sessionClient.Reconnect(feedCtx, request)
	if err != nil {
		cancel()
		return nil, err
	}
	if connected.Subscription == nil {
		cancel()
		return nil, errors.New("ACP draft reconnect returned no subscription")
	}
	s := &draftSession{agent: a, id: sessionID, ctx: feedCtx, cancel: cancel, sub: connected.Subscription, done: make(chan struct{}), state: connected.State, users: make(map[string]eventstream.Envelope), childTerminal: newACPChildTerminalProjector(), terminalResets: make(map[string]string), childReplacements: make(map[string]string)}
	s.lastForeground = previousForeground
	if previousForeground != nil && connected.State.Run.TurnID == previousForeground.activityID {
		// A passive read cannot supersede this connection's known lifecycle.
		s.state.Run.Active = previousForeground.active
		if !previousForeground.active {
			s.terminalActivity = previousForeground.activityID
			s.state.Run.WaitingApproval = false
			if s.state.Approval.Active != nil && s.state.Approval.Active.Target.TurnID == previousForeground.activityID {
				s.state.Approval.Active = nil
			}
		}
	}
	ready := make(chan error, 1)
	a.sessions[sessionID] = s
	go s.relay(replay, ready)
	select {
	case err := <-ready:
		if err != nil {
			delete(a.sessions, sessionID)
			s.cancel()
			_ = s.sub.Close()
		}
		return s, err
	case <-ctx.Done():
		s.cancel()
		_ = s.sub.Close()
		delete(a.sessions, sessionID)
		return nil, ctx.Err()
	}
}

func (s *draftSession) relay(replay bool, ready chan<- error) {
	defer close(s.done)
	defer s.cancel()
	defer s.sub.Close()
	defer func() {
		s.mu.Lock()
		if s.approvalCancel != nil {
			s.approvalCancel()
		}
		s.mu.Unlock()
	}()
	mux := s.agent.inner.startACPTaskStreamMuxProfile(s.ctx, s.id, true)
	s.mu.Lock()
	s.mux = mux
	s.mu.Unlock()
	defer func() {
		if mux != nil {
			mux.Close()
			s.agent.inner.unregisterACPTaskStreamMux(s.id, mux)
		}
	}()
	var tasks <-chan acpTaskStreamDelivery
	if mux != nil {
		tasks = mux.draftDeliveries
	}
	assembler := &appserver.FeedDeliveryAssembler{}
	synced := false
	fail := func(err error) {
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
		if !synced {
			ready <- err
			synced = true
		}
	}
	for {
		select {
		case <-s.ctx.Done():
			fail(s.ctx.Err())
			return
		case frame, open := <-tasks:
			if !open {
				tasks = nil
				continue
			}
			if err := s.emitEnvelope(frame.envelope, frame.replacementID != "", true, frame.replacementID); err != nil {
				fail(err)
				return
			}
		case delivery, open := <-s.sub.Deliveries():
			if !open {
				err := s.sub.Err()
				if err == nil && !synced {
					err = errors.New("ACP draft history ended before sync")
				}
				if err != nil {
					fail(err)
				}
				return
			}
			events, replacement, err := assembler.Accept(delivery)
			if err != nil {
				fail(err)
				return
			}
			for _, env := range events {
				if !synced && !replay {
					continue
				}
				if err := s.emitEnvelope(env, replacement || !synced, false, delivery.SnapshotID); err != nil {
					fail(err)
					return
				}
				mux.Observe(env)
			}
			if delivery.Kind == appserver.FeedDeliverySync && !synced {
				if err := s.emitCurrentRuntimeState(); err != nil {
					fail(err)
					return
				}
				synced = true
				ready <- nil
				s.wakeApproval()
			}
		}
	}
}

func (s *draftSession) emitEnvelope(env eventstream.Envelope, replay, task bool, replacementIDs ...string) error {
	s.projectionMu.Lock()
	defer s.projectionMu.Unlock()
	if env.SessionID != "" && env.SessionID != s.id {
		return errors.New("ACP draft feed crossed Session identity")
	}
	replacementID := ""
	if len(replacementIDs) > 0 {
		replacementID = replacementIDs[0]
	}
	if replay && replacementID == "" {
		replacementID = "history"
	}
	if replay && task && env.ParentTool != nil {
		key := env.ParentTool.ToolCallID
		if s.childReplacements == nil {
			s.childReplacements = make(map[string]string)
		}
		if s.childReplacements[key] != replacementID {
			s.childReplacements[key] = replacementID
			if s.childTerminal != nil {
				s.childTerminal.resetParent(s.id, key)
			}
		}
	}
	if s.childTerminal != nil {
		if isACPChildTerminalEnvelope(env) && env.Update != nil {
			s.childTerminal.track(env, s.id)
			if notification, handled := s.childTerminal.project(env, s.id); handled {
				env.Update = notification.Update
			}
		} else if task && env.Kind == eventstream.KindLifecycle {
			if notification, handled := s.childTerminal.projectLifecycle(env, s.id); handled {
				env.Update = notification.Update
			}
		} else if env.Kind == eventstream.KindNotice {
			if notification, handled := s.childTerminal.projectNotice(env, s.id); handled {
				env.Update = notification.Update
			} else {
				return s.agent.draftNotice(s.ctx, s.id, env.Notice)
			}
		}
	}
	if env.Update != nil {
		if env.Update.SessionUpdateType() == eventstream.UpdateCurrentMode {
			return s.agent.draftRefreshPresentation(s.ctx, s.id, false)
		}
		update, err := draftEnvelopeUpdate(env, replay)
		if err != nil {
			return err
		}
		if update != nil {
			if err := s.agent.callbacks.SessionUpdate(s.ctx, v2.UpdateSessionNotification{SessionId: v2.SessionId(s.id), Update: *update}); err != nil {
				return err
			}
		}
		terminals, err := draftTerminalUpdates(env)
		if err != nil {
			return err
		}
		if replay {
			if s.terminalResets == nil {
				s.terminalResets = make(map[string]string)
			}
			for _, update := range terminals {
				var id v2.TerminalId
				if update.TerminalUpdate != nil {
					id = update.TerminalUpdate.TerminalId
				}
				if update.TerminalOutputChunk != nil {
					id = update.TerminalOutputChunk.TerminalId
				}
				if id != "" && s.terminalResets[string(id)] != replacementID {
					s.terminalResets[string(id)] = replacementID
					var clear v2.SessionUpdate
					if err := draftJSONConvert(map[string]any{"sessionUpdate": "terminal_update", "terminalId": id, "output": map[string]any{"data": ""}}, &clear); err != nil {
						return err
					}
					if err := s.agent.callbacks.SessionUpdate(s.ctx, v2.UpdateSessionNotification{SessionId: v2.SessionId(s.id), Update: clear}); err != nil {
						return err
					}
				}
			}
		}
		for _, update := range terminals {
			if err := s.agent.callbacks.SessionUpdate(s.ctx, v2.UpdateSessionNotification{SessionId: v2.SessionId(s.id), Update: update}); err != nil {
				return err
			}
		}
		if !replay && !task && draftCanonicalUser(env) {
			s.mu.Lock()
			if s.waiter != nil {
				// Only the current admission's candidates are retained. Transcript
				// replay is owned by Control, not this receipt fence.
				s.users[env.EventID] = eventstream.Envelope{EventID: env.EventID, SessionID: env.SessionID, TurnID: env.TurnID, InputOperationID: env.InputOperationID, InputStatus: env.InputStatus, Final: env.Final, Scope: env.Scope, Delivery: env.Delivery}
				s.settleReceiptLocked(env)
			}
			s.mu.Unlock()
		}
	}
	if !replay && env.Kind == eventstream.KindRequestPermission {
		s.mu.Lock()
		s.approvalObserved = true
		s.stateVersion++
		s.mu.Unlock()
		s.wakeApproval()
		return nil
	}
	if !replay && env.Kind == eventstream.KindLifecycle && env.ApprovalRequestID != "" {
		s.mu.Lock()
		s.stateVersion++
		if s.approvalID == env.ApprovalRequestID {
			if s.approvalCancel != nil {
				s.approvalCancel()
			}
			s.approvalID, s.approvalCancel = "", nil
		}
		s.mu.Unlock()
		s.refreshApprovalState()
		return nil
	}
	if replay && !task && env.Kind == eventstream.KindLifecycle && env.Lifecycle != nil && (env.Scope == "" || env.Scope == eventstream.ScopeMain) && eventstream.IsTurnTerminalLifecycle(env) {
		s.mu.Lock()
		if env.TurnID != "" && env.TurnID == s.state.Run.TurnID {
			s.state.Run.Active = false
			s.state.Run.WaitingApproval = false
			if s.state.Approval.Active != nil && s.state.Approval.Active.Target.TurnID == env.TurnID {
				s.state.Approval.Active = nil
			}
			s.terminalActivity = env.TurnID
			s.lastForeground = &draftForegroundState{activityID: env.TurnID, stopReason: draftLifecycleStopReason(env)}
			s.stateVersion++
		}
		s.mu.Unlock()
	}
	if task || replay {
		return nil
	}
	if env.Kind == eventstream.KindLifecycle && env.Lifecycle != nil && (env.Scope == "" || env.Scope == eventstream.ScopeMain) {
		s.mu.Lock()
		state := s.state
		cancelForegroundApproval := state.Approval.Active == nil || state.Approval.Active.Target.TurnID == env.TurnID
		refreshAfterTerminal := state.Approval.Active != nil || s.approvalID != "" || s.approvalObserved
		if env.TurnID == s.terminalActivity && !eventstream.IsTurnTerminalLifecycle(env) {
			s.mu.Unlock()
			return nil
		}
		s.stateVersion++
		state.Run.TurnID = env.TurnID
		state.Run.HandleID, state.Run.RunID = env.HandleID, env.RunID
		state.Run.Kind = appserver.RunKindKernel
		state.Run.Active = !eventstream.IsTurnTerminalLifecycle(env)
		state.Run.WaitingApproval = false
		if !state.Run.Active {
			s.terminalActivity = env.TurnID
			if cancelForegroundApproval {
				state.Approval.Active = nil
			}
			s.failUninsertedReceiptLocked(env.TurnID)
		}
		s.state = state
		s.mu.Unlock()
		stopReason := draftLifecycleStopReason(env)
		if err := s.emitRuntimeStateLocked(state, stopReason); err != nil {
			return err
		}
		if !state.Run.Active {
			if cancelForegroundApproval {
				s.cancelApproval()
			}
			if refreshAfterTerminal {
				s.refreshApprovalState()
			}
		} else if refreshAfterTerminal {
			// A permission wake can race a new foreground lifecycle. Refresh the
			// original FIFO after its identity has reached this projection owner.
			s.refreshApprovalState()
		}
	}
	return nil
}

func draftLifecycleStopReason(env eventstream.Envelope) string {
	if !eventstream.IsTurnTerminalLifecycle(env) {
		return ""
	}
	if env.Lifecycle.StopReason != "" {
		return env.Lifecycle.StopReason
	}
	switch env.Lifecycle.State {
	case eventstream.LifecycleStateCancelled, "canceled":
		return "cancelled"
	case eventstream.LifecycleStateFailed, eventstream.LifecycleStateUnknown:
		return "error"
	default:
		return "end_turn"
	}
}

func draftCanonicalUser(env eventstream.Envelope) bool {
	return env.Update != nil && env.Update.SessionUpdateType() == eventstream.UpdateUserMessage && env.EventID != "" && env.Final && env.Delivery != nil && (env.Delivery.Mode == eventstream.DeliveryCanonical || env.Delivery.Mode == eventstream.DeliveryMirror) && (env.Scope == "" || env.Scope == eventstream.ScopeMain) && env.AgentCommunicationSource == nil
}

func (s *draftSession) settleReceiptLocked(env eventstream.Envelope) {
	w := s.waiter
	if w != nil && w.interactionID != "" && env.EventID == w.interactionID {
		select {
		case w.message <- env.EventID:
		default:
		}
		clear(s.users)
		return
	}
	if w == nil || w.target.TurnID == "" || env.TurnID != w.target.TurnID || env.SessionID != s.id {
		return
	}
	if env.Delivery == nil || env.Delivery.Mode != eventstream.DeliveryCanonical {
		return
	}
	if w.steer && (env.InputOperationID != w.operationID || env.InputStatus != "applied") {
		return
	}
	if !w.steer && env.InputOperationID != "" {
		return
	}
	select {
	case w.message <- env.EventID:
	default:
	}
	clear(s.users)
}

func (s *draftSession) failUninsertedReceiptLocked(turnID string) {
	w := s.waiter
	if w == nil || w.interactionID != "" || w.target.TurnID == "" || w.target.TurnID != turnID {
		return
	}
	select {
	case w.failed <- errors.New("ACP draft Turn settled before this user input was inserted"):
	default:
	}
}

func (s *draftSession) emitRuntimeStateLocked(state appserver.SessionState, stopReason string) error {
	action := state.Run.Active && (state.Run.WaitingApproval || (state.Approval.Active != nil && state.Approval.Active.Target.TurnID == state.Run.TurnID))
	return s.emitForegroundStateLocked(draftForegroundState{activityID: strings.TrimSpace(state.Run.TurnID), active: state.Run.Active, action: action, stopReason: stopReason})
}

func (s *draftSession) emitForegroundStateLocked(state draftForegroundState) error {
	update, err := draftStateUpdate(state.active, state.action, state.activityID, state.stopReason)
	if err != nil {
		return err
	}
	if err := s.agent.callbacks.SessionUpdate(s.ctx, v2.UpdateSessionNotification{SessionId: v2.SessionId(s.id), Update: update}); err != nil {
		return err
	}
	s.lastForeground = &state
	return nil
}

func (s *draftSession) emitCommandState(messageID string, active bool, stopReason string) error {
	s.projectionMu.Lock()
	defer s.projectionMu.Unlock()
	return s.emitForegroundStateLocked(draftForegroundState{activityID: messageID, active: active, stopReason: stopReason})
}

func (s *draftSession) cancelApproval() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.approvalCancel != nil {
		s.approvalCancel()
		s.approvalCancel = nil
	}
	s.approvalID = ""
}

func (s *draftSession) refreshApprovalState() {
	go func() {
		version := s.stateInspectionVersion()
		state, err := s.agent.inner.sessionClient.InspectSession(s.ctx, appserver.StateRequest{SessionID: s.id})
		if err != nil {
			return
		}
		s.projectionMu.Lock()
		state, accepted := s.applyApprovalSnapshotLocked(state, version)
		if accepted && state.Run.Active {
			_ = s.emitRuntimeStateLocked(state, "")
		}
		s.projectionMu.Unlock()
		if accepted && state.Approval.Active != nil {
			s.wakeApproval()
		}
	}()
}

func (s *draftSession) wakeApproval() {
	// The native FIFO is the owner of request identity and scope. A feed
	// notification is only a wake-up; historical permission records are never
	// re-executed and a sibling observer's already-resolved request is ignored.
	go func() {
		version := s.stateInspectionVersion()
		state, err := s.agent.inner.sessionClient.InspectSession(s.ctx, appserver.StateRequest{SessionID: s.id})
		if err != nil || state.Approval.Active == nil {
			return
		}
		s.projectionMu.Lock()
		state, accepted := s.applyApprovalSnapshotLocked(state, version)
		if !accepted || state.Approval.Active == nil {
			s.projectionMu.Unlock()
			return
		}
		active := state.Approval.Active
		s.mu.Lock()
		if s.approvalID == active.RequestID {
			s.mu.Unlock()
			s.projectionMu.Unlock()
			return
		}
		if s.approvalCancel != nil {
			s.approvalCancel()
		}
		ctx, cancel := context.WithCancel(s.ctx)
		s.approvalID, s.approvalCancel = active.RequestID, cancel
		s.mu.Unlock()
		defer cancel()
		if state.Run.Active {
			err = s.emitRuntimeStateLocked(state, "")
		}
		s.projectionMu.Unlock()
		if err != nil {
			return
		}
		// Control separates bounded display facts from the original request. Join
		// only a detached display copy; resolution still uses the native FIFO head.
		permission := active.Permission
		if permission != nil {
			copy := session.CloneProtocolApproval(*permission)
			copy.Subject = session.CloneApprovalSubject(active.Subject)
			permission = &copy
		}
		request, err := draftPermissionRequest(s.id, permission)
		if err != nil {
			_ = s.agent.draftNotice(s.ctx, s.id, "The native approval could not be displayed. It remains pending; reconnect to retry.")
			return
		}
		response, err := s.agent.callbacks.RequestPermission(ctx, request)
		if err != nil {
			if ctx.Err() == nil {
				_ = s.agent.draftNotice(s.ctx, s.id, "The permission response was not received. The native approval remains pending; reconnect to retry.")
			}
			return
		}
		if err := response.Validate(); err != nil {
			_ = s.agent.draftNotice(s.ctx, s.id, "The permission response was invalid. The native approval remains pending; reconnect to retry.")
			return
		}
		outcome, optionID, approved := "cancelled", "", false
		if response.Outcome.Selected != nil {
			outcome, optionID = "selected", string(response.Outcome.Selected.OptionId)
			for _, option := range active.Permission.Options {
				if option.ID == optionID && strings.HasPrefix(option.Kind, "allow") {
					approved = true
					break
				}
			}
		}
		result, err := s.agent.inner.sessionClient.ResolveApproval(ctx, appserver.ResolveApprovalRequest{WriteBase: appserver.WriteBase{OperationID: newACPSessionOperationID("draft-approval"), SessionID: s.id}, Target: active.Target, ApprovalRequestID: string(active.RequestID), Outcome: outcome, OptionID: optionID, Approved: approved})
		if appserver.CommandMutationError(result, err) != nil {
			if ctx.Err() == nil {
				_ = s.agent.draftNotice(s.ctx, s.id, "The permission decision could not be confirmed. Inspect the native approval before retrying.")
			}
			return
		}
		s.mu.Lock()
		if s.approvalID == active.RequestID {
			s.approvalID = ""
			s.approvalCancel = nil
		}
		s.mu.Unlock()
		s.refreshApprovalState()
	}()
}
