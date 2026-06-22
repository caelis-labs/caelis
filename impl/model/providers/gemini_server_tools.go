package providers

import (
	"encoding/json"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/model"
	"google.golang.org/genai"
)

const geminiGoogleSearchToolName = "google_search"
const geminiReplayProvider = "gemini"
const geminiReplayKindServerToolCall = "server_tool_call"
const geminiReplayKindServerToolResponse = "server_tool_response"
const geminiProviderDetailPart = "part"

type geminiGoogleSearchPreference int

const (
	geminiGoogleSearchPrefUnspecified geminiGoogleSearchPreference = iota
	geminiGoogleSearchPrefEnabled
	geminiGoogleSearchPrefDisabled
)

func geminiGoogleSearchEnabled(modelName string, specs []model.ToolSpec) bool {
	switch geminiGoogleSearchPreferenceFromToolSpecs(specs) {
	case geminiGoogleSearchPrefEnabled:
		return true
	case geminiGoogleSearchPrefDisabled:
		return false
	default:
		return geminiUsesCurrentGoogleSearchTool(modelName)
	}
}

func (l *geminiLLM) UsesProviderExecutedTools(req *model.Request) bool {
	if l == nil || req == nil {
		return false
	}
	return geminiGoogleSearchEnabled(l.name, req.Tools)
}

func geminiUsesCurrentGoogleSearchTool(modelName string) bool {
	major, ok := geminiMajorVersion(modelName)
	if !ok {
		return false
	}
	return major >= 2
}

func toGeminiTools(specs []model.ToolSpec, groundingWithGoogleSearch bool) []*genai.Tool {
	functionTools := model.FunctionToolDefinitions(specs)
	out := make([]*genai.Tool, 0, 2)
	if len(functionTools) > 0 {
		declarations := make([]*genai.FunctionDeclaration, 0, len(functionTools))
		for _, one := range functionTools {
			declarations = append(declarations, &genai.FunctionDeclaration{
				Name:                 one.Name,
				Description:          one.Description,
				ParametersJsonSchema: one.Parameters,
			})
		}
		out = append(out, &genai.Tool{FunctionDeclarations: declarations})
	}
	if groundingWithGoogleSearch {
		out = append(out, &genai.Tool{GoogleSearch: &genai.GoogleSearch{}})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func geminiGoogleSearchPreferenceFromToolSpecs(specs []model.ToolSpec) geminiGoogleSearchPreference {
	preference := geminiGoogleSearchPrefUnspecified
	for _, spec := range specs {
		if spec.Kind != model.ToolSpecKindProviderExecuted || spec.ProviderExecuted == nil {
			continue
		}
		switch geminiGoogleSearchPreferenceFromToolSpec(spec.ProviderExecuted) {
		case geminiGoogleSearchPrefDisabled:
			return geminiGoogleSearchPrefDisabled
		case geminiGoogleSearchPrefEnabled:
			preference = geminiGoogleSearchPrefEnabled
		}
	}
	return preference
}

func geminiGoogleSearchPreferenceFromToolSpec(tool *model.ProviderExecutedToolSpec) geminiGoogleSearchPreference {
	if tool == nil || !geminiProviderMatches(tool.Provider) {
		return geminiGoogleSearchPrefUnspecified
	}
	nameMatches := geminiGoogleSearchNameMatches(tool.Name)
	detailsMatch := geminiGoogleSearchProviderDetails(tool.ProviderDetails)
	if !nameMatches && !detailsMatch {
		return geminiGoogleSearchPrefUnspecified
	}
	if geminiGoogleSearchDisabledByDetails(tool.ProviderDetails) {
		return geminiGoogleSearchPrefDisabled
	}
	return geminiGoogleSearchPrefEnabled
}

func geminiProviderMatches(provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	return provider == "" || provider == "gemini" || provider == "google"
}

func geminiGoogleSearchNameMatches(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case geminiGoogleSearchToolName, "google-search", "googlesearch", "grounding_with_google_search":
		return true
	default:
		return false
	}
}

func geminiGoogleSearchProviderDetails(details map[string]json.RawMessage) bool {
	if len(details) == 0 {
		return false
	}
	for _, key := range []string{"google_search", "googleSearch"} {
		if raw, ok := details[key]; ok && raw != nil {
			return true
		}
	}
	if raw, ok := details["type"]; ok {
		var typ string
		if err := json.Unmarshal(raw, &typ); err == nil && geminiGoogleSearchNameMatches(typ) {
			return true
		}
	}
	return false
}

func geminiGoogleSearchDisabledByDetails(details map[string]json.RawMessage) bool {
	if len(details) == 0 {
		return false
	}
	if disabled, ok := geminiBoolProviderDetail(details, "disabled"); ok && disabled {
		return true
	}
	if enabled, ok := geminiBoolProviderDetail(details, "enabled"); ok && !enabled {
		return true
	}
	for _, key := range []string{"google_search", "googleSearch"} {
		if enabled, ok := geminiBoolProviderDetail(details, key); ok && !enabled {
			return true
		}
	}
	return false
}

func geminiBoolProviderDetail(details map[string]json.RawMessage, key string) (bool, bool) {
	raw, ok := details[key]
	if !ok || raw == nil {
		return false, false
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, false
	}
	return value, true
}

func geminiServerToolReplayPart(kind string, value any, token string) (model.Part, bool) {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) == 0 {
		return model.Part{}, false
	}
	part := model.NewReasoningPart("", model.ReasoningVisibilityTokenOnly)
	if part.Reasoning == nil {
		return model.Part{}, false
	}
	part.Reasoning.Replay = &model.ReplayMeta{
		Provider: geminiReplayProvider,
		Kind:     kind,
		Token:    strings.TrimSpace(token),
	}
	part.Reasoning.ProviderDetails = map[string]json.RawMessage{
		geminiProviderDetailPart: append(json.RawMessage(nil), raw...),
	}
	return part, true
}

