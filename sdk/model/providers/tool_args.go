package providers

import (
	"encoding/json"
	"strings"

	"github.com/OnslaughtSnail/caelis/sdk/model"
)

func toolArgsRaw(args map[string]any) string {
	if len(args) == 0 {
		return "{}"
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func toolArgsMap(raw string) map[string]any {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}
	}
	parsed, err := model.ParseToolCallArgs(raw)
	if err != nil {
		return map[string]any{}
	}
	return parsed
}
