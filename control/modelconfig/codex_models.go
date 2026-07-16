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
// Source (embedded snapshot cbc83d96 plus the account-scoped /models catalog
// observed on 2026-07-16):
// https://github.com/openai/codex/blob/main/codex-rs/models-manager/models.json
// https://github.com/openai/codex/blob/main/codex-rs/protocol/src/openai_models.rs
//
// ChatGPT OAuth catalogs filter by account and visibility, not
// supported_in_api. The latter only filters API-key catalogs, so a Pro account
// may legitimately receive gpt-5.3-codex-spark with supported_in_api=false.
type codexOAuthModelSpec struct {
	name                   string
	contextWindowTokens    int
	defaultReasoningEffort string
	reasoningLevels        []string
	fallbackSelectable     bool
}

var codexOAuthModelSpecs = []codexOAuthModelSpec{
	{name: "gpt-5.6-sol", contextWindowTokens: codexOAuthEffectiveContextWindowTokens, defaultReasoningEffort: "low", reasoningLevels: []string{"low", "medium", "high", "xhigh", "max", "ultra"}, fallbackSelectable: true},
	{name: "gpt-5.6-terra", contextWindowTokens: codexOAuthEffectiveContextWindowTokens, defaultReasoningEffort: "medium", reasoningLevels: []string{"low", "medium", "high", "xhigh", "max", "ultra"}, fallbackSelectable: true},
	{name: "gpt-5.6-luna", contextWindowTokens: codexOAuthEffectiveContextWindowTokens, defaultReasoningEffort: "medium", reasoningLevels: []string{"low", "medium", "high", "xhigh", "max"}, fallbackSelectable: true},
	{name: "gpt-5.5", contextWindowTokens: 272000, defaultReasoningEffort: "medium", reasoningLevels: []string{"low", "medium", "high", "xhigh"}, fallbackSelectable: true},
	{name: "gpt-5.4", contextWindowTokens: 272000, defaultReasoningEffort: "medium", reasoningLevels: []string{"low", "medium", "high", "xhigh"}, fallbackSelectable: true},
	{name: "gpt-5.4-mini", contextWindowTokens: 272000, defaultReasoningEffort: "medium", reasoningLevels: []string{"low", "medium", "high", "xhigh"}, fallbackSelectable: true},
	{name: "gpt-5.3-codex-spark", contextWindowTokens: 128000, defaultReasoningEffort: "high", reasoningLevels: []string{"low", "medium", "high", "xhigh"}, fallbackSelectable: true},
	// Retain defaults so an existing configuration can still be loaded, but do
	// not advertise this deprecated ChatGPT-sign-in model in either picker path.
	{name: "gpt-5.2", contextWindowTokens: 272000, defaultReasoningEffort: "medium", reasoningLevels: []string{"low", "medium", "high", "xhigh"}},
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

func filterCodexOAuthSelectableModels(models []string) []string {
	filtered := make([]string, 0, len(models))
	for _, name := range models {
		name = strings.ToLower(strings.TrimSpace(name))
		if !isCodexOAuthModel(name) {
			continue
		}
		filtered = append(filtered, name)
	}
	return filtered
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
		}, true
	}
	return ModelDefaults{}, false
}

func isCodexOAuthModel(name string) bool {
	_, ok := codexOAuthModelDefaults(name)
	return ok
}
