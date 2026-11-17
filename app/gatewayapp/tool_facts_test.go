package gatewayapp

import (
	"encoding/json"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func toolResultJSON(result *tool.Result) []byte { raw, _ := json.Marshal(result); return raw }
