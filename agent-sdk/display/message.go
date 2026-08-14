package display

import "strings"

// AgentMessageFullDisplayArgs returns the semantic recipient and body for a
// SendMessage invocation. It deliberately preserves the complete message so
// presentation surfaces can own preview folding and expansion.
func AgentMessageFullDisplayArgs(args map[string]any) string {
	target := AgentMessageTarget(MapString(args, "to"))
	message := NormalizeDisplayArg(MapString(args, "message"))
	switch {
	case target != "" && message != "":
		return "to " + target + ": " + message
	case target != "":
		return "to " + target
	case message != "":
		return message
	default:
		return ""
	}
}

// AgentMessageTarget normalizes one model-facing target into the same mention
// form used by Agent actors in transcripts.
func AgentMessageTarget(target string) string {
	target = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(target), "@"))
	if target == "" {
		return ""
	}
	return "@" + target
}
