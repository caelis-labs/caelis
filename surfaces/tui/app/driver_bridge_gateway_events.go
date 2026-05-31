package tuiapp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/driver"
)

const appTranscriptBatchInterval = 16 * time.Millisecond

type appTranscriptNarrativeBatcher struct {
	pending *TranscriptEvent
	key     string
}

type sessionEventTurn interface {
	SessionEvents() <-chan appviewmodel.SessionEventEnvelope
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
	appTurn, ok := turn.(sessionEventTurn)
	if !ok {
		return
	}
	if appEvents := appTurn.SessionEvents(); appEvents != nil {
		forwardAppSessionTurnEvents(ctx, driver, turn, sender, send, appEvents)
	}
}

func drainTurnEvents(ctx context.Context, turn tuidriver.Turn) {
	ctx = contextOrBackground(ctx)
	if turn == nil {
		return
	}
	if appTurn, ok := turn.(sessionEventTurn); ok {
		if events := appTurn.SessionEvents(); events != nil {
			drainAppSessionTurnEvents(ctx, events)
			return
		}
	}
}

func drainAppSessionTurnEvents(ctx context.Context, events <-chan appviewmodel.SessionEventEnvelope) {
	for events != nil {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-events:
			if !ok {
				events = nil
			}
		}
	}
}

func forwardAppSessionTurnEvents(ctx context.Context, driver tuidriver.Driver, turn tuidriver.Turn, sender *ProgramSender, send func(tea.Msg), events <-chan appviewmodel.SessionEventEnvelope) {
	ticker := time.NewTicker(appTranscriptBatchInterval)
	defer ticker.Stop()

	var batcher appTranscriptNarrativeBatcher
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
			forwardAppSessionEnvelope(ctx, driver, turn, sender, send, &batcher, env)
		}
	}
	batcher.flush(send)
}

func forwardAppSessionEnvelope(ctx context.Context, driver tuidriver.Driver, turn tuidriver.Turn, sender *ProgramSender, send func(tea.Msg), batcher *appTranscriptNarrativeBatcher, env appviewmodel.SessionEventEnvelope) {
	if strings.TrimSpace(env.Error) != "" {
		if batcher != nil {
			batcher.flush(send)
		}
		send(TaskResultMsg{Err: fmt.Errorf("%s", strings.TrimSpace(env.Error))})
		return
	}
	if transcriptEvents := ProjectSessionEventEnvelopeToTranscriptEvents(env); len(transcriptEvents) > 0 {
		if batcher == nil || !batcher.enqueue(transcriptEvents, send) {
			send(TranscriptEventsMsg{Events: transcriptEvents})
		}
	}
	if env.Approval != nil {
		if batcher != nil {
			batcher.flush(send)
		}
		sendApprovalItemPrompt(ctx, turn, env.Approval, send)
		return
	}
}

func (b *appTranscriptNarrativeBatcher) enqueue(events []TranscriptEvent, send func(tea.Msg)) bool {
	if len(events) != 1 {
		b.flush(send)
		return false
	}
	event := events[0]
	key, ok := appTranscriptNarrativeBatchKey(event)
	if !ok {
		b.flush(send)
		return false
	}
	if b.pending == nil {
		copy := event
		b.pending = &copy
		b.key = key
		return true
	}
	if b.key != key {
		b.flush(send)
		copy := event
		b.pending = &copy
		b.key = key
		return true
	}
	b.pending.Text += event.Text
	b.pending.OccurredAt = event.OccurredAt
	return true
}

func (b *appTranscriptNarrativeBatcher) flush(send func(tea.Msg)) {
	if b == nil || b.pending == nil {
		return
	}
	if send != nil {
		send(TranscriptEventsMsg{Events: []TranscriptEvent{*b.pending}})
	}
	b.pending = nil
	b.key = ""
}

func appTranscriptNarrativeBatchKey(event TranscriptEvent) (string, bool) {
	if event.Kind != TranscriptEventNarrative || event.Final || event.Text == "" {
		return "", false
	}
	switch event.NarrativeKind {
	case TranscriptNarrativeAssistant, TranscriptNarrativeReasoning:
	default:
		return "", false
	}
	return strings.Join([]string{
		strings.TrimSpace(string(event.Scope)),
		strings.TrimSpace(event.ScopeID),
		strings.TrimSpace(event.Actor),
		strings.TrimSpace(string(event.NarrativeKind)),
		strings.TrimSpace(event.AnchorToolCallID),
		strings.TrimSpace(event.AnchorToolName),
	}, "\x00"), true
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
	case schema.ToolCallContent:
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
