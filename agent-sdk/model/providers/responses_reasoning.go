package providers

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// Plaintext continuation is distinct from a display-only reasoning summary.
// VisibleText owns the text; Replay records its wire kind and provider, without
// duplicating the text as an opaque token.
const openAIResponsesReplayKindText = "reasoning_text"

// Retain the provider-issued item identity without depending on stored responses.
const openAIResponsesReasoningItemID = "openai_responses_item_id"

type openAIResponsesPlainReasoningInput struct {
	ID      string                        `json:"id,omitempty"`
	Type    string                        `json:"type"`
	Summary []openAICodexReasoningSummary `json:"summary"`
	Content []openAICodexInputText        `json:"content"`
}

func openAIResponsesPlainReasoningReplay(part model.Part, provider string) (openAIResponsesPlainReasoningInput, bool) {
	if part.Reasoning == nil || part.Reasoning.Replay == nil || part.Reasoning.VisibleText == nil {
		return openAIResponsesPlainReasoningInput{}, false
	}
	replay := part.Reasoning.Replay
	if replay.Kind != openAIResponsesReplayKindText || !strings.EqualFold(strings.TrimSpace(replay.Provider), strings.TrimSpace(provider)) || *part.Reasoning.VisibleText == "" {
		return openAIResponsesPlainReasoningInput{}, false
	}
	var id string
	_ = json.Unmarshal(part.Reasoning.ProviderDetails[openAIResponsesReasoningItemID], &id)
	return openAIResponsesPlainReasoningInput{
		ID:      id,
		Type:    "reasoning",
		Summary: []openAICodexReasoningSummary{},
		Content: []openAICodexInputText{{Type: "reasoning_text", Text: *part.Reasoning.VisibleText}},
	}, true
}
