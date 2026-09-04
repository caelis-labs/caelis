package acpagentbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/acppermission"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/internal/jsonvalue"
)

func (a *RuntimeAgent) runPromptRouter(runCtx context.Context, bridgeCtx context.Context, activeSession session.Session, input string, contentParts []model.ContentPart, cb PromptCallbacks) (bool, error) {
	if a == nil || a.promptRouterFactory == nil {
		return false, nil
	}
	router, err := a.promptRouterFactory(bridgeCtx, activeSession)
	if err != nil {
		return false, err
	}
	if router == nil {
		return false, nil
	}
	result, err := router.Route(runCtx, controlprompt.Request{Submission: controlprompt.Submission{
		Text:        strings.TrimSpace(input),
		Attachments: promptRouterAttachmentsFromContentParts(input, contentParts),
	}})
	if err != nil || !result.Handled {
		return result.Handled, err
	}
	return true, a.emitPromptRouterResult(runCtx, activeSession, result, cb, true)
}

func promptRouterAttachmentsFromContentParts(input string, parts []model.ContentPart) []controlprompt.Attachment {
	if len(parts) == 0 {
		return nil
	}
	inputLen := len([]rune(strings.TrimSpace(input)))
	offset := 0
	textParts := 0
	out := make([]controlprompt.Attachment, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case model.ContentPartText:
			text := strings.TrimSpace(part.Text)
			if text == "" {
				continue
			}
			if textParts > 0 {
				offset++
			}
			offset += len([]rune(text))
			if offset > inputLen {
				offset = inputLen
			}
			textParts++
		case model.ContentPartImage:
			data := strings.TrimSpace(part.Data)
			if data == "" {
				continue
			}
			attachmentOffset := offset
			if attachmentOffset > inputLen {
				attachmentOffset = inputLen
			}
			out = append(out, controlprompt.Attachment{
				Name:     strings.TrimSpace(part.FileName),
				Offset:   attachmentOffset,
				MimeType: strings.TrimSpace(part.MimeType),
				Data:     data,
			})
		}
	}
	return out
}

