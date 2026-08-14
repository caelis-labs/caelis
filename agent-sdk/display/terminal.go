package display

import (
	"strings"

	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
)

func IsTerminalPanelTool(name string, kind string) bool {
	if terminal, ok := terminalPanelToolName(name); ok {
		return terminal
	}
	return strings.EqualFold(strings.TrimSpace(kind), ToolKindExecute)
}

func DisplayTerminalID(toolCallID string, name string) (string, bool) {
	terminal, known := terminalPanelToolName(name)
	if !known && strings.EqualFold(strings.TrimSpace(name), ToolKindExecute) {
		terminal = true
	}
	if terminal {
		if id := strings.TrimSpace(toolCallID); id != "" {
			return id, true
		}
	}
	return "", false
}

func terminalPanelToolName(name string) (bool, bool) {
	info, ok := names.Lookup(name)
	if !ok || !info.TerminalKnown {
		return false, false
	}
	return info.TerminalPanel, true
}

func DisplayTerminalInitialOutput(name string, args map[string]any) string {
	switch SemanticToolName(name, "") {
	case names.Spawn:
		agent := strings.TrimSpace(MapString(args, "agent"))
		prompt := strings.TrimSpace(MapString(args, "prompt"))
		switch {
		case agent != "" && prompt != "":
			return names.Spawn + " agent=" + agent + "\n" + prompt + "\n"
		case agent != "":
			return names.Spawn + " agent=" + agent + "\n"
		case prompt != "":
			return names.Spawn + "\n" + prompt + "\n"
		}
	}
	return ""
}
