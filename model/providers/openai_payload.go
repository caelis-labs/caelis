package providers

type openAIRequestPayload struct {
	Model           string
	Models          []string
	Route           string
	Messages        []map[string]any
	Tools           []map[string]any
	Stream          bool
	Temperature     *float64
	MaxTokens       int
	ResponseFormat  *openAIResponseFormat
	Reasoning       *openAIReasoning
	ReasoningEffort string
	Thinking        *openAIThinking
	Transforms      []string
	Provider        map[string]any
	Plugins         []map[string]any
}

type openAIResponseFormat struct {
	Type       string
	JSONSchema *openAIJSONSchemaFormat
}

type openAIJSONSchemaFormat struct {
	Name   string
	Strict bool
	Schema map[string]any
}

type openAIReasoning struct {
	Effort string `json:"effort,omitempty"`
}

type openAIThinking struct {
	Type string `json:"type,omitempty"`
}

func (p *openAIRequestPayload) toMap() map[string]any {
	body := map[string]any{
		"model":    p.Model,
		"messages": p.Messages,
		"stream":   p.Stream,
	}
	if len(p.Tools) > 0 {
		body["tools"] = p.Tools
	}
	if len(p.Models) > 0 {
		body["models"] = p.Models
	}
	if p.Route != "" {
		body["route"] = p.Route
	}
	if p.Temperature != nil {
		body["temperature"] = *p.Temperature
	}
	if p.MaxTokens > 0 {
		body["max_tokens"] = p.MaxTokens
	}
	if p.ResponseFormat != nil {
		format := map[string]any{"type": p.ResponseFormat.Type}
		if p.ResponseFormat.JSONSchema != nil {
			jsonSchema := map[string]any{
				"name":   p.ResponseFormat.JSONSchema.Name,
				"schema": cloneProviderMap(p.ResponseFormat.JSONSchema.Schema),
			}
			if p.ResponseFormat.JSONSchema.Strict {
				jsonSchema["strict"] = true
			}
			format["json_schema"] = jsonSchema
		}
		body["response_format"] = format
	}
	if p.Reasoning != nil {
		body["reasoning"] = map[string]any{"effort": p.Reasoning.Effort}
	}
	if p.ReasoningEffort != "" {
		body["reasoning_effort"] = p.ReasoningEffort
	}
	if p.Thinking != nil {
		body["thinking"] = map[string]any{"type": p.Thinking.Type}
	}
	if len(p.Transforms) > 0 {
		body["transforms"] = p.Transforms
	}
	if len(p.Provider) > 0 {
		body["provider"] = cloneProviderMap(p.Provider)
	}
	if len(p.Plugins) > 0 {
		body["plugins"] = cloneMapSlice(p.Plugins)
	}
	return body
}

func cloneProviderMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = cloneProviderValue(value)
	}
	return out
}

func cloneProviderValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneProviderMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneProviderValue(item)
		}
		return out
	default:
		return typed
	}
}
