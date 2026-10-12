package modelconfig

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/judgment"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestMagpieDiscoveryAssemblyAndPersistedDispatch(t *testing.T) {
	for _, decision := range []bool{false, true} {
		t.Run(map[bool]string{false: "chat", true: "judgment"}[decision], func(t *testing.T) {
			provider, name := "magpie", "xiaomi/mimo-v2.5"
			if decision {
				provider, name = "magpie-judgment", "typesafe/jev-latest"
			}
			calls := 0
			client := &http.Client{Transport: modelconfigRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				body := ""
				switch req.URL.Path {
				case "/v1/models":
					body = `{"data":[{"id":"xiaomi/mimo-v2.5","context_window":1048576,"max_output_tokens":131072,"reasoning":true,"supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}],"modalities":{"input":["text","image"]}}]}`
					if decision {
						if req.Header.Get("X-Magpie-Deciders") != "1" {
							t.Fatal("decision discovery header missing")
						}
						body = `{"data":[{"id":"xiaomi/mimo-v2.5"},{"id":"typesafe/jev-latest","kind":"decision"}]}`
					}
				case "/v1/responses":
					calls++
					var input map[string]any
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Fatal(err)
					}
					if input["model"] != name {
						t.Fatalf("rewritten upstream ID: %v", input["model"])
					}
					body = `{"model":"xiaomi/mimo-v2.5","status":"completed","output":[{"id":"m1","type":"message","content":[{"type":"output_text","text":"ok"}]}]}`
				case "/v1/systemone":
					calls++
					var input map[string]any
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Fatal(err)
					}
					if input["model"] != name || req.Header.Get("Authorization") != "Bearer magpie" {
						t.Fatal("judgment route or loopback credential missing")
					}
					body = `{"model":"jev-1.13.0","answers":{"relevance":{"type":"score","score":1,"confidence":1,"probabilities":{"0":0,"1":1}}},"usage":{"input_tokens":10,"output_tokens":1}}`
				default:
					t.Fatalf("unexpected endpoint: %s", req.URL.Path)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			configs, err := AssembleConnect(context.Background(), ConnectRequest{Provider: provider, HTTPClient: client, Models: []ModelSelection{{Name: name}}}, ConnectOptions{})
			if err != nil || len(configs) != 1 {
				t.Fatalf("connect: %#v %v", configs, err)
			}
			cfg := configs[0]
			if cfg.Provider != "magpie" || cfg.Model != name || IsJudgment(cfg) != decision {
				t.Fatalf("identity: %#v", cfg)
			}
			if !decision && (cfg.ContextWindowTokens != 1048576 || cfg.MaxOutputTok != 32768 || cfg.ImageInput == nil || !*cfg.ImageInput || len(ReasoningLevelsForConfig(cfg)) != 2) {
				t.Fatalf("accepted metadata lost: %#v", cfg)
			}
			raw, _ := json.Marshal(SanitizePersistedConfig(cfg))
			var saved Config
			if err = json.Unmarshal(raw, &saved); err != nil {
				t.Fatal(err)
			}
			endpointRaw, _ := json.Marshal(SanitizePersistedProviderEndpoint(ProviderEndpointFromConfig(cfg)))
			var endpoint ProviderEndpointConfig
			if err = json.Unmarshal(endpointRaw, &endpoint); err != nil {
				t.Fatal(err)
			}
			cfg = MergeConfigProviderEndpoint(saved, endpoint)
			cfg.HTTPClient = client

			if decision {
				evaluator, err := BuildJudgment(cfg)
				if err != nil {
					t.Fatal(err)
				}
				_, err = evaluator.Evaluate(t.Context(), judgment.Request{State: "synthetic", Questions: map[string]judgment.Question{"relevance": {Type: judgment.Score, Instructions: "Score relevance", Criteria: []string{"irrelevant", "relevant"}}}})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				resolved, err := BuildModel(cfg, 0, 0)
				if err != nil {
					t.Fatal(err)
				}
				for _, err := range resolved.Model.Generate(t.Context(), &model.Request{Messages: []model.Message{model.NewTextMessage(model.RoleUser, "synthetic")}}) {
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if calls != 1 {
				t.Fatalf("generation calls=%d", calls)
			}
		})
	}
}

func TestCustomConnectionsKeepProtocolAndEndpointIdentity(t *testing.T) {
	var configs []Config
	for _, provider := range []string{"custom-chat", "custom-responses"} {
		for _, base := range []string{"https://a.invalid/v1", "https://b.invalid/v1"} {
			rows, err := AssembleConnect(t.Context(), ConnectRequest{Provider: provider, BaseURL: base, APIKey: "fixture", Models: []ModelSelection{{Name: "same/model"}}}, ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			configs = append(configs, rows...)
		}
	}
	if err := ValidatePublicSelectors(configs); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, cfg := range configs {
		if seen[cfg.ID] {
			t.Fatalf("collapsed endpoint/protocol: %s", cfg.ID)
		}
		seen[cfg.ID] = true
	}
}

func TestMagpieExplicitLoopbackKeyScopesDiscoveryAndGeneration(t *testing.T) {
	client := &http.Client{Transport: modelconfigRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer fixture-scoped-key" {
			t.Fatal("explicit loopback key was ignored")
		}
		return modelconfigHTTPResponse(req, 200, `{"data":[{"id":"vendor/model","context_window":128000,"max_output_tokens":8192,"reasoning":false,"modalities":{"input":["text"]}}]}`), nil
	})}
	rows, err := AssembleConnect(t.Context(), ConnectRequest{Provider: "magpie", APIKey: "fixture-scoped-key", HTTPClient: client, Models: []ModelSelection{{Name: "vendor/model"}}}, ConnectOptions{})
	if err != nil || len(rows) != 1 || rows[0].AuthType != model.AuthAPIKey {
		t.Fatalf("scoped gateway key: %#v %v", rows, err)
	}
}
