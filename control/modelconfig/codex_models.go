package modelconfig

import (
	"strings"

	"github.com/caelis-labs/caelis/control/modelcatalog"
)

const codexOAuthDefaultMaxOutputTokens = 32768
const codexOAuthEffectiveContextWindowTokens = 258400

// codexOAuthModelSpec is Control's snapshot of the models exposed by the
// official Codex client's maintained model catalog. Codex subscription model
// availability and capabilities differ from the OpenAI API catalog, so these
// entries must not be inferred from provider=openai metadata.
//
// Capability source (bundled snapshot 24462234b2ae):
// https://github.com/openai/codex/blob/24462234b2ae/codex-rs/models-manager/models.json
// https://github.com/openai/codex/blob/24462234b2ae/codex-rs/protocol/src/openai_models.rs
//
// Only current recommended models are maintained here. The effective context
// window is the catalog's 272000 tokens at the protocol's default 95% utilization.
// Selectability does not imply account entitlement.
type codexOAuthModelSpec struct {
	name                   string
	contextWindowTokens    int
	defaultReasoningEffort string
	reasoningLevels        []string
	imageInput             bool
}

var codexOAuthModelSpecs = []codexOAuthModelSpec{
	{name: "gpt-6-astra", contextWindowTokens: codexOAuthEffectiveContextWindowTokens, defaultReasoningEffort: "low", reasoningLevels: []string{"low", "medium", "high", "xhigh", "max", "ultra"}, imageInput: true},
	{name: "gpt-6-sol", contextWindowTokens: codexOAuthEffectiveContextWindowTokens, defaultReasoningEffort: "medium", reasoningLevels: []string{"low", "medium", "high", "xhigh", "max", "ultra"}, imageInput: true},
	{name: "gpt-6-luna", contextWindowTokens: codexOAuthEffectiveContextWindowTokens, defaultReasoningEffort: "medium", reasoningLevels: []string{"low", "medium", "high", "xhigh", "max"}, imageInput: true},
}

func codexOAuthSelectableModels() []string {
	models := make([]string, 0, len(codexOAuthModelSpecs))
	for _, spec := range codexOAuthModelSpecs {
		models = append(models, spec.name)
	}
	return models
}

func codexOAuthModelDefaults(name string) (ModelDefaults, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, spec := range codexOAuthModelSpecs {
		if spec.name != name {
			continue
		}
		return ModelDefaults{
			ContextWindowTokens:    spec.contextWindowTokens,
			MaxOutputTokens:        codexOAuthDefaultMaxOutputTokens,
			ReasoningLevels:        append([]string(nil), spec.reasoningLevels...),
			ReasoningMode:          modelcatalog.ReasoningModeEffort,
			DefaultReasoningEffort: spec.defaultReasoningEffort,
			ImageInput:             boolPointer(spec.imageInput),
		}, true
	}
	return ModelDefaults{}, false
}
