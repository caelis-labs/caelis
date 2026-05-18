package tuiapp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/driver"
)

const gatewayNarrativeBatchInterval = 16 * time.Millisecond

type gatewayNarrativeBatcher struct {
	pending *kernel.EventEnvelope
	key     string
}

func forwardGatewayTurnEvents(ctx context.Context, driver tuidriver.Driver, turn tuidriver.Turn, sender *ProgramSender) {
	ctx = contextOrBackground(ctx)
	if sender != nil {
		ctx = sender.bindContext(ctx)
	}
	send := sender.sendFunc()
	if turn == nil || send == nil {
		return
	}
	events := turn.Events()
	if events == nil {
		return
	}
	ticker := time.NewTicker(gatewayNarrativeBatchInterval)
	defer ticker.Stop()

	var batcher gatewayNarrativeBatcher
	for events != nil {
		select {
		case <-ctx.Done():
			batcher.flush(send)
			return
		case <-ticker.C:
			batcher.flush(send)
		case env, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if batcher.enqueue(env, send) {
				continue
			}
			send(env)
			startTerminalStreamForwarder(ctx, driver, env, sender)
			if isApprovalGatewayEvent(env.Event.Kind) {
				if !isAutomaticApprovalEvent(env.Event.ApprovalPayload) {
					sendApprovalPrompt(ctx, turn, env.Event.ApprovalPayload, send)
				}
			}
		}
	}
	batcher.flush(send)
}

func (b *gatewayNarrativeBatcher) enqueue(env kernel.EventEnvelope, send func(tea.Msg)) bool {
	key, ok := gatewayNarrativeBatchKey(env)
	if !ok {
		b.flush(send)
		return false
	}
	if b.pending == nil {
		copy := cloneGatewayNarrativeEnvelope(env)
		b.pending = &copy
		b.key = key
		return true
	}
	if b.key != key {
		b.flush(send)
		copy := cloneGatewayNarrativeEnvelope(env)
		b.pending = &copy
		b.key = key
		return true
	}
	mergeGatewayNarrativeEnvelope(b.pending, env)
	return true
}

func (b *gatewayNarrativeBatcher) flush(send func(tea.Msg)) {
	if b == nil || b.pending == nil {
		return
	}
	if send != nil {
		send(*b.pending)
	}
	b.pending = nil
	b.key = ""
}

func gatewayNarrativeBatchKey(env kernel.EventEnvelope) (string, bool) {
	if env.Err != nil || env.Event.Kind != kernel.EventKindAssistantMessage || env.Event.Narrative == nil {
		return "", false
	}
	payload := env.Event.Narrative
	if payload.Role != kernel.NarrativeRoleAssistant || payload.Final || strings.TrimSpace(payload.Visibility) != "ui_only" {
		return "", false
	}
	stream := ""
	switch {
	case payload.ReasoningText != "" && payload.Text == "":
		stream = "reasoning"
	case payload.Text != "" && payload.ReasoningText == "":
		stream = "answer"
	default:
		return "", false
	}
	scope := string(payload.Scope)
	if env.Event.Origin != nil && env.Event.Origin.Scope != "" {
		scope = string(env.Event.Origin.Scope)
	}
	scopeID := gatewayEventScopeID(env.Event)
	return strings.Join([]string{
		strings.TrimSpace(env.Event.HandleID),
		strings.TrimSpace(env.Event.RunID),
		strings.TrimSpace(env.Event.TurnID),
		strings.TrimSpace(env.Event.SessionRef.SessionID),
		strings.TrimSpace(scope),
		strings.TrimSpace(scopeID),
		strings.TrimSpace(payload.ParticipantID),
		strings.TrimSpace(payload.Actor),
		strings.TrimSpace(payload.UpdateType),
		stream,
	}, "\x00"), true
}

func cloneGatewayNarrativeEnvelope(env kernel.EventEnvelope) kernel.EventEnvelope {
	out := env
	if env.Event.Narrative != nil {
		payload := *env.Event.Narrative
		out.Event.Narrative = &payload
	}
	if env.Event.Protocol != nil {
		protocol := session.CloneEventProtocol(*env.Event.Protocol)
		out.Event.Protocol = &protocol
	}
	return out
}

