package tuiapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/protocol/acp/control"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
	"github.com/OnslaughtSnail/caelis/surfaces/transcript"
)

const (
	eventStreamBatchInterval          = 16 * time.Millisecond
	eventStreamCompletionDrainTimeout = 100 * time.Millisecond
)

type eventStreamNarrativeBatcher struct {
	pending *eventstream.Envelope
	key     string
}

func forwardTurnEventStream(ctx context.Context, service control.Service, turn control.Turn, sender *ProgramSender) executeLineResult {
	ctx = contextOrBackground(ctx)
	if sender != nil {
		ctx = sender.bindContext(ctx)
	}
	send := sender.sendFunc()
	if turn == nil || send == nil {
		return executeLineResult{completion: TaskResultMsg{}}
	}
	events := turn.Events()
	if events == nil {
		return executeLineResult{completion: TaskResultMsg{}}
	}
	streamCtx, cancelStreams := context.WithCancel(ctx)
	defer cancelStreams()
	ticker := time.NewTicker(eventStreamBatchInterval)
	defer ticker.Stop()

	var batcher eventStreamNarrativeBatcher
	drain := &turnForwarderDrain{}
	for events != nil {
		select {
		case <-ctx.Done():
			batcher.flush(send)
			return finalizeForwardedTurn(drain, cancelStreams, send, taskResultForContextDone(ctx.Err()))
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
			if eventStreamEnvelopeCompletesTurn(env) {
				batcher.flush(send)
				return finalizeForwardedTurn(drain, cancelStreams, send, taskResultFromEnvelope(env))
			}
			send(env)
			startTerminalStreamForwarder(streamCtx, service, env, sender, drain)
			if req := approvalPayloadFromACPEvent(env); req != nil {
				sendApprovalPrompt(ctx, turn, req, send)
			}
		}
	}
	batcher.flush(send)
	return finalizeForwardedTurn(drain, cancelStreams, send, TaskResultMsg{})
}

func finalizeForwardedTurn(drain *turnForwarderDrain, cancelForwarders func(), send func(tea.Msg), completion TaskResultMsg) executeLineResult {
	drain.wait(eventStreamCompletionDrainTimeout)
	if cancelForwarders != nil {
		cancelForwarders()
		drain.wait(eventStreamBatchInterval)
	}
	if send == nil {
		return executeLineResult{completion: completion}
	}
	// Queue completion in the same lane as streamed ACP envelopes so the TUI
	// appends the turn divider after every final transcript/tool event.
	send(completion)
	return executeLineResult{queued: true}
}

type turnForwarderDrain struct {
	wg sync.WaitGroup
}

func (d *turnForwarderDrain) add() {
	if d != nil {
		d.wg.Add(1)
	}
}

func (d *turnForwarderDrain) done() {
	if d != nil {
		d.wg.Done()
	}
}

// wait bounds completion on terminal stream forwarding. Terminal subscriptions
// can remain open after the ACP turn closes, so timeout means "stop waiting";
// finalizeForwardedTurn cancels those forwarders before queueing completion.
func (d *turnForwarderDrain) wait(timeout time.Duration) {
	if d == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	if timeout <= 0 {
		<-done
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

func taskResultForContextDone(err error) TaskResultMsg {
	if errors.Is(err, context.Canceled) {
		return TaskResultMsg{Interrupted: true}
	}
	if err != nil {
		return TaskResultMsg{Err: err}
	}
	return TaskResultMsg{}
}

func taskResultFromEnvelope(env eventstream.Envelope) TaskResultMsg {
	if env.Err != nil {
		if isUserInterruptError(env.Err) {
			return TaskResultMsg{Err: env.Err, Interrupted: true}
		}
		return TaskResultMsg{Err: env.Err}
	}
	if env.Kind == eventstream.KindError && strings.TrimSpace(env.Error) != "" {
		return TaskResultMsg{Err: errors.New(env.Error)}
	}
	return TaskResultMsg{}
}

func eventStreamEnvelopeCompletesTurn(env eventstream.Envelope) bool {
	return env.Err != nil || (env.Kind == eventstream.KindError && strings.TrimSpace(env.Error) != "")
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
	dst.Cursor = src.Cursor
	dst.OccurredAt = src.OccurredAt
	dstUpdate.Content = schema.TextContent{Type: "text", Text: dstText + srcText}
	dst.Update = dstUpdate
}

func startTerminalStreamForwarder(ctx context.Context, service control.Service, env eventstream.Envelope, sender *ProgramSender, drain *turnForwarderDrain) bool {
	ctx = contextOrBackground(ctx)
	if sender != nil {
		ctx = sender.bindContext(ctx)
	}
	send := sender.sendFunc()
	if send == nil {
		return false
	}
	streamer, ok := service.(control.StreamSubscriber)
	if !ok {
		return false
	}
	events, ok := streamer.SubscribeStream(ctx, env)
	if !ok || events == nil {
		return false
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
				if ctx.Err() != nil {
					batcher.flush(send)
					return
				}
				if batcher.enqueue(terminalEnv, send) {
					continue
				}
				send(terminalEnv)
			}
		}
		batcher.flush(send)
	}
	run := func() {
		defer drain.done()
		start()
	}
	drain.add()
	if sender != nil {
		if sender.startForwarder(run) {
			return true
		}
		drain.done()
		return false
	}
	go run()
	return true
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
	if transcript.StringFromPtr(update.Status) != schema.ToolStatusInProgress {
		return "", false
	}
	text, terminalID := acpTerminalContent(update)
	if text == "" {
		return "", false
	}
	toolName := acpUpdateToolName(transcript.MergeMeta(transcript.ACPUpdateMeta(update), env.Meta), transcript.StringFromPtr(update.Title), transcript.StringFromPtr(update.Kind))
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
			toolName := acpUpdateToolName(transcript.MergeMeta(transcript.ACPUpdateMeta(dstUpdate), dst.Meta), transcript.StringFromPtr(dstUpdate.Title), transcript.StringFromPtr(dstUpdate.Kind))
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
		if text := transcript.ProtocolTextContent(item.Content); text != "" {
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
