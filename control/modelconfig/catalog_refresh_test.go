package modelconfig

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestCodex61ConnectContextDefaultsAndExplicitOverrides(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		provider string
		model    string
		window   int
		want     int
	}{
		{model: "gpt-6.1-sol", provider: "codex", want: 258400},
		{model: "gpt-6.1-sol", provider: "openai-codex", want: 258400},
		{model: "gpt-6.1-sol", provider: "codex", window: 258400, want: 258400},
		{model: "gpt-6.1-sol", provider: "codex", window: 500000, want: 500000},
		{model: "gpt-6.1-sol", provider: "codex", window: 1000000, want: 1000000},
		{model: "gpt-6.1-terra", provider: "codex", want: 475000},
		{model: "gpt-6.1-terra", provider: "openai-codex", want: 475000},
		{model: "gpt-6.1-terra", provider: "codex", window: 258400, want: 258400},
	} {
		t.Run(tc.model+"/"+tc.provider+"/"+strconv.Itoa(tc.window), func(t *testing.T) {
			configs, err := AssembleConnect(context.Background(), ConnectRequest{
				Provider:   tc.provider,
				APIKey:     "test-key",
				HTTPClient: &http.Client{},
				Models:     []ModelSelection{{Name: tc.model, ContextWindowTokens: tc.window}},
			}, ConnectOptions{Authenticate: func(context.Context, AuthenticateRequest) error { return nil }})
			if err != nil || len(configs) != 1 {
				t.Fatalf("AssembleConnect() = %#v, %v", configs, err)
			}
			cfg := configs[0]
			if cfg.ContextWindowTokens != tc.want || cfg.Model != tc.model {
				t.Fatalf("connected config = %#v, want original model ID and window %d", cfg, tc.want)
			}
			resolved, err := BuildModel(cfg, 128000, 0)
			if err != nil {
				t.Fatal(err)
			}
			window, ok := resolved.Model.(interface{ ContextWindowTokens() int })
			if !ok || window.ContextWindowTokens() != tc.want {
				t.Fatalf("built model = %#v, want context window %d", window, tc.want)
			}
			if tc.provider == "codex" || tc.provider == "openai-codex" {
				if cfg.Alias != "openai-codex/"+tc.model || cfg.API != model.APIOpenAICodex {
					t.Fatalf("subscription route = %#v", cfg)
				}
			}
			selected, ok, err := ResolveSelector(configs, cfg.Alias)
			if err != nil || !ok || selected.ID != cfg.ID {
				t.Fatalf("ResolveSelector(%q) = %#v, %t, %v", cfg.Alias, selected, ok, err)
			}
		})
	}
}

func TestBuildSol61ContextWindowPrecedence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		configured int
		override   int
		want       int
	}{
		{name: "host fallback", want: 128000},
		{name: "refreshed preset", configured: 258400, want: 258400},
		{name: "saved old preset", configured: 475000, want: 475000},
		{name: "explicit model window", configured: 500000, want: 500000},
		{name: "runtime override", configured: 475000, override: 1000000, want: 1000000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved, err := BuildModel(Config{
				Provider:            "openai-codex",
				Model:               "gpt-6.1-sol",
				HTTPClient:          &http.Client{},
				ContextWindowTokens: tc.configured,
			}, 128000, tc.override)
			if err != nil {
				t.Fatal(err)
			}
			window, ok := resolved.Model.(interface{ ContextWindowTokens() int })
			if !ok || window.ContextWindowTokens() != tc.want {
				t.Fatalf("built window = %#v, %t, want %d", window, ok, tc.want)
			}
		})
	}
}
