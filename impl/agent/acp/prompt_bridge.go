package acp

import (
	"context"
	"errors"
	"strings"
	"sync"

	controlprompt "github.com/caelis-labs/caelis/ports/controlprompt"
	"github.com/caelis-labs/caelis/ports/model"
	"github.com/caelis-labs/caelis/ports/session"
	"github.com/caelis-labs/caelis/protocol/acp"
	"github.com/caelis-labs/caelis/protocol/acp/control"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
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
	return true, a.emitPromptRouterResult(runCtx, activeSession, router, result, cb, true)
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

func (a *RuntimeAgent) emitPromptRouterResult(ctx context.Context, activeSession session.Session, router controlprompt.Router, result controlprompt.Result, cb acp.PromptCallbacks, suppressUserEcho bool) error {
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
	if err := a.emitPromptRouterSideEffects(ctx, cb, activeSession, result); err != nil {
		return err
	}
	if result.Turn == nil {
		return nil
	}
	var streamer control.StreamSubscriber
	if provider, ok := router.(controlprompt.StreamSubscriberProvider); ok {
		streamer, _ = provider.StreamSubscriber()
	}
	bridge := newControlStreamBridge(ctx, a, cb, sessionID, streamer, outboundFilter)
	for events := result.Turn.Events(); events != nil; {
		select {
		case <-ctx.Done():
			bridge.cancel()
			_ = result.Turn.Close()
			_ = bridge.wait()
			return context.Canceled
		case env, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if err := a.emitControlEnvelope(ctx, cb, sessionID, result.Turn, env, outboundFilter); err != nil {
				bridge.cancel()
				_ = result.Turn.Close()
				_ = bridge.wait()
				return err
			}
			bridge.start(env)
		}
	}
	closeErr := result.Turn.Close()
	waitErr := bridge.wait()
	bridge.cancel()
	if closeErr != nil {
		return closeErr
	}
	return waitErr
}

func promptRouterResultSessionID(activeSession session.Session, result controlprompt.Result) string {
	if result.StatusUpdate != nil {
		if sessionID := strings.TrimSpace(result.StatusUpdate.Session.ID); sessionID != "" {
			return sessionID
		}
	}
	return strings.TrimSpace(activeSession.SessionID)
}

func (a *RuntimeAgent) emitPromptRouterSideEffects(ctx context.Context, cb acp.PromptCallbacks, activeSession session.Session, result controlprompt.Result) error {
	sessionID := promptRouterResultSessionID(activeSession, result)
	if result.StatusUpdate != nil || result.ClearHistory {
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
	sessionID := strings.TrimSpace(env.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(fallbackSessionID)
	}
	switch env.Kind {
	case eventstream.KindRequestPermission:
		if env.Permission == nil {
			return nil
		}
		if outboundFilter != nil {
			outboundFilter.resetSegment()
		}
		resp, err := cb.RequestPermission(ctx, *env.Permission)
		if err != nil || turn == nil {
			return err
		}
		return turn.SubmitApproval(ctx, approvalDecisionFromACPResponse(env.Permission.Options, resp))
	case eventstream.KindSessionUpdate:
		if env.Update == nil {
			return nil
		}
		if suppressACPBridgeSubagentEnvelope(env) {
			return nil
		}
		return emitFilteredSessionUpdate(ctx, cb, acp.SessionNotification{SessionID: sessionID, Update: env.Update}, env.Final, outboundFilter)
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
		}, true, outboundFilter)
	case eventstream.KindError:
		if env.Err != nil {
			return env.Err
		}
		if strings.TrimSpace(env.Error) != "" {
			return errors.New(strings.TrimSpace(env.Error))
		}
	}
	if env.Update != nil {
		if suppressACPBridgeSubagentEnvelope(env) {
			return nil
		}
		return emitFilteredSessionUpdate(ctx, cb, acp.SessionNotification{SessionID: sessionID, Update: env.Update}, env.Final, outboundFilter)
	}
	return nil
}

