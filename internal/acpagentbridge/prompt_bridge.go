package acpagentbridge

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlprompt "github.com/caelis-labs/caelis/ports/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp"
	"github.com/caelis-labs/caelis/protocol/acp/control"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/semantic"
)

func (a *RuntimeAgent) runPromptRouter(runCtx context.Context, bridgeCtx context.Context, activeSession session.Session, input string, contentParts []model.ContentPart, cb acp.PromptCallbacks) (bool, error) {
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
	result, err := router.Route(runCtx, controlprompt.Request{Submission: control.Submission{
		Text:        strings.TrimSpace(input),
		Attachments: promptRouterAttachmentsFromContentParts(input, contentParts),
	}})
	if err != nil || !result.Handled {
		return result.Handled, err
	}
	return true, a.emitPromptRouterResult(runCtx, activeSession, result, cb, true)
}

func promptRouterAttachmentsFromContentParts(input string, parts []model.ContentPart) []control.Attachment {
	if len(parts) == 0 {
		return nil
	}
	inputLen := len([]rune(strings.TrimSpace(input)))
	offset := 0
	textParts := 0
	out := make([]control.Attachment, 0, len(parts))
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
			out = append(out, control.Attachment{
				Name:     strings.TrimSpace(part.FileName),
				Offset:   attachmentOffset,
				MimeType: strings.TrimSpace(part.MimeType),
				Data:     data,
			})
		}
	}
	return out
}

