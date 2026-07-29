package tuiapp

import (
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
)

func commandDisplayArguments(name string, kind string, raw map[string]any) (string, string, bool) {
	return commandTextDisplayArguments(name, kind, terminalCommandDisplay(raw))
}

func commandTextDisplayArguments(name string, kind string, command string) (string, string, bool) {
	canonical := names.CanonicalOrSelf(name)
	if canonical == names.Spawn || canonical == names.Task || !display.IsTerminalPanelTool(name, kind) {
		return "", "", false
	}
	command = display.NormalizeDisplayArg(command)
	if command == "" {
		return "", "", false
	}
	preview, folded := longCommandDisplayPreview(command)
	if !folded {
		return preview, "", true
	}
	return preview, command, true
}

func longCommandDisplayPreview(command string) (string, bool) {
	command = display.NormalizeDisplayArg(command)
	if command == "" {
		return "", false
	}
	lines := strings.Split(command, "\n")
	if len(lines) == 1 && displayColumns(command) <= toolArgsPreviewWidth {
		return command, false
	}
	inline := strings.Join(strings.Fields(command), " ")
	if len(lines) == 1 {
		return truncateDisplayPreviewMiddle(inline, toolArgsPreviewWidth), true
	}
	suffix := fmt.Sprintf(" ... +%d lines", len(lines)-1)
	previewBudget := maxInt(16, toolArgsPreviewWidth-displayColumns(suffix))
	preview := truncateDisplayPreviewMiddle(inline, previewBudget)
	return preview + suffix, true
}
