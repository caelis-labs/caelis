package codex

import (
	"strings"

	"github.com/caelis-labs/caelis/internal/jsonvalue"
)

const (
	terminalInfoMetaKey        = "terminal_info"
	terminalOutputMetaKey      = "terminal_output"
	terminalOutputDeltaMetaKey = "terminal_output_delta"
	terminalExitMetaKey        = "terminal_exit"
)

func withTerminalInfoMeta(meta map[string]any, terminalID string) map[string]any {
	terminalID = strings.TrimSpace(terminalID)
	if terminalID == "" {
		return jsonvalue.CloneMap(meta)
	}
	return withTerminalMeta(meta, terminalInfoMetaKey, map[string]any{"terminal_id": terminalID})
}

func withCanonicalTerminalOutputMeta(meta map[string]any, terminalID string, data string) map[string]any {
	terminalID = strings.TrimSpace(terminalID)
	if terminalID == "" || data == "" {
		return jsonvalue.CloneMap(meta)
	}
	out := jsonvalue.CloneMap(meta)
	delete(out, terminalOutputMetaKey)
	delete(out, terminalOutputDeltaMetaKey)
	return withTerminalMeta(out, terminalOutputMetaKey, map[string]any{
		"terminal_id": terminalID,
		"data":        data,
	})
}

func withTerminalExitMeta(meta map[string]any, terminalID string, exitCode *int) map[string]any {
	terminalID = strings.TrimSpace(terminalID)
	if terminalID == "" {
		return jsonvalue.CloneMap(meta)
	}
	values := map[string]any{"terminal_id": terminalID, "signal": nil}
	if exitCode != nil {
		values["exit_code"] = *exitCode
	}
	return withTerminalMeta(meta, terminalExitMetaKey, values)
}

func withTerminalMeta(meta map[string]any, key string, values map[string]any) map[string]any {
	out := jsonvalue.CloneMap(meta)
	if out == nil {
		out = map[string]any{}
	}
	out[key] = jsonvalue.CloneMap(values)
	return out
}
