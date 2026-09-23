package modelconfig

import (
	"strings"

	"github.com/caelis-labs/caelis/control/modelcatalog"
)

const codexOAuthDefaultMaxOutputTokens = 32768
const codexOAuthEffectiveContextWindowTokens = 258400

// codexOAuthModelSpec is Control's maintained subscription-model metadata.
// Availability and capabilities differ from the OpenAI API catalog, so these
// entries must not be inferred from provider=openai metadata.
//
// Capability source for selectable models (bundled snapshot 24462234b2ae):
// https://github.com/openai/codex/blob/24462234b2ae/codex-rs/models-manager/models.json
// https://github.com/openai/codex/blob/24462234b2ae/codex-rs/protocol/src/openai_models.rs
//
// Current models use the catalog's 272000-token window at the protocol's default
// 95% utilization. Selectability does not imply account entitlement.
//
// Control retains unselectable entries for saved profiles whose capabilities,
// including image input, are derived rather than persisted. Remove these entries
// only with a configuration migration that preserves those capabilities or
// explicitly retires the affected profiles before validation.
type codexOAuthModelSpec struct {
	name                   string
	contextWindowTokens    int
	defaultReasoningEffort string
	reasoningLevels        []string
	imageInput             bool
	fallbackSelectable     bool
}

var codexOAuthModelSpecs = []codexOAuthModelSpec{
	{name: "gpt-6-astra", contextWindowTokens: codexOAuthEffectiveContextWindowTokens, defaultReasoningEffort: "low", reasoningLevels: []string{"low", "medium", "high", "xhigh", "max", "ultra"}, imageInput: true, fallbackSelectable: true},
	{name: "gpt-6-sol", contextWindowTokens: codexOAuthEffectiveContextWindowTokens, defaultReasoningEffort: "medium", reasoningLevels: []string{"low", "medium", "high", "xhigh", "max", "ultra"}, imageInput: true, fallbackSelectable: true},
	{name: "gpt-6-luna", contextWindowTokens: codexOAuthEffectiveContextWindowTokens, defaultReasoningEffort: "medium", reasoningLevels: []string{"low", "medium", "high", "xhigh", "max"}, imageInput: true, fallbackSelectable: true},
	{name: "gpt-5.6-sol", contextWindowTokens: codexOAuthEffectiveContextWindowTokens, defaultReasoningEffort: "low", reasoningLevels: []string{"low", "medium", "high", "xhigh", "max", "ultra"}, imageInput: true},
	{name: "gpt-5.6-terra", contextWindowTokens: codexOAuthEffectiveContextWindowTokens, defaultReasoningEffort: "medium", reasoningLevels: []string{"low", "medium", "high", "xhigh", "max", "ultra"}, imageInput: true},
	{name: "gpt-5.6-luna", contextWindowTokens: codexOAuthEffectiveContextWindowTokens, defaultReasoningEffort: "medium", reasoningLevels: []string{"low", "medium", "high", "xhigh", "max"}, imageInput: true},
	{name: "gpt-5.5", contextWindowTokens: 272000, defaultReasoningEffort: "medium", reasoningLevels: []string{"low", "medium", "high", "xhigh"}, imageInput: true},
	{name: "gpt-5.4", contextWindowTokens: 272000, defaultReasoningEffort: "medium", reasoningLevels: []string{"low", "medium", "high", "xhigh"}, imageInput: true},
	{name: "gpt-5.4-mini", contextWindowTokens: 272000, defaultReasoningEffort: "medium", reasoningLevels: []string{"low", "medium", "high", "xhigh"}, imageInput: true},
	{name: "gpt-5.3-codex-spark", contextWindowTokens: 128000, defaultReasoningEffort: "high", reasoningLevels: []string{"low", "medium", "high", "xhigh"}, imageInput: true},
	{name: "gpt-5.2", contextWindowTokens: 272000, defaultReasoningEffort: "medium", reasoningLevels: []string{"low", "medium", "high", "xhigh"}, imageInput: true},
}

func codexOAuthSelectableModels() []string {
	models := make([]string, 0, len(codexOAuthModelSpecs))
	for _, spec := range codexOAuthModelSpecs {
		if !spec.fallbackSelectable {
			continue
		}
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
