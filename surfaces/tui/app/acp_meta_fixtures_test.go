package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/internal/jsonvalue"
	"github.com/caelis-labs/caelis/surfaces/internal/transcript"
)

type tuiTestMetaCodec struct {
	Root                      string
	Runtime                   string
	RuntimeTool               string
	RuntimeToolName           string
	RuntimeTask               string
	RuntimeTaskTerminalID     string
	RuntimeOutputCursor       string
	RuntimeOutputStart        string
	RuntimeOutputDelta        string
	RuntimeStream             string
	RuntimeStreamMode         string
	RuntimeStreamParentCallID string
	RuntimeStreamParentTool   string
}

var testMeta = tuiTestMetaCodec{
	Root:                      "caelis",
	Runtime:                   "runtime",
	RuntimeTool:               "tool",
	RuntimeToolName:           "name",
	RuntimeTask:               "task",
	RuntimeTaskTerminalID:     "terminal_id",
	RuntimeOutputCursor:       "output_cursor",
	RuntimeOutputStart:        "output_start_cursor",
	RuntimeOutputDelta:        "output_delta",
	RuntimeStream:             "stream",
	RuntimeStreamMode:         "mode",
	RuntimeStreamParentCallID: "parent_call_id",
	RuntimeStreamParentTool:   "parent_tool",
}

func (tuiTestMetaCodec) WithRuntimeSection(meta map[string]any, section string, values map[string]any) map[string]any {
	if section == "" || len(values) == 0 {
		return jsonvalue.CloneMap(meta)
	}
	out := jsonvalue.CloneMap(meta)
	if out == nil {
		out = map[string]any{}
	}
	caelis, _ := out["caelis"].(map[string]any)
	caelis = jsonvalue.CloneMap(caelis)
	if caelis == nil {
		caelis = map[string]any{}
	}
	caelis["version"] = 1
	runtime, _ := caelis["runtime"].(map[string]any)
	runtime = jsonvalue.CloneMap(runtime)
	if runtime == nil {
		runtime = map[string]any{}
	}
	sectionMeta, _ := runtime[section].(map[string]any)
	sectionMeta = jsonvalue.CloneMap(sectionMeta)
	if sectionMeta == nil {
		sectionMeta = map[string]any{}
	}
	for key, value := range values {
		sectionMeta[key] = jsonvalue.Clone(value)
	}
	runtime[section] = sectionMeta
	caelis["runtime"] = runtime
	out["caelis"] = caelis
	return out
}

func (codec tuiTestMetaCodec) WithCompactRuntimeSection(meta map[string]any, section string, values map[string]any) map[string]any {
	compact := map[string]any{}
	for key, value := range values {
		if text, ok := value.(string); ok {
			if text = strings.TrimSpace(text); text != "" {
				compact[key] = text
			}
			continue
		}
		if value != nil {
			compact[key] = jsonvalue.Clone(value)
		}
	}
	return codec.WithRuntimeSection(meta, section, compact)
}

func (tuiTestMetaCodec) WithTerminalInfo(meta map[string]any, terminalID string) map[string]any {
	return testWithTopLevelMeta(meta, "terminal_info", terminalID, "")
}

func (tuiTestMetaCodec) WithTerminalOutput(meta map[string]any, terminalID, data string) map[string]any {
	return transcript.WithTerminalOutput(meta, terminalID, data)
}

func (tuiTestMetaCodec) WithTerminalExit(meta map[string]any, terminalID string, exitCode *int, signal *string) map[string]any {
	out := testWithTopLevelMeta(meta, "terminal_exit", terminalID, "")
	values, _ := out["terminal_exit"].(map[string]any)
	values["signal"] = nil
	if exitCode != nil {
		values["exit_code"] = *exitCode
	}
	if signal != nil {
		values["signal"] = *signal
	}
	return out
}

func (tuiTestMetaCodec) TerminalInfo(meta map[string]any) (transcript.TerminalInfoMeta, bool) {
	return transcript.ReadTerminalInfo(meta)
}

func (tuiTestMetaCodec) TerminalOutput(meta map[string]any) (transcript.TerminalOutputMeta, bool) {
	return transcript.ReadTerminalOutput(meta)
}

func (tuiTestMetaCodec) TerminalExit(meta map[string]any) (transcript.TerminalExitMeta, bool) {
	return transcript.ReadTerminalExit(meta)
}

func testWithTopLevelMeta(meta map[string]any, key, terminalID, data string) map[string]any {
	terminalID = strings.TrimSpace(terminalID)
	if terminalID == "" {
		return jsonvalue.CloneMap(meta)
	}
	out := jsonvalue.CloneMap(meta)
	if out == nil {
		out = map[string]any{}
	}
	values := map[string]any{"terminal_id": terminalID}
	if data != "" {
		values["data"] = data
	}
	out[key] = values
	return out
}
