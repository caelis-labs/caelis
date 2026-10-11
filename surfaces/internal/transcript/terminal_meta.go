package transcript

import (
	"encoding/json"
	"strings"
)

const (
	terminalInfoMetaKey        = "terminal_info"
	terminalOutputMetaKey      = "terminal_output"
	terminalOutputDeltaMetaKey = "terminal_output_delta"
	terminalExitMetaKey        = "terminal_exit"
)

type TerminalInfoMeta struct {
	TerminalID string
}

type TerminalOutputMeta struct {
	TerminalID  string
	Data        string
	Replacement bool
}

type TerminalExitMeta struct {
	TerminalID string
	ExitCode   *int
	Signal     *string
}

func ReadTerminalInfo(meta map[string]any) (TerminalInfoMeta, bool) {
	values := terminalMeta(meta, terminalInfoMetaKey)
	id := strings.TrimSpace(terminalString(values, "terminal_id"))
	if id == "" {
		return TerminalInfoMeta{}, false
	}
	return TerminalInfoMeta{TerminalID: id}, true
}

func ReadTerminalOutput(meta map[string]any) (TerminalOutputMeta, bool) {
	values := terminalMeta(meta, terminalOutputMetaKey)
	id := strings.TrimSpace(terminalString(values, "terminal_id"))
	data, present := values["data"].(string)
	replacement, _ := values["replacement"].(bool)
	if id == "" || !present || data == "" && !replacement {
		return TerminalOutputMeta{}, false
	}
	return TerminalOutputMeta{TerminalID: id, Data: data, Replacement: replacement}, true
}

func ReadTerminalExit(meta map[string]any) (TerminalExitMeta, bool) {
	values := terminalMeta(meta, terminalExitMetaKey)
	id := strings.TrimSpace(terminalString(values, "terminal_id"))
	if id == "" {
		return TerminalExitMeta{}, false
	}
	out := TerminalExitMeta{TerminalID: id}
	if code, ok := terminalInt(values, "exit_code"); ok {
		out.ExitCode = &code
	}
	if signal := terminalString(values, "signal"); signal != "" {
		out.Signal = &signal
	}
	return out, true
}

// WithTerminalOutput updates the Surface-owned batching snapshot. Canonical
// output wins over the maintained provider alias.
func WithTerminalOutput(meta map[string]any, terminalID, data string, replacement ...bool) map[string]any {
	terminalID = strings.TrimSpace(terminalID)
	replace := len(replacement) > 0 && replacement[0]
	if terminalID == "" || data == "" && !replace {
		return CloneAnyMap(meta)
	}
	out := CloneAnyMap(meta)
	if out == nil {
		out = map[string]any{}
	}
	delete(out, terminalOutputDeltaMetaKey)
	values := map[string]any{"terminal_id": terminalID, "data": data}
	if replace {
		values["replacement"] = true
	}
	out[terminalOutputMetaKey] = values
	return out
}

func terminalMeta(meta map[string]any, key string) map[string]any {
	values, _ := meta[key].(map[string]any)
	return CloneAnyMap(values)
}

func terminalString(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

func terminalInt(values map[string]any, key string) (int, bool) {
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
