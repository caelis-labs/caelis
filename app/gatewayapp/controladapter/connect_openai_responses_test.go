package controladapter

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func TestConnectCatalogOffersOneCustomProtocolEntry(t *testing.T) {
	t.Parallel()
	candidates := completeConnectProviders(t.Context(), nil, "api-key", "", 100)
	for _, want := range []string{"deepseek", "xiaomi", "magpie", "ollama", "custom"} {
		if !slashCandidatesHaveValue(candidates, want) {
			t.Fatalf("missing %s: %#v", want, candidates)
		}
	}
	for _, retired := range []string{"openai", "anthropic", "gemini", "openrouter", "minimax", "volcengine", "openai-chat-compatible", "openai-responses-compatible"} {
		if slashCandidatesHaveValue(candidates, retired) {
			t.Fatalf("retired preset %s", retired)
		}
	}
	protocols, err := completeConnectArgs(t.Context(), nil, "connect-custom-protocol", "", 100)
	if err != nil || len(protocols) != 4 {
		t.Fatalf("protocol choices: %#v %v", protocols, err)
	}
	for _, entry := range protocols {
		if !strings.HasPrefix(entry.Value, "custom-") {
			t.Fatalf("unowned protocol: %s", entry.Value)
		}
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