func (a *RuntimeAgent) emitPromptRouterResult(ctx context.Context, activeSession session.Session, result controlprompt.Result, cb PromptCallbacks, suppressUserEcho bool) error {
	if cb == nil {
		return nil
	}
	sessionID := promptRouterResultSessionID(activeSession, result)
	outboundFilter := newACPNarrativeFilter(suppressUserEcho)
	taskMux := a.startACPTaskStreamMux(ctx, sessionID)
	taskEvents := taskMux.Events()
	defer a.detachACPTaskStreamMux(ctx, taskMux, cb, sessionID, outboundFilter)
	for _, env := range result.Events {
		if err := a.emitTaskAwareControlEnvelope(ctx, cb, sessionID, nil, taskMux, &taskEvents, env, outboundFilter); err != nil {
			return err
		}
	}
	if result.SlashResult != nil {
		text := strings.TrimSpace(a.slashResultFormatter(*result.SlashResult))
		if text != "" {
			if err := a.emitControlEnvelope(ctx, cb, sessionID, nil, eventstream.Envelope{
				Kind:   eventstream.KindNotice,
				Notice: text,
			}, outboundFilter); err != nil {
				return err
			}
		}
	}
	var reconnectDeliveries <-chan appserver.FeedDelivery
	reconnectAssembler := &appserver.FeedDeliveryAssembler{}
	reconnectIrreversible := false
	if result.Reconnect != nil {
		defer result.Reconnect.Close()
		reconnectDeliveries = result.Reconnect.Deliveries()
		initialDone := false
		for reconnectDeliveries != nil && !initialDone {
			select {
			case <-ctx.Done():
				return context.Canceled
			case delivery, ok := <-reconnectDeliveries:
				if !ok {
					reconnectDeliveries = nil
					continue
				}
				events, replacement, err := reconnectAssembler.Accept(delivery)
				if err != nil {
					return err
				}
				if replacement && reconnectIrreversible {
					return fmt.Errorf("internal/acpagentbridge: Session replacement crossed emitted ACP output")
				}
				for _, env := range events {
					if err := a.emitControlBackfillEnvelope(ctx, cb, sessionID, env, outboundFilter); err != nil {
						return err
					}
					reconnectIrreversible = true
				}
				if delivery.Kind == appserver.FeedDeliverySync {
					initialDone = true
					reconnectIrreversible = true
				}
			}
		}
		if err := result.Reconnect.Err(); err != nil {
			return err
		}
		for _, env := range result.Reconnect.BootstrapEvents() {
			if err := a.emitTaskAwareControlEnvelope(ctx, cb, sessionID, result.Reconnect, taskMux, &taskEvents, env, outboundFilter); err != nil {
				return err
			}
		}
	}
	if err := a.emitPromptRouterSideEffects(ctx, cb, activeSession, result); err != nil {
		return err
	}
	if result.Reconnect != nil {
		state := result.Reconnect.State()
		if !state.Run.Active && state.Approval.Active == nil {
			return nil
		}
		for reconnectDeliveries != nil {
			select {
			case <-ctx.Done():
				return context.Canceled
			case taskEnvelope, ok := <-taskEvents:
				if !ok {
					taskEvents = nil
					continue
				}
				if err := a.emitControlEnvelope(ctx, cb, sessionID, nil, taskEnvelope, outboundFilter); err != nil {
					return err
				}
			case delivery, ok := <-reconnectDeliveries:
				if !ok {
					reconnectDeliveries = nil
					continue
				}
				events, replacement, err := reconnectAssembler.Accept(delivery)
				if err != nil {
					return err
				}
				if replacement && reconnectIrreversible {
					return fmt.Errorf("internal/acpagentbridge: Session replacement crossed emitted ACP output")
				}
				for _, env := range events {
					if err := a.emitTaskAwareControlEnvelope(ctx, cb, sessionID, result.Reconnect, taskMux, &taskEvents, env, outboundFilter); err != nil {
						return err
					}
					reconnectIrreversible = true
				}
			}
		}
		return result.Reconnect.Err()
	}
	if result.Turn == nil {
		return nil
	}
	for events := result.Turn.Events(); events != nil; {
		select {
		case <-ctx.Done():
			result.Turn.Cancel()
			_ = result.Turn.Close()
			return context.Canceled
		case taskEnvelope, ok := <-taskEvents:
			if !ok {
				taskEvents = nil
				continue
			}
			if err := a.emitControlEnvelope(ctx, cb, sessionID, nil, taskEnvelope, outboundFilter); err != nil {
				_ = result.Turn.Close()
				return err
			}
		case env, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if err := a.emitTaskAwareControlEnvelope(ctx, cb, sessionID, result.Turn, taskMux, &taskEvents, env, outboundFilter); err != nil {
				_ = result.Turn.Close()
				return err
			}
		}
	}
	closeErr := result.Turn.Close()
	if closeErr != nil {
		return closeErr
	}
	return nil
}

// emitTaskAwareControlEnvelope is the single parent-envelope delivery order for
// both Control turns and the direct Runtime runner. It emits the canonical
// envelope, then discovers and drains any Task stream mounted by that envelope.
func (a *RuntimeAgent) emitTaskAwareControlEnvelope(
	ctx context.Context,
	cb PromptCallbacks,
	sessionID string,
	turn controlApprovalSubmitter,
	taskMux *acpTaskStreamMux,
	taskEvents *<-chan eventstream.Envelope,
	env eventstream.Envelope,
	outboundFilter *acpNarrativeFilter,
) error {
	if err := a.emitControlEnvelope(ctx, cb, sessionID, turn, env, outboundFilter); err != nil {
		return err
	}
	taskMux.Observe(env)
	return a.drainReadyACPTaskStream(ctx, cb, sessionID, taskEvents, outboundFilter)
}

