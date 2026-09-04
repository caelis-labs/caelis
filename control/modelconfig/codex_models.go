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
// Capability source (bundled snapshot e8b65624e073, observed on 2026-09-04):
// https://github.com/openai/codex/blob/main/codex-rs/models-manager/models.json
// https://github.com/openai/codex/blob/main/codex-rs/protocol/src/openai_models.rs
//
// Fallback selection offers the maintained current models and omits superseded
// entries. Omitted entries retain complete defaults for typed and persisted
// configurations. Selectability does not imply account entitlement.
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
	{name: "gpt-5.6-sol", contextWindowTokens: codexOAuthEffectiveContextWindowTokens, defaultReasoningEffort: "low", reasoningLevels: []string{"low", "medium", "high", "xhigh", "max", "ultra"}, imageInput: true, fallbackSelectable: true},
	{name: "gpt-5.6-terra", contextWindowTokens: codexOAuthEffectiveContextWindowTokens, defaultReasoningEffort: "medium", reasoningLevels: []string{"low", "medium", "high", "xhigh", "max", "ultra"}, imageInput: true, fallbackSelectable: true},
	{name: "gpt-5.6-luna", contextWindowTokens: codexOAuthEffectiveContextWindowTokens, defaultReasoningEffort: "medium", reasoningLevels: []string{"low", "medium", "high", "xhigh", "max"}, imageInput: true, fallbackSelectable: true},
	{name: "gpt-5.5", contextWindowTokens: 272000, defaultReasoningEffort: "medium", reasoningLevels: []string{"low", "medium", "high", "xhigh"}, imageInput: true, fallbackSelectable: true},
	{name: "gpt-5.4", contextWindowTokens: 272000, defaultReasoningEffort: "medium", reasoningLevels: []string{"low", "medium", "high", "xhigh"}, imageInput: true},
	{name: "gpt-5.4-mini", contextWindowTokens: 272000, defaultReasoningEffort: "medium", reasoningLevels: []string{"low", "medium", "high", "xhigh"}, imageInput: true},
	{name: "gpt-5.3-codex-spark", contextWindowTokens: 128000, defaultReasoningEffort: "high", reasoningLevels: []string{"low", "medium", "high", "xhigh"}, imageInput: true},
	// Retain defaults so existing configurations can still be loaded without
	// advertising models that are hidden or absent from the bundled catalog.
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

func isCodexOAuthModel(name string) bool {
	_, ok := codexOAuthModelDefaults(name)
	return ok
}
