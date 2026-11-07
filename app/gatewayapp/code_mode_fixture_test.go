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
			source = "return await tools.describe(" + string(value) + ");"
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
		source = "await tools.describe(" + string(nameJSON) + ");const r=await tools.call(" + string(nameJSON) + "," + string(input.Args) + ");for(const p of r.parts){if(p.handle)await output.image(p.handle);else if(p.text)output.text(p.text);else if(p.json)output.text(p.json);}return {outcome:r.outcome,is_error:!!r.is_error};"
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
	if strings.Contains(input.Code, "tools.search(") {
		return tool.ToolSearchToolName
	}
	return tool.MCPCallToolName
}