func (a *RuntimeAgent) drainReadyACPTaskStream(
	ctx context.Context,
	cb PromptCallbacks,
	sessionID string,
	taskEvents *<-chan eventstream.Envelope,
	outboundFilter *acpNarrativeFilter,
) error {
	for taskEvents != nil && *taskEvents != nil {
		select {
		case taskEnvelope, open := <-*taskEvents:
			if !open {
				*taskEvents = nil
				return nil
			}
			if err := a.emitControlEnvelope(ctx, cb, sessionID, nil, taskEnvelope, outboundFilter); err != nil {
				return err
			}
		default:
			return nil
		}
	}
	return nil
}

// emitControlBackfillEnvelope preserves transcript-bearing ACP updates without
// re-running historical interaction semantics. In particular, only the typed
// active approval bootstrap may call RequestPermission after reconnect.
func (a *RuntimeAgent) emitControlBackfillEnvelope(
	ctx context.Context,
	cb PromptCallbacks,
	sessionID string,
	env eventstream.Envelope,
	outboundFilter *acpNarrativeFilter,
) error {
	switch env.Kind {
	case eventstream.KindRequestPermission:
		return nil
	case eventstream.KindError:
		text := strings.TrimSpace(env.Error)
		if env.Err != nil {
			text = strings.TrimSpace(env.Err.Error())
		}
		if text == "" {
			return nil
		}
		env = eventstream.Envelope{Kind: eventstream.KindNotice, SessionID: sessionID, Notice: text}
	}
	return a.emitControlEnvelope(ctx, cb, sessionID, nil, env, outboundFilter)
}

func promptRouterResultSessionID(activeSession session.Session, result controlprompt.Result) string {
	if sessionID := strings.TrimSpace(result.ActiveSessionID); sessionID != "" {
		return sessionID
	}
	if result.StatusUpdate != nil {
		if sessionID := strings.TrimSpace(result.StatusUpdate.Session.ID); sessionID != "" {
			return sessionID
		}
	}
	return strings.TrimSpace(activeSession.SessionID)
}

func (a *RuntimeAgent) emitPromptRouterSideEffects(ctx context.Context, cb PromptCallbacks, activeSession session.Session, result controlprompt.Result) error {
	sessionID := promptRouterResultSessionID(activeSession, result)
	if result.StatusUpdate != nil || result.ClearHistory || result.RefreshStatus {
		if err := a.emitPromptRouterSessionState(ctx, cb, activeSession, sessionID, result.ClearHistory); err != nil {
			return err
		}
	}
	if result.RefreshCommands {
		return a.emitAvailableCommandsUpdate(ctx, cb, sessionID)
	}
	return nil
}

