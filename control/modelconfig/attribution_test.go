package modelconfig

import (
	"net/http"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestBuildModelSendsProductAttribution(t *testing.T) {
	const responses = "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"
	const chat = `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`
	for _, tt := range []struct {
		provider, path, body       string
		api                        model.APIType
		auth                       model.AuthType
		referer, title, originator string
	}{
		{"openrouter", "/chat/completions", chat, model.APIOpenRouter, model.AuthAPIKey, "https://github.com/caelis-labs/caelis", "Caelis", ""},
		{"openai-codex", "/responses", responses, model.APIOpenAICodex, model.AuthOAuthToken, "", "", "caelis"},
		{"xai", "/responses", responses, model.APIXAIResponses, model.AuthOAuthToken, "", "", ""},
	} {
		t.Run(tt.provider, func(t *testing.T) {
			requests := 0
			client := &http.Client{Transport: modelconfigRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				requests++
				if req.URL.Path != tt.path {
					t.Errorf("path = %q, want %q", req.URL.Path, tt.path)
				}
				for header, want := range map[string]string{
					"User-Agent": "caelis/1.2.3", "HTTP-Referer": tt.referer, "X-Title": tt.title, "originator": tt.originator,
				} {
					if got := req.Header.Get(header); got != want {
						t.Errorf("%s = %q, want %q", header, got, want)
					}
				}
				return modelconfigHTTPResponse(req, http.StatusOK, tt.body), nil
			})}
			resolved, err := BuildModel(Config{
				Provider: tt.provider, API: tt.api, Model: "probe", BaseURL: "https://model.example.test",
				AuthType: tt.auth, Token: "fixture", HTTPClient: client,
			}, ModelBuildOptions{ClientVersion: "v1.2.3"})
			if err != nil {
				t.Fatal(err)
			}
			for _, err := range model.Generate(t.Context(), resolved.Model, &model.Request{
				Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")},
			}) {
				if err != nil {
					t.Fatal(err)
				}
			}
			if requests != 1 {
				t.Fatalf("requests = %d, want one", requests)
			}
		})
	}
}