func mergeGatewayNarrativeEnvelope(dst *kernel.EventEnvelope, src kernel.EventEnvelope) {
	if dst == nil || dst.Event.Narrative == nil || src.Event.Narrative == nil {
		return
	}
	dst.Cursor = src.Cursor
	dst.Event.OccurredAt = src.Event.OccurredAt
	dst.Event.Narrative.Text += src.Event.Narrative.Text
	dst.Event.Narrative.ReasoningText += src.Event.Narrative.ReasoningText
	updateGatewayNarrativeProtocolContent(&dst.Event)
}

func updateGatewayNarrativeProtocolContent(event *kernel.Event) {
	if event == nil || event.Narrative == nil || event.Protocol == nil {
		return
	}
	protocol := session.CloneEventProtocol(*event.Protocol)
	if protocol.Update == nil {
		protocol.Update = &session.ProtocolUpdate{}
	}
	switch {
	case event.Narrative.ReasoningText != "" && event.Narrative.Text == "":
		protocol.Update.SessionUpdate = string(session.ProtocolUpdateTypeAgentThought)
		protocol.Update.Content = session.ProtocolTextContent(event.Narrative.ReasoningText)
	case event.Narrative.Text != "":
		protocol.Update.SessionUpdate = string(session.ProtocolUpdateTypeAgentMessage)
		protocol.Update.Content = session.ProtocolTextContent(event.Narrative.Text)
	default:
		return
	}
	protocol.UpdateType = strings.TrimSpace(protocol.Update.SessionUpdate)
	event.Protocol = &protocol
}

func startTerminalStreamForwarder(ctx context.Context, driver tuidriver.Driver, env kernel.EventEnvelope, sender *ProgramSender) {
	ctx = contextOrBackground(ctx)
	if sender != nil {
		ctx = sender.bindContext(ctx)
	}
	send := sender.sendFunc()
	if send == nil {
		return
	}
	streamer, ok := driver.(streamDriver)
	if !ok {
		return
	}
	events, ok := streamer.SubscribeStream(ctx, env)
	if !ok || events == nil {
		return
	}
	start := func() {
		ticker := time.NewTicker(gatewayNarrativeBatchInterval)
		defer ticker.Stop()
		var batcher gatewayTerminalBatcher
		for events != nil {
			select {
			case <-ctx.Done():
				batcher.flush(send)
				return
			case <-ticker.C:
				batcher.flush(send)
			case terminalEnv, ok := <-events:
				if !ok {
					events = nil
					continue
				}
				if batcher.enqueue(terminalEnv, send) {
					continue
				}
				send(terminalEnv)
			}
		}
		batcher.flush(send)
	}
	if sender != nil {
		sender.startForwarder(start)
		return
	}
	go start()
}

type gatewayTerminalBatcher struct {
	pending *kernel.EventEnvelope
	key     string
}

func (b *gatewayTerminalBatcher) enqueue(env kernel.EventEnvelope, send func(tea.Msg)) bool {
	key, ok := gatewayTerminalBatchKey(env)
	if !ok {
		b.flush(send)
		return false
	}
	if b.pending == nil {
		copy := cloneGatewayTerminalEnvelope(env)
		b.pending = &copy
		b.key = key
		return true
	}
	if b.key != key {
		b.flush(send)
		copy := cloneGatewayTerminalEnvelope(env)
		b.pending = &copy
		b.key = key
		return true
	}
	mergeGatewayTerminalEnvelope(b.pending, env)
	return true
}

func (b *gatewayTerminalBatcher) flush(send func(tea.Msg)) {
	if b == nil || b.pending == nil {
		return
	}
	if send != nil {
		send(*b.pending)
	}
	b.pending = nil
	b.key = ""
}

func gatewayTerminalBatchKey(env kernel.EventEnvelope) (string, bool) {
	if env.Err != nil || env.Event.Kind != kernel.EventKindToolResult || env.Event.ToolResult == nil {
		return "", false
	}
	payload := env.Event.ToolResult
	if payload.Status != kernel.ToolStatusRunning {
		return "", false
	}
	text, terminalID := gatewayTerminalContent(env)
	if text == "" {
		return "", false
	}
	return strings.Join([]string{
		strings.TrimSpace(env.Event.HandleID),
		strings.TrimSpace(env.Event.RunID),
		strings.TrimSpace(env.Event.TurnID),
		strings.TrimSpace(env.Event.SessionRef.SessionID),
		strings.TrimSpace(payload.CallID),
		strings.TrimSpace(payload.ToolName),
		terminalID,
	}, "\x00"), true
}

