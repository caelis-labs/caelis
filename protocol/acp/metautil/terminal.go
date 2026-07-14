package metautil

import (
	"encoding/json"
	"strings"
)

const (
	TerminalInfoKey   = "terminal_info"
	TerminalOutputKey = "terminal_output"
	TerminalExitKey   = "terminal_exit"
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
	return withTopLevelTerminalMeta(meta, TerminalInfoKey, map[string]any{
		"terminal_id": terminalID,
	})
}

func WithTerminalOutput(meta map[string]any, terminalID string, data string) map[string]any {
	terminalID = strings.TrimSpace(terminalID)
	if terminalID == "" || data == "" {
		return CloneMap(meta)
	}
	return withTopLevelTerminalMeta(meta, TerminalOutputKey, map[string]any{
		"terminal_id": terminalID,
		"data":        data,
	})
}

func WithTerminalExit(meta map[string]any, terminalID string, exitCode *int, signal *string) map[string]any {
	terminalID = strings.TrimSpace(terminalID)
	if terminalID == "" {
		return CloneMap(meta)
	}
	values := map[string]any{
		"terminal_id": terminalID,
		"signal":      nil,
	}
	if exitCode != nil {
		code := *exitCode
		values["exit_code"] = code
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
	if key == "" || len(values) == 0 {
		return CloneMap(meta)
	}
	out := CloneMap(meta)
	if out == nil {
		out = map[string]any{}
	}
	out[key] = CloneMap(values)
	return out
}

func topLevelTerminalMeta(meta map[string]any, key string) map[string]any {
	if len(meta) == 0 || key == "" {
		return nil
	}
	return CloneMap(mapAt(meta, key))
}

func stringAt(values map[string]any, key string) string {
	if len(values) == 0 {
		return ""
	}
	text, _ := values[key].(string)
	return text
}

func intAt(values map[string]any, key string) (int, bool) {
	if len(values) == 0 {
		return 0, false
	}
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
