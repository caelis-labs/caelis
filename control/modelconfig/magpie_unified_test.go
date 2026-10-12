package modelconfig

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/judgment"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestMagpieUnifiedCatalogKeepsLegacyMenuAndDispatchIdentity(t *testing.T) {
	for _, base := range []string{"http://127.0.0.1:3425/v1", "https://gateway.example/v1"} {
		t.Run(base, func(t *testing.T) {
			calls := map[string]int{}
			client := &http.Client{Transport: modelconfigRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				body := ""
				switch req.URL.Path {
				case "/v1/models":
					body = `{"data":[{"id":"codex/gpt-6-luna","context_window":272000,"max_output_tokens":32768,"reasoning":false,"modalities":{"input":["text"]}}]}`
					if req.Header.Get("X-Magpie-Deciders") == "1" {
						body = `{"data":[{"id":"codex/gpt-6-luna","context_window":272000,"max_output_tokens":32768,"reasoning":false,"modalities":{"input":["text"]}},{"id":"typesafe/jev-latest","kind":"decision"}]}`
					}
				case "/v1/responses", "/v1/systemone":
					var input map[string]any
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Fatal(err)
					}
					name, _ := input["model"].(string)
					calls[name]++
					if req.URL.Path == "/v1/responses" && name == "codex/gpt-6-luna" {
						body = `{"model":"codex/gpt-6-luna","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`
					} else if req.URL.Path == "/v1/systemone" && name == "typesafe/jev-latest" {
						body = `{"model":"typesafe/jev-latest","answers":{"relevance":{"type":"score","score":1,"confidence":1,"probabilities":{"0":0,"1":1}}}}`
					} else {
						t.Fatalf("wrong dispatch %s: %s", req.URL.Path, name)
					}
				default:
					t.Fatalf("unexpected endpoint %s", req.URL.Path)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			req := ConnectRequest{Provider: "magpie", BaseURL: base, HTTPClient: client, APIKey: "fixture"}
			menu, err := SelectableModels(t.Context(), req)
			if err != nil || len(menu) != 1 || menu[0].Judgment {
				t.Fatalf("legacy menu: %#v %v", menu, err)
			}
			req.IncludeJudgments = true
			menu, err = SelectableModels(t.Context(), req)
			if err != nil || len(menu) != 2 || !menu[1].Judgment {
				t.Fatalf("unified menu: %#v %v", menu, err)
			}
			req.Models = []ModelSelection{{Name: "codex/gpt-6-luna"}, {Name: "typesafe/jev-latest"}}
			configs, err := AssembleConnect(t.Context(), req, ConnectOptions{})
			if err != nil || len(configs) != 2 {
				t.Fatalf("mixed assembly: %#v %v", configs, err)
			}
			if IsJudgment(configs[0]) || !IsJudgment(configs[1]) || configs[0].ProviderEndpointID == configs[1].ProviderEndpointID {
				t.Fatalf("protocol identities: %#v", configs)
			}
			for i, provider := range []string{"magpie", "magpie-judgment"} {
				legacy := req
				legacy.Provider, legacy.Models = provider, req.Models[i:i+1]
				old, err := AssembleConnect(t.Context(), legacy, ConnectOptions{})
				if err != nil || old[0].ID != configs[i].ID {
					t.Fatalf("legacy identity changed: %#v %v", old, err)
				}
				endpointRaw, _ := json.Marshal(SanitizePersistedProviderEndpoint(ProviderEndpointFromConfig(configs[i])))
				configRaw, _ := json.Marshal(SanitizePersistedConfig(configs[i]))
				var saved Config
				var endpoint ProviderEndpointConfig
				if err := json.Unmarshal(configRaw, &saved); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(endpointRaw, &endpoint); err != nil {
					t.Fatal(err)
				}
				saved = MergeConfigProviderEndpoint(saved, endpoint)
				saved.Token, saved.HTTPClient = "fixture", client
				if IsJudgment(saved) {
					evaluator, err := BuildJudgment(saved)
					if err != nil {
						t.Fatal(err)
					}
					_, err = evaluator.Evaluate(t.Context(), judgment.Request{State: "fixture", Questions: map[string]judgment.Question{"relevance": {Type: judgment.Score, Instructions: "Score", Criteria: []string{"no", "yes"}}}})
					if err != nil {
						t.Fatal(err)
					}
				} else {
					built, err := BuildModel(saved, 0, 0)
					if err != nil {
						t.Fatal(err)
					}
					for _, err := range built.Model.Generate(t.Context(), &model.Request{Messages: []model.Message{model.NewTextMessage(model.RoleUser, "fixture")}}) {
						if err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			if calls["codex/gpt-6-luna"] != 1 || calls["typesafe/jev-latest"] != 1 {
				t.Fatalf("calls: %v", calls)
			}
		})
	}
}

func TestMagpieSelectionExcludesKnownNonConversationalModels(t *testing.T) {
	client := &http.Client{Transport: modelconfigRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"data":[{"id":"xiaomi/mimo-v2.5-asr","type":"model"},{"id":"vendor/custom-unknown"},{"id":"vendor/chat","kind":"chat"},{"id":"vendor/embed","kind":"embedding"}]}`
		if req.Header.Get("X-Magpie-Deciders") == "1" {
			body = `{"data":[{"id":"xiaomi/mimo-v2.5-asr","type":"model"},{"id":"vendor/custom-unknown"},{"id":"vendor/chat","kind":"chat"},{"id":"vendor/embed","kind":"embedding"},{"id":"typesafe/jev-latest","kind":"decision"}]}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	for _, includeJudgments := range []bool{false, true} {
		models, err := SelectableModels(t.Context(), ConnectRequest{Provider: "magpie", BaseURL: "http://127.0.0.1:3425/v1", HTTPClient: client, IncludeJudgments: includeJudgments})
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]bool{"vendor/chat": false, "vendor/custom-unknown": false}
		if includeJudgments {
			want["typesafe/jev-latest"] = true
		}
		if len(models) != len(want) {
			t.Fatalf("models = %#v, want %v", models, want)
		}
		for _, item := range models {
			judgment, ok := want[item.Name]
			if !ok || item.Judgment != judgment {
				t.Fatalf("model = %#v, want %v", item, want)
			}
		}
	}
}
