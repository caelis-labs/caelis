package tool

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

const (
	approxPromptRunesPerToken     = 4
	modelPromptToolOverheadTokens = 24
)

// EstimateModelPromptTokens returns the approximate model-context cost of the
// supplied tool specifications.
func EstimateModelPromptTokens(tools []Tool) int {
	return EstimateModelSpecsPromptTokens(ModelSpecs(tools))
}

// EstimateDefinitionPromptTokens returns the approximate model-context cost of
// one projected tool definition.
func EstimateDefinitionPromptTokens(def Definition) int {
	return EstimateModelSpecsPromptTokens(modelSpecsFromDefinitions([]Definition{CloneDefinition(def)}))
}

// EstimateModelSpecsPromptTokens returns the approximate model-context cost of
// already-projected tool specifications.
func EstimateModelSpecsPromptTokens(specs []model.ToolSpec) int {
	if len(specs) == 0 {
		return 0
	}
	raw, err := json.Marshal(specs)
	if err != nil {
		return len(specs) * 64
	}
	return estimateModelPromptTextTokens(string(raw)) + len(specs)*modelPromptToolOverheadTokens
}

func estimateModelPromptTextTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	runes := utf8.RuneCountInString(text)
	tokens := runes / approxPromptRunesPerToken
	if runes%approxPromptRunesPerToken != 0 {
		tokens++
	}
	return max(tokens, 1)
}
