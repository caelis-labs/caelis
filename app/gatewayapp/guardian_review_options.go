package gatewayapp

import (
	"encoding/json"

	"github.com/OnslaughtSnail/caelis/ports/approval"
	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/model"
)

func guardianApprovalOptionsJSON(payload *gateway.ApprovalPayload) (string, bool, error) {
	if payload == nil {
		return "", false, nil
	}
	options := approval.NormalizeOptions(payload.Options)
	if len(options) == 0 {
		return "", false, nil
	}
	raw, err := json.MarshalIndent(options, "", "  ")
	if err != nil {
		return "", false, err
	}
	return string(raw), true, nil
}

func guardianOutputSpec(payload *gateway.ApprovalPayload) *model.OutputSpec {
	properties := map[string]any{
		"risk_level": map[string]any{
			"type": "string",
			"enum": []any{"low", "medium", "high", "critical"},
		},
		"user_authorization": map[string]any{
			"type": "string",
			"enum": []any{"unknown", "low", "medium", "high"},
		},
		"outcome": map[string]any{
			"type": "string",
			"enum": []any{"allow", "deny"},
		},
		"rationale": map[string]any{"type": "string"},
	}
	if payload != nil {
		if optionIDs := approval.OptionIDs(payload.Options); len(optionIDs) > 0 {
			properties["option_id"] = map[string]any{
				"type": "string",
				"enum": stringsToAny(optionIDs),
			}
		}
	}
	return &model.OutputSpec{
		Mode: model.OutputModeSchema,
		JSONSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           properties,
			"required":             []any{"outcome"},
		},
		MaxOutputTokens: guardianMaxOutputTokens,
	}
}

func stringsToAny(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}
