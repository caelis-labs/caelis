package modelcatalog

// OllamaCloudProvider is the capability-catalog namespace for models called
// directly through https://ollama.com. It is not a user-visible provider.
const OllamaCloudProvider = "ollama-cloud"

type ollamaCloudModel struct {
	Name                   string
	ContextWindowTokens    int
	SupportsImages         bool
	ReasoningMode          string
	ReasoningEfforts       []string
	DefaultReasoningEffort string
}

// ollamaCloudModels is the curated frontier catalog shown for the direct
// Cloud API. The public /api/tags directory is broader and does not publish
// maximum output or per-model effort metadata, so it is not used as the
// selectable Cloud catalog.
var ollamaCloudModels = []ollamaCloudModel{
	{
		Name:                   "glm-5.2",
		ContextWindowTokens:    1000000,
		ReasoningMode:          ReasoningModeEffort,
		ReasoningEfforts:       []string{"high", "max"},
		DefaultReasoningEffort: "high",
	},
	{
		Name:                   "minimax-m3",
		ContextWindowTokens:    524288,
		SupportsImages:         true,
		ReasoningMode:          ReasoningModeToggle,
		DefaultReasoningEffort: "high",
	},
	{
		Name:                   "kimi-k3",
		ContextWindowTokens:    1000000,
		SupportsImages:         true,
		ReasoningMode:          ReasoningModeToggle,
		DefaultReasoningEffort: "high",
	},
	{
		Name:                   "deepseek-v4-flash",
		ContextWindowTokens:    1000000,
		ReasoningMode:          ReasoningModeToggle,
		ReasoningEfforts:       []string{"high", "max"},
		DefaultReasoningEffort: "high",
	},
	{
		Name:                   "deepseek-v4-pro",
		ContextWindowTokens:    1000000,
		ReasoningMode:          ReasoningModeToggle,
		ReasoningEfforts:       []string{"high", "max"},
		DefaultReasoningEffort: "high",
	},
}

// ListOllamaCloudModels returns direct-API model names in maintained product
// priority order.
func ListOllamaCloudModels() []string {
	out := make([]string, 0, len(ollamaCloudModels))
	for _, model := range ollamaCloudModels {
		out = append(out, model.Name)
	}
	return out
}

func ollamaCloudCatalogEntries() []catalogEntry {
	out := make([]catalogEntry, 0, len(ollamaCloudModels))
	for _, model := range ollamaCloudModels {
		out = append(out, catalogEntry{
			provider: OllamaCloudProvider,
			pattern:  model.Name,
			caps: ModelCapabilities{
				ContextWindowTokens: model.ContextWindowTokens,
				// Ollama does not publish a hard maximum output limit for these
				// Cloud models. Keep a conservative product default without
				// claiming a provider maximum.
				DefaultMaxOutputTokens: 32768,
				SupportsImages:         model.SupportsImages,
				SupportsToolCalls:      true,
				SupportsReasoning:      true,
				ReasoningMode:          model.ReasoningMode,
				ReasoningEfforts:       append([]string(nil), model.ReasoningEfforts...),
				DefaultReasoningEffort: model.DefaultReasoningEffort,
				SupportsJSONOutput:     true,
			},
		})
	}
	return out
}
