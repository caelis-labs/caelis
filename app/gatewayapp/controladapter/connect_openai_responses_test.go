package controladapter

import (
	"context"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func TestConnectCatalogSeparatesOpenAIResponsesAndChatCompletions(t *testing.T) {
	t.Parallel()

	apiKey := completeConnectProviders(context.Background(), nil, "api-key", "", 100)
	official := slashCandidateByValue(apiKey, "openai")
	chat := slashCandidateByValue(apiKey, "openai-chat-compatible")
	responses := slashCandidateByValue(apiKey, "openai-responses-compatible")
	if official.Value == "" || chat.Value == "" || responses.Value == "" {
		t.Fatalf("API-key providers = %#v, want openai, openai-chat-compatible, and openai-responses-compatible", apiKey)
	}
	if slashCandidatesHaveValue(apiKey, "openai-compatible") {
		t.Fatalf("API-key providers = %#v, durable openai-compatible identity must not be the menu label", apiKey)
	}
	if !strings.Contains(official.Detail, "Responses") {
		t.Fatalf("openai detail = %q, want Responses", official.Detail)
	}
	if !strings.Contains(chat.Detail, "Chat Completions") {
		t.Fatalf("openai-chat-compatible detail = %q, want Chat Completions", chat.Detail)
	}
	if !strings.Contains(responses.Detail, "Responses") || strings.Contains(responses.Detail, "Chat Completions") {
		t.Fatalf("openai-responses-compatible detail = %q, want Responses without Chat Completions", responses.Detail)
	}
	if official.Display == chat.Display || official.Display == responses.Display || chat.Display == responses.Display {
		t.Fatalf("provider displays collided: %#v %#v %#v", official, chat, responses)
	}

	legacyQuery := completeConnectProviders(context.Background(), nil, "api-key", "openai-compatible", 10)
	if !slashCandidatesHaveValue(legacyQuery, "openai-chat-compatible") {
		t.Fatalf("query openai-compatible = %#v, want labeled chat compatible via durable Provider", legacyQuery)
	}
	if slashCandidatesHaveValue(legacyQuery, "openai-responses-compatible") {
		t.Fatalf("query openai-compatible = %#v, should not select Responses compatible", legacyQuery)
	}

	responsesQuery := completeConnectProviders(context.Background(), nil, "api-key", "openai-responses-compatible", 10)
	if !slashCandidatesHaveValue(responsesQuery, "openai-responses-compatible") {
		t.Fatalf("query openai-responses-compatible = %#v, want Responses compatible", responsesQuery)
	}

	deepseek := completeConnectProviders(context.Background(), nil, "api-key", "deepseek", 10)
	if len(deepseek) != 1 || deepseek[0].Value != "deepseek" {
		t.Fatalf("DeepSeek catalog = %#v, want unchanged", deepseek)
	}

	chatTemplate, ok := modelconfig.LookupProvider("openai-compatible")
	if !ok || chatTemplate.Provider != "openai-compatible" || chatTemplate.Label != "openai-chat-compatible" {
		t.Fatalf("LookupProvider(openai-compatible) = %#v, %v", chatTemplate, ok)
	}
}

func slashCandidateByValue(candidates []controlprompt.SlashArgCandidate, value string) controlprompt.SlashArgCandidate {
	for _, candidate := range candidates {
		if candidate.Value == value {
			return candidate
		}
	}
	return controlprompt.SlashArgCandidate{}
}
