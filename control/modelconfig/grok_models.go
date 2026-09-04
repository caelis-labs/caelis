package modelconfig

import (
	"strings"

	"github.com/caelis-labs/caelis/control/modelcatalog"
)

const (
	grokOAuthContextWindowTokens = 500000
	grokOAuthMaxOutputTokens     = 32768
)

// grokOAuthModelSpecs is the maintained fallback when xAI's account model
// directory is unavailable. xAI's official Grok Build catalog currently uses
// grok-4.6 over the Responses API; older defaults remain available only for
// persisted or explicitly typed configurations.
var grokOAuthModelSpecs = []struct {
	name                   string
	defaultReasoningEffort string
	reasoningLevels        []string
	imageInput             bool
	fallbackSelectable     bool
}{
	{name: "grok-4.6", defaultReasoningEffort: "high", reasoningLevels: []string{"low", "medium", "high", "xhigh"}, imageInput: true, fallbackSelectable: true},
	{name: "grok-4.5", defaultReasoningEffort: "high", reasoningLevels: []string{"low", "medium", "high"}, imageInput: true},
}

func grokOAuthSelectableModels() []string {
	out := make([]string, 0, len(grokOAuthModelSpecs))
	for _, spec := range grokOAuthModelSpecs {
		if !spec.fallbackSelectable {
			continue
		}
		out = append(out, spec.name)
	}
	return out
}

func grokOAuthModelDefaults(name string) (ModelDefaults, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if strings.Contains(name, "non-reasoning") {
		return ModelDefaults{
			ContextWindowTokens: grokOAuthContextWindowTokens,
			MaxOutputTokens:     grokOAuthMaxOutputTokens,
			ReasoningMode:       modelcatalog.ReasoningModeNone,
			ImageInput:          boolPointer(true),
		}, true
	}
	for _, spec := range grokOAuthModelSpecs {
		if spec.name != name {
			continue
		}
		return ModelDefaults{
			ContextWindowTokens:    grokOAuthContextWindowTokens,
			MaxOutputTokens:        grokOAuthMaxOutputTokens,
			ReasoningLevels:        append([]string(nil), spec.reasoningLevels...),
			ReasoningMode:          modelcatalog.ReasoningModeEffort,
			DefaultReasoningEffort: spec.defaultReasoningEffort,
			ImageInput:             boolPointer(spec.imageInput),
		}, true
	}
	return ModelDefaults{}, false
}
