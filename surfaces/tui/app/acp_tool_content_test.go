package tuiapp

import "github.com/caelis-labs/caelis/agent-sdk/session"

func testToolContent(text string) []session.ProtocolToolCallContent {
	return []session.ProtocolToolCallContent{{
		Type:    "content",
		Content: session.ProtocolTextContent(text),
	}}
}

func testTerminalContent(text string) []session.ProtocolToolCallContent {
	return []session.ProtocolToolCallContent{{
		Type:    "terminal",
		Content: session.ProtocolTextContent(text),
	}}
}

func testTerminalContentWithID(text string, terminalID string) []session.ProtocolToolCallContent {
	return []session.ProtocolToolCallContent{{
		Type:       "terminal",
		Content:    session.ProtocolTextContent(text),
		TerminalID: terminalID,
	}}
}

func testRuntimeToolMeta(values map[string]any) map[string]any {
	return map[string]any{
		"caelis": map[string]any{
			"runtime": map[string]any{
				"tool": values,
			},
		},
	}
}

func toolResultLabel(name string, input map[string]any) string {
	switch name {
	case "READ":
		return baseNameFromInput(input)
	case "LIST":
		return baseNameFromInput(input)
	case "RG", "SEARCH", "FIND":
		if pattern, _ := input["pattern"].(string); pattern != "" {
			return `"` + pattern + `"`
		}
		if query, _ := input["query"].(string); query != "" {
			return `"` + query + `"`
		}
	case "PATCH", "WRITE":
		return baseNameFromInput(input)
	}
	return name
}

func baseNameFromInput(input map[string]any) string {
	path, _ := input["path"].(string)
	if path == "" {
		return ""
	}
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}
