// Package acpmeta owns Caelis metadata compatibility at the external ACP Host
// boundary. It is private to the ACP Agent bridge and is not a product protocol.
package acpmeta

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/internal/jsonvalue"
)

const (
	TerminalInfoKey        = "terminal_info"
	TerminalOutputKey      = "terminal_output"
	TerminalOutputDeltaKey = "terminal_output_delta"
	TerminalExitKey        = "terminal_exit"
)

type TerminalInfo struct {
	TerminalID string
}

type TerminalOutput struct {
	TerminalID string
	Data       string
}

type TerminalExit struct {
	TerminalID string
	ExitCode   *int
	Signal     *string
}

func WithTerminalInfo(meta map[string]any, terminalID string) map[string]any {
	terminalID = strings.TrimSpace(terminalID)
	if terminalID == "" {
		return jsonvalue.CloneMap(meta)
	}
	return withTerminalMeta(meta, TerminalInfoKey, map[string]any{"terminal_id": terminalID})
}

func WithTerminalOutput(meta map[string]any, terminalID string, data string) map[string]any {
	terminalID = strings.TrimSpace(terminalID)
	if terminalID == "" || data == "" {
		return jsonvalue.CloneMap(meta)
	}
	return withTerminalMeta(WithoutTerminalOutput(meta), TerminalOutputKey, map[string]any{
		"terminal_id": terminalID,
		"data":        data,
	})
}

func WithoutTerminalOutput(meta map[string]any) map[string]any {
	out := jsonvalue.CloneMap(meta)
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
		return jsonvalue.CloneMap(meta)
	}
	values := map[string]any{"terminal_id": terminalID, "signal": nil}
	if exitCode != nil {
		values["exit_code"] = *exitCode
	}
	if signal != nil {
		values["signal"] = *signal
	}
	return withTerminalMeta(meta, TerminalExitKey, values)
}

func ReadTerminalInfo(meta map[string]any) (TerminalInfo, bool) {
	values := terminalMeta(meta, TerminalInfoKey)
	id := strings.TrimSpace(stringAt(values, "terminal_id"))
	if id == "" {
		return TerminalInfo{}, false
	}
	return TerminalInfo{TerminalID: id}, true
}

func ReadTerminalOutput(meta map[string]any) (TerminalOutput, bool) {
	values := terminalMeta(meta, TerminalOutputKey)
	id := strings.TrimSpace(stringAt(values, "terminal_id"))
	data, _ := values["data"].(string)
	if id == "" || data == "" {
		return TerminalOutput{}, false
	}
	return TerminalOutput{TerminalID: id, Data: data}, true
}

func ReadTerminalExit(meta map[string]any) (TerminalExit, bool) {
	values := terminalMeta(meta, TerminalExitKey)
	id := strings.TrimSpace(stringAt(values, "terminal_id"))
	if id == "" {
		return TerminalExit{}, false
	}
	out := TerminalExit{TerminalID: id}
	if code, ok := intAt(values, "exit_code"); ok {
		out.ExitCode = &code
	}
	if signal := stringAt(values, "signal"); signal != "" {
		out.Signal = &signal
	}
	return out, true
}

func withTerminalMeta(meta map[string]any, key string, values map[string]any) map[string]any {
	out := jsonvalue.CloneMap(meta)
	if out == nil {
		out = map[string]any{}
	}
	out[key] = jsonvalue.CloneMap(values)
	return out
}

func terminalMeta(meta map[string]any, key string) map[string]any {
	values, _ := meta[key].(map[string]any)
	return jsonvalue.CloneMap(values)
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
