package codefreecaps

import "strings"

const Provider = "codefree"

const (
	UnknownContextWindowTokens = 128000
	UnknownMaxOutputTokens     = 8000
)

type Model struct {
	ID                     string
	ContextWindowTokens    int
	MaxOutputTokens        int
	DefaultMaxOutputTokens int
	SupportsToolCalls      bool
	SupportsJSONOutput     bool
	SupportsImages         bool
}

var Models = []Model{
	{
		ID:                     "GLM-4.7",
		ContextWindowTokens:    80000,
		MaxOutputTokens:        8000,
		DefaultMaxOutputTokens: 8000,
		SupportsToolCalls:      true,
		SupportsJSONOutput:     true,
	},
	{
		ID:                     "Qwen3.5-122B-A10B",
		ContextWindowTokens:    112000,
		MaxOutputTokens:        16000,
		DefaultMaxOutputTokens: 16000,
		SupportsToolCalls:      true,
		SupportsJSONOutput:     true,
		SupportsImages:         true,
	},
	{
		ID:                     "DeepSeek-V4-Flash-ctyun-oc",
		ContextWindowTokens:    112000,
		MaxOutputTokens:        16000,
		DefaultMaxOutputTokens: 16000,
		SupportsToolCalls:      true,
		SupportsJSONOutput:     true,
	},
	{
		ID:                     "GLM-5.1-ctyun-oc",
		ContextWindowTokens:    112000,
		MaxOutputTokens:        16000,
		DefaultMaxOutputTokens: 16000,
		SupportsToolCalls:      true,
		SupportsJSONOutput:     true,
	},
	{
		ID:                     "GLM-5-ctyun-oc",
		ContextWindowTokens:    112000,
		MaxOutputTokens:        16000,
		DefaultMaxOutputTokens: 16000,
		SupportsToolCalls:      true,
		SupportsJSONOutput:     true,
	},
	{
		ID:                     "GLM-5.1",
		ContextWindowTokens:    112000,
		MaxOutputTokens:        16000,
		DefaultMaxOutputTokens: 16000,
		SupportsToolCalls:      true,
		SupportsJSONOutput:     true,
	},
}

func Lookup(modelName string) (Model, bool) {
	modelName = strings.ToLower(strings.TrimSpace(modelName))
	if modelName == "" {
		return Model{}, false
	}
	var best Model
	bestLen := 0
	for _, model := range Models {
		id := strings.ToLower(model.ID)
		if modelName == id {
			return model, true
		}
		if strings.HasPrefix(modelName, id) && len(id) > bestLen {
			best = model
			bestLen = len(id)
		}
	}
	if bestLen > 0 {
		return best, true
	}
	return Model{}, false
}

func SupportsImageInputs(modelName string) bool {
	model, ok := Lookup(modelName)
	return ok && model.SupportsImages
}
