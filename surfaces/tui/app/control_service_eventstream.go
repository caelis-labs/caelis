package tuiapp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/protocol/acp/control"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

const eventStreamBatchInterval = 16 * time.Millisecond

type eventStreamNarrativeBatcher struct {
	pending *eventstream.Envelope
	key     string
}

func forwardTurnEventStream(ctx context.Context, service control.Service, turn control.Turn, sender *ProgramSender) {
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
	ticker := time.NewTicker(eventStreamBatchInterval)
	defer ticker.Stop()

	var batcher eventStreamNarrativeBatcher
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
			startTerminalStreamForwarder(ctx, service, env, sender)
			if req := approvalPayloadFromACPEvent(env); req != nil {
				sendApprovalPrompt(ctx, turn, req, send)
			}
		}
	}
	batcher.flush(send)
}

func approvalPayloadFromACPEvent(env eventstream.Envelope) *approvalPayload {
	if env.Kind != eventstream.KindRequestPermission || env.Permission == nil {
		return nil
	}
	tool := env.Permission.ToolCall
	rawInput := acpRawMap(tool.RawInput)
	options := make([]approvalOption, 0, len(env.Permission.Options))
	for _, option := range env.Permission.Options {
		options = append(options, approvalOption{
			ID:   strings.TrimSpace(option.OptionID),
			Name: strings.TrimSpace(option.Name),
			Kind: strings.TrimSpace(option.Kind),
		})
	}
	return &approvalPayload{
		ToolCallID: strings.TrimSpace(tool.ToolCallID),
		ToolName: firstNonEmpty(
			metaString(mergeTranscriptMeta(acpUpdateMeta(tool), env.Meta), "caelis", "runtime", "tool", "name"),
			stringFromPtr(tool.Title),
			stringFromPtr(tool.Kind),
		),
		RawInput:           rawInput,
		Reason:             firstNonEmpty(rawString(rawInput, "approval_reason"), rawString(rawInput, "reason")),
		Justification:      rawString(rawInput, "justification"),
		SandboxPermissions: rawString(rawInput, "sandbox_permissions"),
		Options:            options,
	}
}

func (b *eventStreamNarrativeBatcher) enqueue(env eventstream.Envelope, send func(tea.Msg)) bool {
	key, ok := eventStreamNarrativeBatchKey(env)
	if !ok {
		b.flush(send)
		return false
	}
	if b.pending == nil {
		copy := cloneEventStreamNarrativeEnvelope(env)
		b.pending = &copy
		b.key = key
		return true
	}
	if b.key != key {
		b.flush(send)
		copy := cloneEventStreamNarrativeEnvelope(env)
		b.pending = &copy
		b.key = key
		return true
	}
	mergeEventStreamNarrativeEnvelope(b.pending, env)
	return true
}

func (b *eventStreamNarrativeBatcher) flush(send func(tea.Msg)) {
	if b == nil || b.pending == nil {
		return
	}
	if send != nil {
		send(*b.pending)
	}
	b.pending = nil
	b.key = ""
}

func eventStreamNarrativeBatchKey(env eventstream.Envelope) (string, bool) {
	if env.Err != nil || env.Kind != eventstream.KindSessionUpdate || env.Final {
		return "", false
	}
	update, ok := env.Update.(schema.ContentChunk)
	if !ok {
		return "", false
	}
	text := protocolTextContent(update.Content)
	if text == "" {
		return "", false
	}
	updateType := strings.TrimSpace(update.SessionUpdate)
	if updateType != schema.UpdateAgentMessage && updateType != schema.UpdateAgentThought {
		return "", false
	}
	return strings.Join([]string{
		strings.TrimSpace(env.HandleID),
		strings.TrimSpace(env.RunID),
		strings.TrimSpace(env.TurnID),
		strings.TrimSpace(env.SessionID),
		strings.TrimSpace(string(env.Scope)),
		strings.TrimSpace(env.ScopeID),
		strings.TrimSpace(env.ParticipantID),
		strings.TrimSpace(env.Actor),
		updateType,
	}, "\x00"), true
}

func cloneEventStreamNarrativeEnvelope(env eventstream.Envelope) eventstream.Envelope {
	return eventstream.CloneEnvelope(env)
}

func mergeEventStreamNarrativeEnvelope(dst *eventstream.Envelope, src eventstream.Envelope) {
	if dst == nil {
		return
	}
	dstUpdate, ok := dst.Update.(schema.ContentChunk)
	if !ok {
		return
	}
	srcUpdate, ok := src.Update.(schema.ContentChunk)
	if !ok {
		return
	}
	dstText := protocolTextContent(dstUpdate.Content)
	srcText := protocolTextContent(srcUpdate.Content)
	if srcText == "" {
		return
	}
	dst.Cursor = src.Cursor
	dst.OccurredAt = src.OccurredAt
	dstUpdate.Content = schema.TextContent{Type: "text", Text: dstText + srcText}
	dst.Update = dstUpdate
}

