package tool

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const (
	approxPromptRunesPerToken     = 4
	modelPromptToolOverheadTokens = 24
)

// EstimateModelPromptTokens returns the approximate model-context cost of the
// currently visible tool specifications.
func EstimateModelPromptTokens(tools []Tool) int {
	specs := ModelSpecs(tools)
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
