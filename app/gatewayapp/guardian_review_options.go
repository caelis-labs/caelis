package gatewayapp

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/internal/kernel"
)

const (
	guardianMaxApprovalOptions       = 32
	guardianMaxApprovalOptionIDRunes = 128
	guardianMaxApprovalOptionRunes   = 240
)

func guardianApprovalOptionsJSON(payload *kernel.ApprovalPayload) (string, bool, bool, error) {
	if payload == nil {
		return "", false, false, nil
	}
	options := approval.NormalizeOptions(payload.Options)
	if len(options) == 0 {
		return "", false, false, nil
	}
	if err := approval.ValidateStrictOptions(options); err != nil {
		return "", false, false, err
	}
	if len(options) > guardianMaxApprovalOptions {
		return `[{"error":"approval options exceeded Guardian structural limits"}]`, true, true, nil
	}
	for _, option := range options {
		if utf8.RuneCountInString(strings.TrimSpace(option.ID)) > guardianMaxApprovalOptionIDRunes ||
			utf8.RuneCountInString(strings.TrimSpace(option.Name)) > guardianMaxApprovalOptionRunes ||
			utf8.RuneCountInString(strings.TrimSpace(option.Kind)) > guardianMaxApprovalOptionRunes {
			return `[{"error":"approval options exceeded Guardian structural limits"}]`, true, true, nil
		}
	}
	raw, err := json.MarshalIndent(options, "", "  ")
	if err != nil {
		return "", false, false, err
	}
	return string(raw), true, false, nil
}

func guardianOutputSpec(payload *kernel.ApprovalPayload) (*model.OutputSpec, error) {
	if payload == nil || len(payload.Options) == 0 {
		return nil, fmt.Errorf("guardian requires normalized approval options")
	}
	if err := approval.ValidateStrictOptions(payload.Options); err != nil {
		return nil, err
	}
	return &model.OutputSpec{Mode: model.OutputModeSchema, JSONSchema: map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{"option_id": map[string]any{"type": "string"}, "rationale": map[string]any{"type": "string"}},
		"required":   []any{"option_id"},
	}}, nil
}

// guardianOutputSpecForModel preserves Guardian's structured response contract
// without requiring native schema output from providers such as Codex OAuth.
// The fixed Guardian instructions and parser still enforce the JSON shape when
// the model can only return plain text.
func guardianOutputSpecForModel(llm model.LLM, payload *kernel.ApprovalPayload) (*model.OutputSpec, error) {
	output, err := guardianOutputSpec(payload)
	if err != nil {
		return nil, err
	}
	capabilities, declared := model.CapabilitiesOf(llm)
	if declared && capabilities.StructuredOutput {
		return output, nil
	}
	output.Mode = model.OutputModeText
	output.JSONSchema = nil
	return output, nil
}
