package providers

import "strings"

func newOpenRouter(cfg Config, token string) *openAICompatLLM {
	llm := newOpenAICompat(cfg, token)
	llm.api = APIOpenRouter
	llm.options.StrictFunctionTools = true
	llm.openRouter = cloneOpenRouterConfig(cfg.OpenRouter)
	return llm
}

func cloneOpenRouterConfig(in OpenRouterConfig) OpenRouterConfig {
	return OpenRouterConfig{
		Models:     cloneStringSlice(in.Models),
		Route:      strings.TrimSpace(in.Route),
		Provider:   cloneAnyMap(in.Provider),
		Transforms: cloneStringSlice(in.Transforms),
		Plugins:    cloneMapSlice(in.Plugins),
	}
}

func normalizeOpenRouterModelID(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	const providerPrefix = "openrouter/"
	if strings.HasPrefix(strings.ToLower(value), providerPrefix) {
		remainder := strings.TrimSpace(value[len(providerPrefix):])
		if strings.Contains(remainder, "/") {
			return remainder
		}
	}
	return value
}

func normalizeOpenRouterModelIDs(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, one := range in {
		if normalized := normalizeOpenRouterModelID(one); normalized != "" {
			out = append(out, normalized)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, one := range in {
		one = strings.TrimSpace(one)
		if one != "" {
			out = append(out, one)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneAnyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		if key = strings.TrimSpace(key); key != "" {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneMapSlice(in []map[string]any) []map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(in))
	for _, one := range in {
		if cloned := cloneAnyMap(one); len(cloned) > 0 {
			out = append(out, cloned)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