func cloneGatewayTerminalEnvelope(env kernel.EventEnvelope) kernel.EventEnvelope {
	out := env
	if env.Event.ToolResult != nil {
		payload := *env.Event.ToolResult
		payload.RawInput = cloneAnyMap(payload.RawInput)
		payload.RawOutput = cloneAnyMap(payload.RawOutput)
		payload.Content = session.CloneProtocolToolCallContent(payload.Content)
		out.Event.ToolResult = &payload
	}
	if env.Event.Protocol != nil {
		protocol := session.CloneEventProtocol(*env.Event.Protocol)
		out.Event.Protocol = &protocol
	}
	if env.Event.Meta != nil {
		out.Event.Meta = cloneAnyMap(env.Event.Meta)
	}
	return out
}

func mergeGatewayTerminalEnvelope(dst *kernel.EventEnvelope, src kernel.EventEnvelope) {
	if dst == nil || dst.Event.ToolResult == nil || src.Event.ToolResult == nil {
		return
	}
	dst.Cursor = src.Cursor
	dst.Event.OccurredAt = src.Event.OccurredAt
	dstPayload := dst.Event.ToolResult
	if text, terminalID := gatewayTerminalContent(src); text != "" {
		existing, existingTerminalID := gatewayTerminalContent(*dst)
		if strings.EqualFold(strings.TrimSpace(dstPayload.ToolName), "RUN_COMMAND") {
			text = appendDeltaStreamChunk(existing, text)
		} else {
			text = mergeSubagentStreamChunk(existing, text)
		}
		if terminalID == "" {
			terminalID = existingTerminalID
		}
		setGatewayTerminalEnvelopeContent(dst, text, terminalID)
	}
}

func gatewayTerminalContent(env kernel.EventEnvelope) (string, string) {
	if env.Event.ToolResult == nil {
		return "", ""
	}
	content := gatewayProtocolToolContent(env.Event, env.Event.ToolResult.Content)
	for _, item := range content {
		if !strings.EqualFold(strings.TrimSpace(item.Type), "terminal") {
			continue
		}
		if text := protocolTextContent(item.Content); text != "" {
			return text, strings.TrimSpace(item.TerminalID)
		}
	}
	return "", ""
}

func setGatewayTerminalEnvelopeContent(env *kernel.EventEnvelope, text string, terminalID string) {
	if env == nil || env.Event.ToolResult == nil {
		return
	}
	if text == "" {
		return
	}
	content := []session.ProtocolToolCallContent{{
		Type:       "terminal",
		Content:    session.ProtocolTextContent(text),
		TerminalID: strings.TrimSpace(terminalID),
	}}
	env.Event.ToolResult.Content = session.CloneProtocolToolCallContent(content)
	if env.Event.Protocol != nil && env.Event.Protocol.Update != nil {
		env.Event.Protocol.Update.Content = session.CloneProtocolToolCallContent(content)
	}
}

func protocolTextContent(raw any) string {
	switch typed := raw.(type) {
	case nil:
		return ""
	case map[string]any:
		if typ, _ := typed["type"].(string); !strings.EqualFold(strings.TrimSpace(typ), "text") {
			return ""
		}
		text, _ := typed["text"].(string)
		return text
	case session.ProtocolToolCallContent:
		return protocolTextContent(typed.Content)
	default:
		data, err := json.Marshal(raw)
		if err != nil {
			return ""
		}
		var decoded struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(data, &decoded); err != nil {
			return ""
		}
		if !strings.EqualFold(strings.TrimSpace(decoded.Type), "text") {
			return ""
		}
		return decoded.Text
	}
}

func rawString(values map[string]any, key string) string {
	if len(values) == 0 {
		return ""
	}
	switch value := values[key].(type) {
	case string:
		return value
	case fmt.Stringer:
		return value.String()
	default:
		return ""
	}
}

func rawBool(values map[string]any, key string) bool {
	if len(values) == 0 {
		return false
	}
	switch value := values[key].(type) {
	case bool:
		return value
	case string:
		return strings.EqualFold(strings.TrimSpace(value), "true")
	default:
		return false
	}
}

func cloneAnyMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
