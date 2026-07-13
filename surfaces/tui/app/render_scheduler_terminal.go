package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/surfaces/transcript"
)

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
	text, terminalID := acpTerminalOutput(update)
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
		if text, terminalID := acpTerminalOutput(srcUpdate); text != "" {
			existing, existingTerminalID := acpTerminalOutput(dstUpdate)
			text = mergeTerminalStreamChunk(existing, text)
			if terminalID == "" {
				terminalID = existingTerminalID
			}
			setACPTerminalEnvelopeOutput(dst, text, terminalID)
		}
	}
}

func acpTerminalOutput(update schema.ToolCallUpdate) (string, string) {
	output, ok := metautil.TerminalOutput(update.Meta)
	if ok {
		return output.Data, output.TerminalID
	}
	info, ok := metautil.TerminalInfo(update.Meta)
	if ok {
		return "", info.TerminalID
	}
	return "", ""
}

func setACPTerminalEnvelopeOutput(env *eventstream.Envelope, text string, terminalID string) {
	if env == nil || text == "" {
		return
	}
	switch update := env.Update.(type) {
	case schema.ToolCallUpdate:
		update.Meta = metautil.WithTerminalOutput(update.Meta, terminalID, text)
		env.Update = update
	case schema.ToolCall:
		update.Meta = metautil.WithTerminalOutput(update.Meta, terminalID, text)
		env.Update = update
	}
}
