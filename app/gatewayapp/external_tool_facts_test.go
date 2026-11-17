package gatewayapp_test

import (
	"encoding/json"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func toolResultJSON(value *tool.Result) []byte { raw, _ := json.Marshal(value); return raw }
func factJSON(value any) []byte                { raw, _ := json.Marshal(value); return raw }
