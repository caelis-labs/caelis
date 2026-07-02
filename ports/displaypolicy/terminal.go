package displaypolicy

import "strings"

var terminalPanelToolNames = map[string]bool{
	"RUN_COMMAND": true,
	"SPAWN":       true,
	"TASK":        false,
}

func IsTerminalPanelTool(name string, kind string) bool {
	if terminal, ok := terminalPanelToolName(name); ok {
		return terminal
	}
	return strings.EqualFold(strings.TrimSpace(kind), ToolKindExecute)
}

func DisplayTerminalID(toolCallID string, name string) (string, bool) {
	if terminal, ok := terminalPanelToolName(name); ok && terminal {
		if id := strings.TrimSpace(toolCallID); id != "" {
			return id, true
		}
	}
	return "", false
}

func terminalPanelToolName(name string) (bool, bool) {
	terminal, ok := terminalPanelToolNames[strings.ToUpper(strings.TrimSpace(name))]
	return terminal, ok
}

func DisplayTerminalInitialOutput(name string, args map[string]any) string {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "SPAWN":
		agent := strings.TrimSpace(MapString(args, "agent"))
		prompt := strings.TrimSpace(MapString(args, "prompt"))
		switch {
		case agent != "" && prompt != "":
			return "SPAWN agent=" + agent + "\n" + prompt + "\n"
		case agent != "":
			return "SPAWN agent=" + agent + "\n"
		case prompt != "":
			return "SPAWN\n" + prompt + "\n"
		}
	}
	return ""
}