func startTerminalStreamForwarder(ctx context.Context, service control.Service, env eventstream.Envelope, sender *ProgramSender) {
	ctx = contextOrBackground(ctx)
	if sender != nil {
		ctx = sender.bindContext(ctx)
	}
	send := sender.sendFunc()
	if send == nil {
		return
	}
	streamer, ok := service.(control.StreamSubscriber)
	if !ok {
		return
	}
	events, ok := streamer.SubscribeStream(ctx, env)
	if !ok || events == nil {
		return
	}
	start := func() {
		ticker := time.NewTicker(eventStreamBatchInterval)
		defer ticker.Stop()
		var batcher eventStreamTerminalBatcher
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

type eventStreamTerminalBatcher struct {
	pending *eventstream.Envelope
	key     string
}

func (b *eventStreamTerminalBatcher) enqueue(env eventstream.Envelope, send func(tea.Msg)) bool {
	key, ok := eventStreamTerminalBatchKey(env)
	if !ok {
		b.flush(send)
		return false
	}
	if b.pending == nil {
		copy := cloneEventStreamTerminalEnvelope(env)
		b.pending = &copy
		b.key = key
		return true
	}
	if b.key != key {
		b.flush(send)
		copy := cloneEventStreamTerminalEnvelope(env)
		b.pending = &copy
		b.key = key
		return true
	}
	mergeEventStreamTerminalEnvelope(b.pending, env)
	return true
}

func (b *eventStreamTerminalBatcher) flush(send func(tea.Msg)) {
	if b == nil || b.pending == nil {
		return
	}
	if send != nil {
		send(*b.pending)
	}
	b.pending = nil
	b.key = ""
}

func eventStreamTerminalBatchKey(env eventstream.Envelope) (string, bool) {
	if env.Err != nil || env.Kind != eventstream.KindSessionUpdate {
		return "", false
	}
	update, ok := env.Update.(schema.ToolCallUpdate)
	if !ok {
		return "", false
	}
	if stringFromPtr(update.Status) != schema.ToolStatusInProgress {
		return "", false
	}
	text, terminalID := acpTerminalContent(update)
	if text == "" {
		return "", false
	}
	toolName := acpUpdateToolName(mergeTranscriptMeta(acpUpdateMeta(update), env.Meta), stringFromPtr(update.Title), stringFromPtr(update.Kind))
	return strings.Join([]string{
		strings.TrimSpace(env.HandleID),
		strings.TrimSpace(env.RunID),
		strings.TrimSpace(env.TurnID),
		strings.TrimSpace(env.SessionID),
		strings.TrimSpace(update.ToolCallID),
		strings.TrimSpace(toolName),
		terminalID,
	}, "\x00"), true
}

func cloneEventStreamTerminalEnvelope(env eventstream.Envelope) eventstream.Envelope {
	return eventstream.CloneEnvelope(env)
}

func mergeEventStreamTerminalEnvelope(dst *eventstream.Envelope, src eventstream.Envelope) {
	if dst == nil {
		return
	}
	dstUpdate, ok := dst.Update.(schema.ToolCallUpdate)
	if !ok {
		return
	}
	dst.Cursor = src.Cursor
	dst.OccurredAt = src.OccurredAt
	if srcUpdate, ok := src.Update.(schema.ToolCallUpdate); ok {
		if text, terminalID := acpTerminalContent(srcUpdate); text != "" {
			existing, existingTerminalID := acpTerminalContent(dstUpdate)
			toolName := acpUpdateToolName(mergeTranscriptMeta(acpUpdateMeta(dstUpdate), dst.Meta), stringFromPtr(dstUpdate.Title), stringFromPtr(dstUpdate.Kind))
			if strings.EqualFold(strings.TrimSpace(toolName), "RUN_COMMAND") {
				text = mergeCommandStreamChunk(existing, text)
			} else {
				text = mergeSubagentStreamChunk(existing, text)
			}
			if terminalID == "" {
				terminalID = existingTerminalID
			}
			setACPTerminalEnvelopeContent(dst, text, terminalID)
		}
	}
}

func acpTerminalContent(update schema.ToolCallUpdate) (string, string) {
	for _, item := range update.Content {
		if !strings.EqualFold(strings.TrimSpace(item.Type), "terminal") {
			continue
		}
		if text := protocolTextContent(item.Content); text != "" {
			return text, strings.TrimSpace(item.TerminalID)
		}
	}
	return "", ""
}

func setACPTerminalEnvelopeContent(env *eventstream.Envelope, text string, terminalID string) {
	if env == nil || text == "" {
		return
	}
	switch update := env.Update.(type) {
	case schema.ToolCallUpdate:
		update.Content = []schema.ToolCallContent{{
			Type:       "terminal",
			Content:    schema.TextContent{Type: "text", Text: text},
			TerminalID: strings.TrimSpace(terminalID),
		}}
		env.Update = update
	case schema.ToolCall:
		update.Content = []schema.ToolCallContent{{
			Type:       "terminal",
			Content:    schema.TextContent{Type: "text", Text: text},
			TerminalID: strings.TrimSpace(terminalID),
		}}
		env.Update = update
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