func geminiAssistantContentParts(m model.Message) []*genai.Part {
	parts := make([]*genai.Part, 0, len(m.Parts))
	skippedServerToolCalls := map[string]struct{}{}
	for _, msgPart := range m.Parts {
		switch msgPart.Kind {
		case model.PartKindText:
			if msgPart.Text != nil && strings.TrimSpace(msgPart.Text.Text) != "" {
				parts = append(parts, genai.NewPartFromText(msgPart.Text.Text))
			}
		case model.PartKindReasoning:
			replayPart, skippedCallID := geminiServerToolPartFromReplay(msgPart)
			if skippedCallID != "" {
				skippedServerToolCalls[skippedCallID] = struct{}{}
			}
			if replayPart == nil {
				continue
			}
			if replayPart.ToolResponse != nil {
				if _, skipped := skippedServerToolCalls[strings.TrimSpace(replayPart.ToolResponse.ID)]; skipped {
					continue
				}
			}
			parts = append(parts, replayPart)
		case model.PartKindToolUse:
			if msgPart.ToolUse == nil {
				continue
			}
			call := model.ToolCall{
				ID:               msgPart.ToolUse.ID,
				Name:             msgPart.ToolUse.Name,
				Args:             toolArgsRawFromMessagePart(msgPart),
				ThoughtSignature: geminiReplayToken(msgPart.ToolUse.Replay),
			}
			// Gemini tool loop requires thought signature in functionCall parts.
			// Skip legacy tool calls without signature to avoid request rejection.
			if call.ThoughtSignature == "" {
				continue
			}
			part := genai.NewPartFromFunctionCall(call.Name, toolArgsMap(call.Args))
			if strings.TrimSpace(call.ID) != "" && part != nil && part.FunctionCall != nil {
				part.FunctionCall.ID = call.ID
			}
			part.ThoughtSignature = decodeGeminiThoughtSignature(call.ThoughtSignature)
			parts = append(parts, part)
		}
	}
	return parts
}

func geminiReplayToken(replay *model.ReplayMeta) string {
	if replay == nil {
		return ""
	}
	return strings.TrimSpace(replay.Token)
}

func toolArgsRawFromMessagePart(part model.Part) string {
	if part.ToolUse == nil {
		return ""
	}
	calls := model.NewMessage(model.RoleAssistant, part).ToolCalls()
	if len(calls) == 0 {
		return ""
	}
	return strings.TrimSpace(calls[0].Args)
}

func geminiServerToolPartFromReplay(part model.Part) (*genai.Part, string) {
	if part.Reasoning == nil || part.Reasoning.Replay == nil {
		return nil, ""
	}
	replay := part.Reasoning.Replay
	if !strings.EqualFold(strings.TrimSpace(replay.Provider), geminiReplayProvider) {
		return nil, ""
	}
	raw := part.Reasoning.ProviderDetails[geminiProviderDetailPart]
	if len(raw) == 0 {
		return nil, ""
	}
	switch strings.TrimSpace(replay.Kind) {
	case geminiReplayKindServerToolCall:
		var call genai.ToolCall
		if err := json.Unmarshal(raw, &call); err != nil {
			return nil, ""
		}
		token := geminiReplayToken(replay)
		if token == "" {
			return nil, strings.TrimSpace(call.ID)
		}
		return &genai.Part{
			ToolCall:         &call,
			ThoughtSignature: decodeGeminiThoughtSignature(token),
		}, ""
	case geminiReplayKindServerToolResponse:
		var response genai.ToolResponse
		if err := json.Unmarshal(raw, &response); err != nil {
			return nil, ""
		}
		return &genai.Part{ToolResponse: &response}, ""
	default:
		return nil, ""
	}
}
