package tuiapp

import (
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
)

func commandDisplayArguments(name string, kind string, raw map[string]any) (string, string, bool) {
	return commandTextDisplayArguments(name, kind, terminalCommandDisplay(raw))
}

func commandTextDisplayArguments(name string, kind string, command string) (string, string, bool) {
	if name == surfaceToolSpawn || name == surfaceToolTask ||
		(!surfaceIsTerminalPanelTool(name) && !isExecuteToolKind(kind)) {
		return "", "", false
	}
	command = display.NormalizeDisplayArg(command)
	if command == "" {
		return "", "", false
	}
	preview, folded := longCommandDisplayPreview(command, toolArgsPreviewWidth)
	if !folded {
		return preview, "", true
	}
	return preview, command, true
}

func longCommandDisplayPreview(command string, budget int) (string, bool) {
	command = display.NormalizeDisplayArg(command)
	if command == "" {
		return "", false
	}
	lines := strings.Split(command, "\n")
	budget = maxInt(compactSingleLineMinBudget, budget)
	if len(lines) == 1 && displayColumns(command) <= budget {
		return command, false
	}
	inline := strings.Join(strings.Fields(command), " ")
	if len(lines) == 1 {
		return truncateDisplayPreviewMiddle(inline, budget), true
	}
	suffix := fmt.Sprintf(" ... +%d lines", len(lines)-1)
	previewBudget := maxInt(compactSingleLineMinBudget, budget-displayColumns(suffix))
	preview := truncateDisplayPreviewMiddle(inline, previewBudget)
	return preview + suffix, true
}
