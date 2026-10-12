package gatewayapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/control/modelcatalog"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/control/modelprofile/builder"
)

func TestReloadedNoSelectorProfilePreservesProviderReasoningDefault(t *testing.T) {
	for _, tc := range []struct{ provider, name, endpoint string }{
		{provider: "openrouter", name: "minimax/minimax-m2.7"},
		{provider: "ollama", name: "mistral-large-4", endpoint: "https://ollama.com"},
		{provider: "magpie", name: "group/reasoning"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			cfg := modelconfig.NormalizeConfig(modelconfig.Config{
				Provider: tc.provider, Model: tc.name, BaseURL: tc.endpoint, ReasoningMode: modelcatalog.ReasoningModeNone,
			})
			if tc.provider == "magpie" {
				client := &http.Client{Transport: reasoningDiscoveryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"group/reasoning","context_window":200000,"max_output_tokens":32000,"reasoning":true,"supported_reasoning_levels":[],"modalities":{"input":["text"]}}]}`))}, nil
				})}
				configs, err := modelconfig.AssembleConnect(t.Context(), modelconfig.ConnectRequest{Provider: "magpie", HTTPClient: client, Models: []modelconfig.ModelSelection{{Name: tc.name}}}, modelconfig.ConnectOptions{})
				if err != nil || len(configs) != 1 {
					t.Fatalf("Magpie connect: %#v %v", configs, err)
				}
				cfg = configs[0]
			}

			if cfg.ReasoningMode != modelcatalog.ReasoningModeNone || cfg.ReasoningEffort != "" || len(cfg.ReasoningLevels) != 0 {
				t.Fatalf("invented reasoning controls: %#v", cfg)
			}
			cfg.Token, cfg.PersistToken = "", false
			profile, err := builder.FromProvider(cfg)
			if err != nil {
				t.Fatal(err)
			}
			store := newAppConfigStore(t.TempDir())
			if err := store.Save(AppConfig{Models: persistedModelConfig{Configs: []ModelConfig{cfg}}, ModelProfiles: modelprofile.Configuration{DefaultProfileID: profile.ID, DefaultEffort: "none", Profiles: []modelprofile.ModelProfile{profile}}}); err != nil {
				t.Fatal(err)
			}
			lookup, err := newModelLookup(store, 128000)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := resolveRuntimeProviderProfile(doc.ModelProfiles, lookup, profile.ID, "none")
			if err != nil {
				t.Fatal(err)
			}
			called := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				for _, field := range []string{"think", "reasoning", "reasoning_effort"} {
					if _, present := payload[field]; present {
						t.Errorf("unsupported default %s = %#v", field, payload[field])
					}
				}
				w.Header().Set("Content-Type", "application/json")
				switch tc.provider {
				case "ollama":
					_, _ = fmt.Fprint(w, `{"model":"mistral-large-4","message":{"role":"assistant","content":"done"},"done":true}`)
				case "magpie":
					_, _ = fmt.Fprint(w, `{"model":"group/reasoning","status":"completed","output":[{"id":"m1","type":"message","content":[{"type":"output_text","text":"done"}]}]}`)
				default:
					_, _ = fmt.Fprint(w, `{"id":"chat_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`)
				}
			}))
			defer server.Close()
			resolved.BaseURL, resolved.HTTPClient, resolved.Token = server.URL, server.Client(), "synthetic-token"
			llm, err := modelconfig.BuildModel(resolved, 128000, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, err := range llm.Model.Generate(context.Background(), &model.Request{Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")}, Reasoning: model.ReasoningConfig{Effort: llm.ReasoningEffort}}) {
				if err != nil {
					t.Fatal(err)
				}
			}
			if !called {
				t.Fatal("no SDK wire request after configuration reload")
			}
		})
	}
}

type reasoningDiscoveryRoundTripFunc func(*http.Request) (*http.Response, error)

func (f reasoningDiscoveryRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