func (a *RuntimeAgent) emitPromptRouterResult(ctx context.Context, activeSession session.Session, result controlprompt.Result, cb acp.PromptCallbacks, suppressUserEcho bool) error {
	if cb == nil {
		return nil
	}
	sessionID := promptRouterResultSessionID(activeSession, result)
	outboundFilter := newACPNarrativeFilter(suppressUserEcho)
	if result.SlashResult != nil {
		text := strings.TrimSpace(control.FormatSlashResult(*result.SlashResult))
		if text != "" {
			if err := a.emitControlEnvelope(ctx, cb, sessionID, nil, eventstream.Envelope{
				Kind:   eventstream.KindNotice,
				Notice: text,
			}, outboundFilter); err != nil {
				return err
			}
		}
	}
	for _, env := range result.Events {
		if err := a.emitControlEnvelope(ctx, cb, sessionID, nil, env, outboundFilter); err != nil {
			return err
		}
	}
	for _, env := range result.ReplayEvents {
		if err := a.emitControlEnvelope(ctx, cb, sessionID, nil, env, outboundFilter); err != nil {
			return err
		}
	}
	if result.Reconnect != nil {
		defer result.Reconnect.Close()
		for backfill := result.Reconnect.Backfill(); backfill != nil; {
			select {
			case <-ctx.Done():
				return context.Canceled
			case env, ok := <-backfill:
				if !ok {
					backfill = nil
					continue
				}
				if err := a.emitControlBackfillEnvelope(ctx, cb, sessionID, env, outboundFilter); err != nil {
					return err
				}
			}
		}
		if err := result.Reconnect.Err(); err != nil {
			return err
		}
		for _, env := range result.Reconnect.BootstrapEvents() {
			if err := a.emitControlEnvelope(ctx, cb, sessionID, result.Reconnect, env, outboundFilter); err != nil {
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
		for events := result.Reconnect.Events(); events != nil; {
			select {
			case <-ctx.Done():
				return context.Canceled
			case env, ok := <-events:
				if !ok {
					events = nil
					continue
				}
				if err := a.emitControlEnvelope(ctx, cb, sessionID, result.Reconnect, env, outboundFilter); err != nil {
					return err
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
			_ = result.Turn.Close()
			return context.Canceled
		case env, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if err := a.emitControlEnvelope(ctx, cb, sessionID, result.Turn, env, outboundFilter); err != nil {
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

// emitControlBackfillEnvelope preserves transcript-bearing ACP updates without
// re-running historical interaction semantics. In particular, only the typed
// active approval bootstrap may call RequestPermission after reconnect.
func (a *RuntimeAgent) emitControlBackfillEnvelope(
	ctx context.Context,
	cb acp.PromptCallbacks,
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

func (a *RuntimeAgent) emitPromptRouterSideEffects(ctx context.Context, cb acp.PromptCallbacks, activeSession session.Session, result controlprompt.Result) error {
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

func (a *RuntimeAgent) emitPromptRouterSessionState(ctx context.Context, cb acp.PromptCallbacks, activeSession session.Session, sessionID string, includeSessionInfo bool) error {
	targetSession, err := a.promptRouterTargetSession(ctx, activeSession, sessionID)
	if err != nil {
		return err
	}
	if includeSessionInfo {
		if err := cb.SessionUpdate(ctx, acp.SessionNotification{
			SessionID: sessionID,
			Update:    acp.SessionInfoUpdate{SessionUpdate: acp.UpdateSessionInfo},
		}); err != nil {
			return err
		}
	}
	if a.modes != nil {
		modes, err := a.modes.SessionModes(ctx, targetSession)
		if err != nil {
			return err
		}
		if modes != nil && strings.TrimSpace(modes.CurrentModeID) != "" {
			if err := cb.SessionUpdate(ctx, acp.SessionNotification{
				SessionID: sessionID,
				Update: acp.CurrentModeUpdate{
					SessionUpdate: acp.UpdateCurrentMode,
					CurrentModeID: strings.TrimSpace(modes.CurrentModeID),
				},
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
		if err := cb.SessionUpdate(ctx, acp.SessionNotification{
			SessionID: sessionID,
			Update: acp.ConfigOptionUpdate{
				SessionUpdate: acp.UpdateConfigOption,
				ConfigOptions: options,
			},
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

func (a *RuntimeAgent) emitAvailableCommandsUpdate(ctx context.Context, cb acp.PromptCallbacks, sessionID string) error {
	if a.commands == nil {
		return nil
	}
	commands, err := a.commands.AvailableCommands(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return err
	}
	return cb.SessionUpdate(ctx, acp.SessionNotification{
		SessionID: strings.TrimSpace(sessionID),
		Update: acp.AvailableCommandsUpdate{
			SessionUpdate:     acp.UpdateAvailableCmds,
			AvailableCommands: commands,
		},
	})
}

func (a *RuntimeAgent) emitControlEnvelope(ctx context.Context, cb acp.PromptCallbacks, fallbackSessionID string, turn control.Turn, env eventstream.Envelope, outboundFilter *acpNarrativeFilter) error {
	if cb == nil {
		return nil
	}
	source := acpFilterSourceFromEnvelope(env, fallbackSessionID)
	sessionID := source.SessionID
	switch env.Kind {
	case eventstream.KindRequestPermission:
		if env.Permission == nil {
			return nil
		}
		if outboundFilter != nil {
			outboundFilter.resetSource(source)
		}
		resp, err := cb.RequestPermission(ctx, *env.Permission)
		if err != nil || turn == nil {
			return err
		}
		return turn.SubmitApproval(ctx, approvalDecisionFromACPResponse(env.ApprovalRequestID, env.Permission.Options, resp))
	case eventstream.KindSessionUpdate:
		if env.Update == nil {
			return nil
		}
		if outboundFilter != nil && outboundFilter.childTerminal != nil && isACPChildTerminalEnvelope(env) {
			outboundFilter.childTerminal.track(env, sessionID)
			filtered, ok := outboundFilter.filterNotificationWithFinal(
				acp.SessionNotification{SessionID: sessionID, Update: env.Update}, env.Final, source,
			)
			if !ok {
				return nil
			}
			env.Update = filtered.Update
			if notification, handled := outboundFilter.childTerminal.project(env, sessionID); handled {
				if notification.Update == nil {
					return nil
				}
				// A final child narrative chunk is not the parent Spawn terminal.
				// Only the parent tool result below may close the mounted panel.
				return cb.SessionUpdate(ctx, normalizeACPStdioTerminalExtension(notification))
			}
		}
		notification := acp.SessionNotification{SessionID: sessionID, Update: env.Update}
		if outboundFilter != nil && outboundFilter.childTerminal != nil {
			notification = outboundFilter.childTerminal.normalizeParentClose(notification)
		}
		return emitFilteredSessionUpdate(ctx, cb, notification, env.Final, source, outboundFilter)
	case eventstream.KindNotice:
		text := strings.TrimSpace(env.Notice)
		if text == "" {
			return nil
		}
		return emitFilteredSessionUpdate(ctx, cb, acp.SessionNotification{
			SessionID: sessionID,
			Update: acp.ContentChunk{
				SessionUpdate: acp.UpdateAgentMessage,
				Content:       acp.TextContent{Type: "text", Text: text},
			},
		}, true, source, outboundFilter)
	case eventstream.KindError:
		if env.Err != nil {
			return env.Err
		}
		if strings.TrimSpace(env.Error) != "" {
			return errors.New(strings.TrimSpace(env.Error))
		}
	}
	if env.Update != nil {
		return emitFilteredSessionUpdate(ctx, cb, acp.SessionNotification{SessionID: sessionID, Update: env.Update}, env.Final, source, outboundFilter)
	}
	return nil
}

func emitFilteredSessionUpdate(ctx context.Context, cb acp.PromptCallbacks, notification acp.SessionNotification, final bool, source acpFilterSource, outboundFilter *acpNarrativeFilter) error {
	if outboundFilter != nil {
		filtered, ok := outboundFilter.filterNotificationWithFinal(notification, final, source)
		if !ok {
			return nil
		}
		notification = filtered
	}
	return cb.SessionUpdate(ctx, notification)
}

func approvalDecisionFromACPResponse(requestID eventstream.ApprovalRequestID, options []acp.PermissionOption, resp acp.RequestPermissionResponse) control.ApprovalDecision {
	approval := &session.ProtocolApproval{Options: make([]session.ProtocolApprovalOption, 0, len(options))}
	for _, option := range options {
		approval.Options = append(approval.Options, session.ProtocolApprovalOption{
			ID: strings.TrimSpace(option.OptionID), Name: strings.TrimSpace(option.Name), Kind: strings.TrimSpace(option.Kind),
		})
	}
	decision := semantic.DecodePermissionResponse(resp, approval)
	return control.ApprovalDecision{
		RequestID: requestID,
		Outcome:   decision.Outcome, OptionID: decision.OptionID, Approved: decision.Approved,
	}
}
