package eventmeta

import (
	"encoding/json"
	"strings"
)

const (
	TerminalInfoKey        = "terminal_info"
	TerminalOutputKey      = "terminal_output"
	TerminalOutputDeltaKey = "terminal_output_delta"
	TerminalExitKey        = "terminal_exit"
)

type TerminalInfoMeta struct {
	TerminalID string
}

type TerminalOutputMeta struct {
	TerminalID string
	Data       string
}

type TerminalExitMeta struct {
	TerminalID string
	ExitCode   *int
	Signal     *string
}

func WithTerminalInfo(meta map[string]any, terminalID string) map[string]any {
	terminalID = strings.TrimSpace(terminalID)
	if terminalID == "" {
		return CloneMap(meta)
	}
	return withTopLevelTerminalMeta(meta, TerminalInfoKey, map[string]any{"terminal_id": terminalID})
}

func WithTerminalOutput(meta map[string]any, terminalID string, data string) map[string]any {
	terminalID = strings.TrimSpace(terminalID)
	if terminalID == "" || data == "" {
		return CloneMap(meta)
	}
	return withTopLevelTerminalMeta(WithoutTerminalOutput(meta), TerminalOutputKey, map[string]any{
		"terminal_id": terminalID,
		"data":        data,
	})
}

func WithoutTerminalOutput(meta map[string]any) map[string]any {
	out := CloneMap(meta)
	delete(out, TerminalOutputKey)
	delete(out, TerminalOutputDeltaKey)
	if len(out) == 0 {
		return nil
	}
	return out
}

func WithTerminalExit(meta map[string]any, terminalID string, exitCode *int, signal *string) map[string]any {
	terminalID = strings.TrimSpace(terminalID)
	if terminalID == "" {
		return CloneMap(meta)
	}
	values := map[string]any{"terminal_id": terminalID, "signal": nil}
	if exitCode != nil {
		values["exit_code"] = *exitCode
	}
	if signal != nil {
		values["signal"] = *signal
	}
	return withTopLevelTerminalMeta(meta, TerminalExitKey, values)
}

func TerminalInfo(meta map[string]any) (TerminalInfoMeta, bool) {
	values := topLevelTerminalMeta(meta, TerminalInfoKey)
	id := strings.TrimSpace(stringAt(values, "terminal_id"))
	if id == "" {
		return TerminalInfoMeta{}, false
	}
	return TerminalInfoMeta{TerminalID: id}, true
}

func TerminalOutput(meta map[string]any) (TerminalOutputMeta, bool) {
	values := topLevelTerminalMeta(meta, TerminalOutputKey)
	id := strings.TrimSpace(stringAt(values, "terminal_id"))
	data, _ := values["data"].(string)
	if id == "" || data == "" {
		return TerminalOutputMeta{}, false
	}
	return TerminalOutputMeta{TerminalID: id, Data: data}, true
}

func TerminalExit(meta map[string]any) (TerminalExitMeta, bool) {
	values := topLevelTerminalMeta(meta, TerminalExitKey)
	id := strings.TrimSpace(stringAt(values, "terminal_id"))
	if id == "" {
		return TerminalExitMeta{}, false
	}
	out := TerminalExitMeta{TerminalID: id}
	if code, ok := intAt(values, "exit_code"); ok {
		out.ExitCode = &code
	}
	if signal := stringAt(values, "signal"); signal != "" {
		out.Signal = &signal
	}
	return out, true
}

func withTopLevelTerminalMeta(meta map[string]any, key string, values map[string]any) map[string]any {
	out := CloneMap(meta)
	if out == nil {
		out = map[string]any{}
	}
	out[key] = CloneMap(values)
	return out
}

func topLevelTerminalMeta(meta map[string]any, key string) map[string]any {
	return CloneMap(mapAt(meta, key))
}

func stringAt(values map[string]any, key string) string {
	text, _ := values[key].(string)
	return text
}

func intAt(values map[string]any, key string) (int, bool) {
	switch typed := values[key].(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		value, err := typed.Int64()
		return int(value), err == nil
	default:
		return 0, false
	}
}
