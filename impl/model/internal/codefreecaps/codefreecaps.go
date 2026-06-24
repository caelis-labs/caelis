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
		ContextWindowTokens:    88000,
		MaxOutputTokens:        8000,
		DefaultMaxOutputTokens: 8000,
		SupportsToolCalls:      true,
		SupportsJSONOutput:     true,
	},
	{
		ID:                     "Qwen3.5-122B-A10B",
		ContextWindowTokens:    128000,
		MaxOutputTokens:        16000,
		DefaultMaxOutputTokens: 16000,
		SupportsToolCalls:      true,
		SupportsJSONOutput:     true,
		SupportsImages:         true,
	},
	{
		ID:                     "GLM-5.1",
		ContextWindowTokens:    128000,
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
	for _, model := range Models {
		id := strings.ToLower(model.ID)
		if modelName == id || strings.HasPrefix(modelName, id) {
			return model, true
		}
	}
	return Model{}, false
}

func SupportsImageInputs(modelName string) bool {
	model, ok := Lookup(modelName)
	return ok && model.SupportsImages
}
