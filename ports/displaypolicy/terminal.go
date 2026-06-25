package displaypolicy

import "strings"

func DisplayTerminalID(toolCallID string, name string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "RUN_COMMAND", "SPAWN":
		if id := strings.TrimSpace(toolCallID); id != "" {
			return id, true
		}
	}
	return "", false
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