func (a *RuntimeAgent) emitPromptRouterSessionState(ctx context.Context, cb PromptCallbacks, activeSession session.Session, sessionID string, includeSessionInfo bool) error {
	targetSession, err := a.promptRouterTargetSession(ctx, activeSession, sessionID)
	if err != nil {
		return err
	}
	if includeSessionInfo {
		update, err := standardSessionUpdate(eventstream.UpdateSessionInfo, acpsdk.SessionSessionInfoUpdate{
			SessionUpdate: eventstream.UpdateSessionInfo,
		})
		if err != nil {
			return err
		}
		if err := cb.SessionUpdate(ctx, eventstream.SessionNotification{
			SessionID: sessionID,
			Update:    update,
		}); err != nil {
			return err
		}
	}
	if a.modes != nil {
		modes, err := a.modes.SessionModes(ctx, targetSession)
		if err != nil {
			return err
		}
		if modes != nil && strings.TrimSpace(string(modes.CurrentModeId)) != "" {
			update, err := standardSessionUpdate(eventstream.UpdateCurrentMode, acpsdk.SessionCurrentModeUpdate{
				SessionUpdate: eventstream.UpdateCurrentMode,
				CurrentModeId: acpsdk.SessionModeId(strings.TrimSpace(string(modes.CurrentModeId))),
			})
			if err != nil {
				return err
			}
			if err := cb.SessionUpdate(ctx, eventstream.SessionNotification{
				SessionID: sessionID,
				Update:    update,
			}); err != nil {
				return err
			}
		}
	}
	if a.config != nil {
		options, err := a.config.SessionConfigOptions(ctx, targetSession)
		if err != nil {
			return err
		}
		update, err := standardSessionUpdate(eventstream.UpdateConfigOption, acpsdk.SessionConfigOptionUpdate{
			SessionUpdate: eventstream.UpdateConfigOption,
			ConfigOptions: options,
		})
		if err != nil {
			return err
		}
		if err := cb.SessionUpdate(ctx, eventstream.SessionNotification{
			SessionID: sessionID,
			Update:    update,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (a *RuntimeAgent) promptRouterTargetSession(ctx context.Context, activeSession session.Session, sessionID string) (session.Session, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || strings.EqualFold(sessionID, strings.TrimSpace(activeSession.SessionID)) {
		return activeSession, nil
	}
	return a.session(ctx, sessionID)
}

func (a *RuntimeAgent) emitAvailableCommandsUpdate(ctx context.Context, cb PromptCallbacks, sessionID string) error {
	if a.commands == nil {
		return nil
	}
	commands, err := a.commands.AvailableCommands(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return err
	}
	update, err := standardSessionUpdate(eventstream.UpdateAvailableCmds, acpsdk.SessionAvailableCommandsUpdate{
		SessionUpdate:     eventstream.UpdateAvailableCmds,
		AvailableCommands: commands,
	})
	if err != nil {
		return err
	}
	return cb.SessionUpdate(ctx, eventstream.SessionNotification{
		SessionID: strings.TrimSpace(sessionID),
		Update:    update,
	})
}

// standardSessionUpdate keeps standard ACP session-state wire members owned by
// acp-go-sdk while the bridge still routes other projection updates through the
// Control eventstream.Update callback.
func standardSessionUpdate(updateType string, update any) (eventstream.RawUpdate, error) {
	raw, err := json.Marshal(update)
	if err != nil {
		return eventstream.RawUpdate{}, fmt.Errorf("internal/acpagentbridge: encode %s: %w", updateType, err)
	}
	return eventstream.RawUpdate{SessionUpdate: strings.TrimSpace(updateType), Raw: raw}, nil
}

type controlApprovalSubmitter interface {
	SubmitApproval(context.Context, controlprompt.ApprovalDecision) error
}

func (a *RuntimeAgent) emitControlEnvelope(ctx context.Context, cb PromptCallbacks, fallbackSessionID string, turn controlApprovalSubmitter, env eventstream.Envelope, outboundFilter *acpNarrativeFilter) error {
	if cb == nil {
		return nil
	}
	sessionID := acpEnvelopeSessionID(env, fallbackSessionID)
	switch env.Kind {
	case eventstream.KindRequestPermission:
		if env.Permission == nil {
			return nil
		}
		approval, err := acppermission.DecodePermissionRequest(*env.Permission)
		if err != nil {
			return err
		}
		request, err := sdkPermissionRequestFromSchema(*env.Permission)
		if err != nil {
			return err
		}
		resp, err := cb.RequestPermission(ctx, request)
		if err != nil || turn == nil {
			return err
		}
		return turn.SubmitApproval(ctx, approvalDecisionFromACPResponse(env.ApprovalRequestID, approval, resp))
	case eventstream.KindSessionUpdate:
		if env.Update == nil {
			return nil
		}
		if outboundFilter != nil && outboundFilter.childTerminal != nil && isACPChildTerminalEnvelope(env) {
			outboundFilter.childTerminal.track(env, sessionID)
			filtered, ok := outboundFilter.FilterNotification(
				eventstream.SessionNotification{SessionID: sessionID, Update: env.Update},
			)
			if !ok {
				return nil
			}
			env.Update = filtered.Update
			if notification, handled := outboundFilter.childTerminal.project(env, sessionID); handled {
				if notification.Update == nil {
					return nil
				}
				// A final child narrative chunk is not terminal evidence. The
				// typed child Task lifecycle or a canonical parent result closes it.
				return cb.SessionUpdate(ctx, normalizeACPStdioTerminalExtension(notification))
			}
		}
		notification := eventstream.SessionNotification{SessionID: sessionID, Update: env.Update}
		if err := emitFilteredSessionUpdate(ctx, cb, notification, outboundFilter); err != nil {
			return err
		}
		return nil
	case eventstream.KindNotice:
		if outboundFilter != nil && outboundFilter.childTerminal != nil {
			notification, handled := outboundFilter.childTerminal.projectNotice(env, sessionID)
			if handled {
				if notification.Update == nil {
					return nil
				}
				return cb.SessionUpdate(ctx, normalizeACPStdioTerminalExtension(notification))
			}
		}
		return emitACPNotice(ctx, cb, sessionID, env, "", outboundFilter)
	case eventstream.KindLifecycle:
		if outboundFilter == nil || outboundFilter.childTerminal == nil {
			return nil
		}
		notification, handled := outboundFilter.childTerminal.projectLifecycle(env, sessionID)
		if !handled || notification.Update == nil {
			return nil
		}
		return emitFilteredSessionUpdate(ctx, cb, notification, outboundFilter)
	case eventstream.KindError:
		if env.Err != nil {
			return env.Err
		}
		if strings.TrimSpace(env.Error) != "" {
			return errors.New(strings.TrimSpace(env.Error))
		}
	}
	if env.Update != nil {
		return emitFilteredSessionUpdate(ctx, cb, eventstream.SessionNotification{SessionID: sessionID, Update: env.Update}, outboundFilter)
	}
	return nil
}

func emitACPNotice(
	ctx context.Context,
	cb PromptCallbacks,
	sessionID string,
	env eventstream.Envelope,
	fallbackMessageID string,
	outboundFilter *acpNarrativeFilter,
) error {
	if cb == nil {
		return nil
	}
	text := strings.TrimSpace(env.Notice)
	if text == "" {
		return nil
	}
	return emitFilteredSessionUpdate(ctx, cb, eventstream.SessionNotification{
		SessionID: sessionID,
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentMessage,
			Content:       eventstream.TextContent{Type: "text", Text: text},
			MessageID: firstNonEmptyNoticeMessageID(
				strings.TrimSpace(env.ProjectionID),
				strings.TrimSpace(env.EventID),
				strings.TrimSpace(env.Cursor),
				strings.TrimSpace(fallbackMessageID),
			),
			Meta: jsonvalue.CloneMap(env.Meta),
		},
	}, outboundFilter)
}

func firstNonEmptyNoticeMessageID(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func emitFilteredSessionUpdate(ctx context.Context, cb PromptCallbacks, notification eventstream.SessionNotification, outboundFilter *acpNarrativeFilter) error {
	if outboundFilter != nil {
		filtered, ok := outboundFilter.FilterNotification(notification)
		if !ok {
			return nil
		}
		notification = filtered
	}
	return cb.SessionUpdate(ctx, notification)
}

func approvalDecisionFromACPResponse(requestID eventstream.ApprovalRequestID, approval *session.ProtocolApproval, resp acpsdk.RequestPermissionResponse) controlprompt.ApprovalDecision {
	decision := approvalResponseFromSDK(resp, approval)
	return controlprompt.ApprovalDecision{
		RequestID: requestID,
		Outcome:   decision.Outcome, OptionID: decision.OptionID, Approved: decision.Approved,
	}
}
