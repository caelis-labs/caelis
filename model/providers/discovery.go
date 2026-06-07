package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/OnslaughtSnail/caelis/model"
)

// RemoteModel describes one model discovered from provider list APIs.
type RemoteModel struct {
	Name                string
	ContextWindowTokens int
	MaxOutputTokens     int
	Capabilities        []string
}

// DiscoverOpenAIModels queries an OpenAI-compatible /models endpoint.
func DiscoverOpenAIModels(ctx context.Context, cfg OpenAIConfig) ([]RemoteModel, error) {
	if ctx == nil {
		return nil, fmt.Errorf("providers: context is required")
	}
	runCtx := ctx
	var cancel context.CancelFunc
	if cfg.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}
	client := coalesceHTTPClient(cfg.HTTPClient)
	endpoint := normalizeProviderBaseURL(cfg.BaseURL, defaultOpenAIBaseURL) + "/models"
	req, err := http.NewRequestWithContext(runCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if token := strings.TrimSpace(cfg.Token); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	applyConfiguredHeaders(req, cfg.Headers)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, statusError(resp)
	}
	var payload struct {
		Data []struct {
			ID                  string `json:"id"`
			ContextWindow       any    `json:"context_window"`
			MaxOutputTokens     any    `json:"max_output_tokens"`
			InputTokenLimit     any    `json:"input_token_limit"`
			OutputTokenLimit    any    `json:"output_token_limit"`
			ContextLength       any    `json:"context_length"`
			MaxCompletionTokens any    `json:"max_completion_tokens"`
			Capabilities        any    `json:"capabilities"`
			SupportedMethods    any    `json:"supported_generation_methods"`
			SupportedParameters any    `json:"supported_parameters"`
			SupportsReasoning   any    `json:"supports_reasoning"`
			ReasoningSupported  any    `json:"reasoning_supported"`
			Architecture        struct {
				InputModalities  any `json:"input_modalities"`
				OutputModalities any `json:"output_modalities"`
			} `json:"architecture"`
			TopProvider struct {
				ContextLength       any `json:"context_length"`
				MaxCompletionTokens any `json:"max_completion_tokens"`
			} `json:"top_provider"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	models := make([]RemoteModel, 0, len(payload.Data))
	for _, item := range payload.Data {
		name := strings.TrimSpace(item.ID)
		if name == "" {
			continue
		}
		caps := appendUniqueStrings(nil, toStringSlice(item.Capabilities)...)
		caps = appendUniqueStrings(caps, toStringSlice(item.SupportedMethods)...)
		caps = appendUniqueStrings(caps, toStringSlice(item.SupportedParameters)...)
		caps = appendUniqueStrings(caps, toStringSlice(item.Architecture.InputModalities)...)
		caps = appendUniqueStrings(caps, toStringSlice(item.Architecture.OutputModalities)...)
		if toBool(item.SupportsReasoning) || toBool(item.ReasoningSupported) || supportsReasoningParameter(item.SupportedParameters) {
			caps = appendUniqueStrings(caps, "reasoning")
		}
		models = append(models, RemoteModel{
			Name: name,
			ContextWindowTokens: firstPositiveInt(
				toInt(item.ContextWindow),
				toInt(item.ContextLength),
				toInt(item.InputTokenLimit),
				toInt(item.TopProvider.ContextLength),
			),
			MaxOutputTokens: firstPositiveInt(
				toInt(item.MaxOutputTokens),
				toInt(item.MaxCompletionTokens),
				toInt(item.OutputTokenLimit),
				toInt(item.TopProvider.MaxCompletionTokens),
			),
			Capabilities: caps,
		})
	}
	return normalizeRemoteModels(models), nil
}

func RemoteModelsToModelInfo(provider string, models []RemoteModel) []model.ModelInfo {
	out := make([]model.ModelInfo, 0, len(models))
	for _, remote := range normalizeRemoteModels(models) {
		info := model.ModelInfo{
			ModelID:     remote.Name,
			DisplayName: remote.Name,
			Provider:    provider,
			MaxTokens:   remote.ContextWindowTokens,
		}
		for _, cap := range remote.Capabilities {
			switch strings.ToLower(strings.TrimSpace(cap)) {
			case "tools", "tool", "tool_calls", "function_calling", "function":
				info.SupportsTools = true
			case "image", "images", "vision":
				info.SupportsImage = true
			case "audio":
				info.SupportsAudio = true
			}
		}
		out = append(out, info)
	}
	return out
}

func applyConfiguredHeaders(req *http.Request, headers map[string]string) {
	if req == nil || len(headers) == 0 {
		return
	}
	for key, value := range headers {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		req.Header.Set(key, value)
	}
}

func cloneHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for key, value := range headers {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func supportsReasoningParameter(value any) bool {
	for _, one := range toStringSlice(value) {
		switch strings.ToLower(strings.TrimSpace(one)) {
		case "reasoning", "reasoning_effort", "include_reasoning":
			return true
		}
	}
	return false
}

func normalizeRemoteModels(in []RemoteModel) []RemoteModel {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]RemoteModel, len(in))
	for _, item := range in {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		item.Name = name
		item.Capabilities = appendUniqueStrings(nil, item.Capabilities...)
		existing, ok := seen[name]
		if !ok {
			seen[name] = item
			continue
		}
		if existing.ContextWindowTokens <= 0 && item.ContextWindowTokens > 0 {
			existing.ContextWindowTokens = item.ContextWindowTokens
		}
		if existing.MaxOutputTokens <= 0 && item.MaxOutputTokens > 0 {
			existing.MaxOutputTokens = item.MaxOutputTokens
		}
		existing.Capabilities = appendUniqueStrings(existing.Capabilities, item.Capabilities...)
		seen[name] = existing
	}
	out := make([]RemoteModel, 0, len(seen))
	for _, item := range seen {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func appendUniqueStrings(base []string, values ...string) []string {
	seen := make(map[string]struct{}, len(base)+len(values))
	out := make([]string, 0, len(base)+len(values))
	for _, item := range append(base, values...) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func toStringSlice(raw any) []string {
	switch value := raw.(type) {
	case []string:
		return append([]string(nil), value...)
	case []any:
		out := make([]string, 0, len(value))
		for _, one := range value {
			text := strings.TrimSpace(fmt.Sprint(one))
			if text != "" && text != "<nil>" {
				out = append(out, text)
			}
		}
		return out
	case map[string]any:
		out := make([]string, 0, len(value))
		for k, v := range value {
			if toBool(v) {
				out = append(out, k)
			}
		}
		return out
	default:
		text := strings.TrimSpace(fmt.Sprint(raw))
		if text == "" || text == "<nil>" || text == "map[]" {
			return nil
		}
		return []string{text}
	}
}

func toInt(raw any) int {
	switch value := raw.(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float32:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		i, _ := value.Int64()
		return int(i)
	case string:
		value = strings.TrimSpace(value)
		if value == "" {
			return 0
		}
		i, _ := strconv.Atoi(value)
		return i
	default:
		return 0
	}
}

func toBool(raw any) bool {
	switch value := raw.(type) {
	case bool:
		return value
	case string:
		value = strings.TrimSpace(strings.ToLower(value))
		return value == "1" || value == "true" || value == "yes" || value == "on"
	case int:
		return value != 0
	case int64:
		return value != 0
	case float64:
		return value != 0
	default:
		return false
	}
}

func firstPositiveInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
