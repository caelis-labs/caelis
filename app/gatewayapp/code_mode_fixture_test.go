package gatewayapp

import (
	"encoding/json"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// CodeModeFixtureArgs encodes the body of a scripted js_exec call.
func CodeModeFixtureArgs(code string) string {
	encoded, err := json.Marshal(map[string]string{"code": code})
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// CodeModeFixtureSelectedTools keeps the opt-in external-package model harness
// on the product's catalog policy without exporting a production API.
func CodeModeFixtureSelectedTools(candidates []tool.Tool) []string {
	return selectedCodeModeTools(candidates)
}
