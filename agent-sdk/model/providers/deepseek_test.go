package providers

import (
	"testing"
)

func TestDeepSeekCustomNonAnthropicEndpointUsesOpenAICompatFallback(t *testing.T) {
	llm := newDeepSeek(Config{
		Provider: "deepseek",
		Model:    "deepseek-v4-pro",
		BaseURL:  "https://proxy.example.com/v1",
		Auth: AuthConfig{
			Type:  AuthAPIKey,
			Token: "deepseek-token",
		},
	}, "deepseek-token")

	typed, ok := llm.(*openAICompatLLM)
	if !ok {
		t.Fatalf("newDeepSeek(custom /v1) = %T, want OpenAI-compatible fallback", llm)
	}
	if typed.options.StructuredOutput != openAICompatStructuredOutputJSONOutput {
		t.Fatalf("StructuredOutput = %q, want legacy DeepSeek json_object compatibility", typed.options.StructuredOutput)
	}
}

func TestDeepSeekCustomAnthropicEndpointUsesAnthropicSDK(t *testing.T) {
	llm := newDeepSeek(Config{
		Provider: "deepseek",
		Model:    "deepseek-v4-pro",
		BaseURL:  "https://proxy.example.com/anthropic",
		Auth: AuthConfig{
			Type:  AuthAPIKey,
			Token: "deepseek-token",
		},
	}, "deepseek-token")

	if _, ok := llm.(*anthropicSDKLLM); !ok {
		t.Fatalf("newDeepSeek(custom /anthropic) = %T, want Anthropic SDK path", llm)
	}
}

func TestDeepSeekCustomAnthropicV1EndpointUsesAnthropicSDK(t *testing.T) {
	llm := newDeepSeek(Config{
		Provider: "deepseek",
		Model:    "deepseek-v4-pro",
		BaseURL:  "https://proxy.example.com/anthropic/v1",
		Auth: AuthConfig{
			Type:  AuthAPIKey,
			Token: "deepseek-token",
		},
	}, "deepseek-token")

	if _, ok := llm.(*anthropicSDKLLM); !ok {
		t.Fatalf("newDeepSeek(custom /anthropic/v1) = %T, want Anthropic SDK path", llm)
	}
}
