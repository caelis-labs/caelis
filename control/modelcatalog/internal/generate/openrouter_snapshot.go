package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

type openRouterModel struct {
	ID            string `json:"id"`
	ContextLength int    `json:"context_length"`
	Architecture  struct {
		InputModalities  []string `json:"input_modalities"`
		OutputModalities []string `json:"output_modalities"`
	} `json:"architecture"`
	SupportedParameters []string `json:"supported_parameters"`
	TopProvider         struct {
		ContextLength       int `json:"context_length"`
		MaxCompletionTokens int `json:"max_completion_tokens"`
	} `json:"top_provider"`
	Reasoning *struct {
		Mandatory        bool            `json:"mandatory"`
		SupportedEfforts json.RawMessage `json:"supported_efforts"`
		DefaultEffort    string          `json:"default_effort"`
	} `json:"reasoning"`
}

// refreshOpenRouterSnapshot uses the official route metadata without deriving
// native-provider aliases. Historical records remain available to explicit
// capability lookup; directory absence is not a provider retirement claim.
func refreshOpenRouterSnapshot(compact, source []byte) ([]byte, error) {
	var snap map[string]json.RawMessage
	if err := json.Unmarshal(compact, &snap); err != nil {
		return nil, err
	}
	var response struct {
		Data       []openRouterModel `json:"data"`
		TotalCount int               `json:"total_count"`
		Links      struct {
			Next string `json:"next"`
		} `json:"links"`
	}
	if err := json.Unmarshal(source, &response); err != nil {
		return nil, err
	}
	if response.TotalCount > len(response.Data) || response.Links.Next != "" {
		return nil, fmt.Errorf("OpenRouter source is an incomplete page; provide the complete unfiltered model list")
	}
	eligible := map[string]openRouterModel{}
	for _, model := range response.Data {
		id := strings.ToLower(strings.TrimSpace(model.ID))
		if id == "" || strings.HasSuffix(id, ":batch") || model.ContextLength <= 0 ||
			!slices.Equal(model.Architecture.OutputModalities, []string{"text"}) ||
			!slices.Contains(model.SupportedParameters, "tools") {
			continue
		}
		eligible["openrouter:"+id] = model
	}
	if len(eligible) == 0 {
		return nil, fmt.Errorf("OpenRouter source contains no eligible chat/tool models")
	}
	for key, raw := range snap {
		if !strings.HasPrefix(key, "openrouter:") {
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		if _, present := eligible[key]; present {
			delete(fields, "hidden_from_directory")
		} else {
			fields["hidden_from_directory"] = json.RawMessage("true")
		}
		encoded, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		snap[key] = encoded
	}
	for key, model := range eligible {
		entry, err := openRouterCapabilities(model)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		controls, err := json.Marshal(entry)
		if err != nil {
			return nil, err
		}
		var updated map[string]json.RawMessage
		if err := json.Unmarshal(controls, &updated); err != nil {
			return nil, err
		}
		if historical, exists := snap[key]; exists {
			// Refresh published capabilities while preserving unknown fields.
			// Saved positive model limits are owned by configuration, not here.
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(historical, &fields); err != nil {
				return nil, err
			}
			for _, field := range []string{"context_window", "max_output", "tool_calls", "images", "json_output", "reasoning", "reasoning_mode", "reasoning_efforts", "default_reasoning_effort"} {
				delete(fields, field)
				if value, exists := updated[field]; exists {
					fields[field] = value
				}
			}
			snap[key], err = json.Marshal(fields)
			if err != nil {
				return nil, err
			}
		} else {
			snap[key], err = json.Marshal(updated)
			if err != nil {
				return nil, err
			}
		}
	}
	return json.Marshal(snap)
}

func openRouterCapabilities(model openRouterModel) (capEntry, error) {
	entry := capEntry{
		ContextWindow: model.ContextLength,
		MaxOutput:     model.TopProvider.MaxCompletionTokens,
		ToolCalls:     true,
		Images:        slices.Contains(model.Architecture.InputModalities, "image"),
		JSONOutput:    slices.Contains(model.SupportedParameters, "response_format") || slices.Contains(model.SupportedParameters, "structured_outputs"),
		Reasoning:     model.Reasoning != nil,
		ReasoningMode: "none",
	}
	if model.TopProvider.ContextLength > 0 && model.TopProvider.ContextLength < entry.ContextWindow {
		// Model limits describe the maximum; primary-provider limits can be
		// smaller. Use the smaller published budget as a conservative default.
		// https://openrouter.ai/docs/guides/overview/models
		entry.ContextWindow = model.TopProvider.ContextLength
	}
	if model.Reasoning == nil || len(model.Reasoning.SupportedEfforts) == 0 {
		return entry, nil
	}
	// The official API distinguishes omitted (no effort selector) from null
	// (all gateway effort values). Mandatory reasoning excludes disable.
	if bytes.Equal(bytes.TrimSpace(model.Reasoning.SupportedEfforts), []byte("null")) {
		entry.ReasoningEfforts = []string{"max", "xhigh", "high", "medium", "low", "minimal", "none"}
	} else if err := json.Unmarshal(model.Reasoning.SupportedEfforts, &entry.ReasoningEfforts); err != nil {
		return capEntry{}, err
	}
	entry.ReasoningEfforts = slices.DeleteFunc(entry.ReasoningEfforts, func(effort string) bool {
		return effort == "none" && model.Reasoning.Mandatory
	})
	if len(entry.ReasoningEfforts) > 0 {
		entry.ReasoningMode = "effort"
		if slices.Contains(entry.ReasoningEfforts, model.Reasoning.DefaultEffort) {
			entry.DefaultReasoningEffort = model.Reasoning.DefaultEffort
		}
	}
	return entry, nil
}
