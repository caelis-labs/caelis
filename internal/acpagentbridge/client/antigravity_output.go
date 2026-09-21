package client

import (
	"strings"

	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpmeta"
	"github.com/caelis-labs/caelis/internal/jsonvalue"
)

// Antigravity command completions can put their entire result in rawOutput
// without standard content. Project only this complete, typed result shape;
// rawOutput stays unchanged and the text is a replaceable snapshot, not a
// terminal delta. Remove this compatibility path when supported runtimes and
// retained traces all supply standard content for these results.
func antigravityCommandContent(content []ToolCallContent, raw any, status, kind string, meta map[string]any) []ToolCallContent {
	if content != nil || (status != "completed" && status != "failed") || (kind != "" && kind != toolKindExecute) {
		return content
	}
	if _, ok := acpmeta.ReadTerminalInfo(meta); ok {
		return content
	}
	if _, ok := acpmeta.ReadTerminalOutput(meta); ok {
		return content
	}
	if _, ok := acpmeta.ReadTerminalExit(meta); ok {
		return content
	}
	output, ok := raw.(map[string]any)
	if !ok {
		return content
	}
	text, hasText := output["combinedOutput"].(string)
	command, hasCommand := output["commandLine"].(string)
	directory, hasDirectory := output["workingDir"].(string)
	_, hasExit := jsonvalue.Int64At(output, "exitCode")
	if !hasText || text == "" || !hasCommand || strings.TrimSpace(command) == "" || !hasDirectory || strings.TrimSpace(directory) == "" || !hasExit {
		return content
	}
	return []ToolCallContent{{Type: "content", Content: map[string]any{"type": "text", "text": text}}}
}
