package tuiapp

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/protocol/acp/control"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/surfaces/transcript"
)

const (
	eventStreamBatchInterval = 16 * time.Millisecond
)

// eventStreamNarrativeBatcher bounds ProgramSender traffic before Tea receives
// the feed. The render scheduler independently coalesces its render-tick
// queue, including callers that bypass this Control-service forwarding path.
type eventStreamNarrativeBatcher struct {
	pending *eventstream.Envelope
	key     string
}

func forwardTurnEventStream(ctx context.Context, turn control.Turn, sender *ProgramSender) executeLineResult {
	ctx = contextOrBackground(ctx)
	if sender != nil {
		ctx = sender.bindContext(ctx)
	}
	send := sender.sendFunc()
	if turn == nil || send == nil {
		return executeLineResult{completion: TaskResultMsg{}}
	}
	events := turn.Events()
	ticker := time.NewTicker(eventStreamBatchInterval)
	defer ticker.Stop()

	var batcher eventStreamNarrativeBatcher
	failureReason := ""
	cancelled := false
	cancelSignal := ctx.Done()
	cancelRequested := false
	for events != nil {
		select {
		case <-cancelSignal:
			// Cancelling the TUI run context requests cancellation; it is not a
			// completed Turn boundary. Keep consuming the authoritative stream so
			// Control can cross its Runtime producer and lease-release barrier
			// before the UI observes the one terminal lifecycle envelope.
			cancelSignal = nil
			cancelRequested = true
			batcher.flush(send)
			turn.Cancel()
		case <-ticker.C:
			batcher.flush(send)
		case env, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if reason := eventStreamEnvelopeErrorReason(env); reason != "" {
				failureReason = reason
				cancelled = eventstream.IsCancelledReason(reason)
			}
			if batcher.enqueue(env, send) {
				continue
			}
			if isMainTurnTerminalLifecycle(env) {
				copy := eventstream.CloneEnvelope(env)
				batcher.flush(send)
				send(copy)
				return executeLineResult{queued: true}
			}
			send(env)
			if req := approvalPayloadFromACPEvent(env); req != nil {
				sendApprovalPrompt(ctx, turn, req, send)
			}
		}
	}
	batcher.flush(send)
	var terminal eventstream.Envelope
	switch {
	case cancelled || cancelRequested:
		terminal = eventstream.TurnCancelled(turn.HandleID(), turn.RunID(), turn.TurnID(), failureReason, time.Now())
	case failureReason != "":
		terminal = eventstream.TurnFailed(turn.HandleID(), turn.RunID(), turn.TurnID(), failureReason, time.Now())
	default:
		terminal = eventstream.TurnCompleted(turn.HandleID(), turn.RunID(), turn.TurnID(), time.Now())
	}
	send(terminal)
	return executeLineResult{queued: true}
}

func eventStreamEnvelopeErrorReason(env eventstream.Envelope) string {
	if env.Err == nil && env.Kind != eventstream.KindError {
		return ""
	}
	if env.Err != nil {
		return display.UserVisibleError(env.Err)
	}
	if text := strings.TrimSpace(env.Error); text != "" {
		return text
	}
	return ""
}

func approvalPayloadFromACPEvent(env eventstream.Envelope) *approvalPayload {
	if env.Kind != eventstream.KindRequestPermission || env.Permission == nil {
		return nil
	}
	tool := env.Permission.ToolCall
	rawInput := transcript.RawMap(tool.RawInput)
	options := make([]approvalOption, 0, len(env.Permission.Options))
	for _, option := range env.Permission.Options {
		options = append(options, approvalOption{
			ID:   strings.TrimSpace(option.OptionID),
			Name: strings.TrimSpace(option.Name),
			Kind: strings.TrimSpace(option.Kind),
		})
	}
	return &approvalPayload{
		RequestID:  env.ApprovalRequestID,
		ToolCallID: strings.TrimSpace(tool.ToolCallID),
		ToolName: firstNonEmpty(
			transcript.MetaString(transcript.MergeMeta(transcript.ACPUpdateMeta(tool), env.Meta), "caelis", "runtime", "tool", "name"),
			transcript.StringFromPtr(tool.Title),
			transcript.StringFromPtr(tool.Kind),
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
	text := transcript.ProtocolTextContent(update.Content)
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
		strings.TrimSpace(update.MessageID),
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
	dstText := transcript.ProtocolTextContent(dstUpdate.Content)
	srcText := transcript.ProtocolTextContent(srcUpdate.Content)
	if srcText == "" {
		return
	}
	// ACP content chunks are deltas. Coalescing is only a client-delivery
	// optimization, so preserve every byte (including repeated chunks) while
	// keeping the latest Envelope as the complete transport identity. Any
	// cumulative-stream normalization belongs before the Surface boundary.
	merged := dstText + srcText
	latest := eventstream.CloneEnvelope(src)
	latestUpdate, ok := latest.Update.(schema.ContentChunk)
	if !ok {
		return
	}
	latestUpdate.Content = schema.TextContent{Type: "text", Text: merged}
	latest.Update = latestUpdate
	*dst = latest
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
