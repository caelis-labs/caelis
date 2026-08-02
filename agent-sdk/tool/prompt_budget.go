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
// default visible tool specifications when no concrete model is available.
// Capability-gated tools are therefore included conservatively in this budget.
func EstimateModelPromptTokens(tools []Tool) int {
	return EstimateModelSpecsPromptTokens(ModelSpecs(tools))
}

// EstimateDefinitionPromptTokens returns the approximate model-context cost of
// one projected tool definition.
func EstimateDefinitionPromptTokens(def Definition) int {
	return EstimateModelSpecsPromptTokens(modelSpecsFromDefinitions([]Definition{CloneDefinition(def)}))
}

// EstimateToolSearchResultPromptTokens returns the approximate model-context
// cost of the complete serialized ToolSearch result, including projected source
// metadata. It deliberately uses serialized bytes so the admission budget is
// no weaker than the shared tool-result truncation budget for non-ASCII text.
func EstimateToolSearchResultPromptTokens(result ToolSearchResult) int {
	raw, err := json.Marshal(result)
	if err != nil {
		return MaxToolSearchResultPromptTokens + 1
	}
	if len(raw) == 0 {
		return 0
	}
	tokens := len(raw) / approxPromptRunesPerToken
	if len(raw)%approxPromptRunesPerToken != 0 {
		tokens++
	}
	return tokens
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