func emitFilteredSessionUpdate(ctx context.Context, cb acp.PromptCallbacks, notification acp.SessionNotification, final bool, outboundFilter *acpNarrativeFilter) error {
	if outboundFilter != nil {
		filtered, ok := outboundFilter.FilterNotificationWithFinal(notification, final)
		if !ok {
			return nil
		}
		notification = filtered
	}
	return cb.SessionUpdate(ctx, notification)
}

func approvalDecisionFromACPResponse(options []acp.PermissionOption, resp acp.RequestPermissionResponse) control.ApprovalDecision {
	outcome := strings.TrimSpace(resp.Outcome.Outcome)
	optionID := strings.TrimSpace(resp.Outcome.OptionID)
	approved := false
	if strings.EqualFold(outcome, "selected") {
		for _, option := range options {
			if strings.TrimSpace(option.OptionID) != optionID {
				continue
			}
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(option.Kind)), "allow") {
				approved = true
			}
			break
		}
	}
	return control.ApprovalDecision{
		Outcome:  outcome,
		OptionID: optionID,
		Approved: approved,
	}
}

type controlStreamBridge struct {
	ctx               context.Context
	cancel            context.CancelFunc
	agent             *RuntimeAgent
	cb                acp.PromptCallbacks
	fallbackSessionID string
	streamer          control.StreamSubscriber
	outboundFilter    *acpNarrativeFilter
	mu                sync.Mutex
	ownedToolCalls    map[string]struct{}
	wg                sync.WaitGroup
	errMu             sync.Mutex
	err               error
}

func newControlStreamBridge(ctx context.Context, agent *RuntimeAgent, cb acp.PromptCallbacks, fallbackSessionID string, streamer control.StreamSubscriber, outboundFilter *acpNarrativeFilter) *controlStreamBridge {
	streamCtx, cancel := context.WithCancel(ctx)
	return &controlStreamBridge{
		ctx:               streamCtx,
		cancel:            cancel,
		agent:             agent,
		cb:                cb,
		fallbackSessionID: fallbackSessionID,
		streamer:          streamer,
		outboundFilter:    outboundFilter,
		ownedToolCalls:    map[string]struct{}{},
	}
}

func (b *controlStreamBridge) start(env eventstream.Envelope) {
	if b == nil || b.cb == nil || b.streamer == nil {
		return
	}
	events, ok := b.streamer.SubscribeStream(b.ctx, env)
	if !ok || events == nil {
		return
	}
	b.markOwnedToolCall(toolCallIDFromControlEnvelope(env))
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		for {
			select {
			case <-b.ctx.Done():
				return
			case terminalEnv, ok := <-events:
				if !ok {
					return
				}
				if err := b.agent.emitControlEnvelope(b.ctx, b.cb, b.fallbackSessionID, nil, terminalEnv, b.outboundFilter); err != nil {
					b.recordError(err)
					b.cancel()
					return
				}
			}
		}
	}()
}

func (b *controlStreamBridge) markOwnedToolCall(toolCallID string) {
	if b == nil || strings.TrimSpace(toolCallID) == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ownedToolCalls[strings.TrimSpace(toolCallID)] = struct{}{}
}

func (b *controlStreamBridge) ownsToolCall(toolCallID string) bool {
	if b == nil || strings.TrimSpace(toolCallID) == "" {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.ownedToolCalls[strings.TrimSpace(toolCallID)]
	return ok
}

func toolCallIDFromControlEnvelope(env eventstream.Envelope) string {
	switch update := env.Update.(type) {
	case acp.ToolCall:
		return strings.TrimSpace(update.ToolCallID)
	case acp.ToolCallUpdate:
		return strings.TrimSpace(update.ToolCallID)
	default:
		return ""
	}
}

func (b *controlStreamBridge) wait() error {
	if b == nil {
		return nil
	}
	b.wg.Wait()
	b.errMu.Lock()
	defer b.errMu.Unlock()
	return b.err
}

func (b *controlStreamBridge) recordError(err error) {
	if b == nil || err == nil {
		return
	}
	b.errMu.Lock()
	defer b.errMu.Unlock()
	if b.err == nil {
		b.err = err
	}
}
