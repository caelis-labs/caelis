package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/surfaces/internal/transcript"
)

func eventStreamTerminalBatchKey(env eventstream.Envelope) (string, bool) {
	if env.Err != nil || env.Kind != eventstream.KindSessionUpdate {
		return "", false
	}
	update, ok := env.Update.(eventstream.ToolCallUpdate)
	if !ok {
		return "", false
	}
	if transcript.StringFromPtr(update.Status) != eventstream.ToolStatusInProgress {
		return "", false
	}
	text, terminalID := acpTerminalOutput(update)
	if text == "" {
		return "", false
	}
	toolName := transcript.ToolNameFromMeta(transcript.MergeMeta(eventstream.UpdateMeta(update), env.Meta))
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
	dstUpdate, ok := dst.Update.(eventstream.ToolCallUpdate)
	if !ok {
		return
	}
	dst.Cursor = src.Cursor
	dst.OccurredAt = src.OccurredAt
	if srcUpdate, ok := src.Update.(eventstream.ToolCallUpdate); ok {
		if text, terminalID := acpTerminalOutput(srcUpdate); text != "" {
			existing, existingTerminalID := acpTerminalOutput(dstUpdate)
			// ACP terminal_output contains exact incremental bytes. Scheduler
			// batching may coalesce frames, but must never infer overlap or treat
			// repeated log lines as cumulative snapshots.
			text = existing + text
			if terminalID == "" {
				terminalID = existingTerminalID
			}
			setACPTerminalEnvelopeOutput(dst, text, terminalID)
		}
	}
}

func acpTerminalOutput(update eventstream.ToolCallUpdate) (string, string) {
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
	case eventstream.ToolCallUpdate:
		update.Meta = metautil.WithTerminalOutput(update.Meta, terminalID, text)
		env.Update = update
	case eventstream.ToolCall:
		update.Meta = metautil.WithTerminalOutput(update.Meta, terminalID, text)
		env.Update = update
	}
}
