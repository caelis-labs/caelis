package gatewayapp

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

// CodeModeFixtureCall migrates deterministic provider fixtures to the product's
// real JS interface. It neither executes tools nor replaces live-model evidence.
func CodeModeFixtureCall(enabled bool, name, args string) (string, string) {
	if !enabled {
		return name, args
	}
	var source string
	switch name {
	case tool.ToolSearchToolName:
		var input struct {
			Query string `json:"query"`
			Name  string `json:"name"`
		}
		_ = json.Unmarshal([]byte(args), &input)
		if input.Name != "" {
			value, _ := json.Marshal(input.Name)
			source = "return await tools.search(" + string(value) + ");"
		} else {
			value, _ := json.Marshal(input.Query)
			source = "return await tools.search(" + string(value) + ");"
		}
	case tool.MCPCallToolName:
		var input struct {
			Name string          `json:"tool_name"`
			Args json.RawMessage `json:"arguments"`
		}
		_ = json.Unmarshal([]byte(args), &input)
		nameJSON, _ := json.Marshal(input.Name)
		if len(input.Args) == 0 {
			input.Args = []byte(`{}`)
		}
		source = "await tools.search(" + string(nameJSON) + ");return await tools.call(" + string(nameJSON) + "," + string(input.Args) + ");"
	default:
		return name, args
	}
	encoded, _ := json.Marshal(map[string]any{"code": source})
	return codemode.ToolName, string(encoded)
}

// CodeModeFixturePhase preserves a scripted fixture's phase machine while its
// actual response and execution use js_exec rather than hidden legacy entries.
func CodeModeFixturePhase(name, args string) string {
	if name != codemode.ToolName {
		return name
	}
	var input struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal([]byte(args), &input)
	if strings.Contains(input.Code, "tools.call(") {
		return tool.MCPCallToolName
	}
	if strings.Contains(input.Code, "tools.search(") {
		return tool.ToolSearchToolName
	}
	return tool.MCPCallToolName
}

// CodeModeFixtureSelectedTools keeps the opt-in external-package model harness
// on the product's catalog policy without exporting a production API.
func CodeModeFixtureSelectedTools(candidates []tool.Tool) []string {
	return selectedCodeModeTools(candidates)
}
