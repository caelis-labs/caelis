package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/model"
)

func TestNewCodeFreeUsesDefaults(t *testing.T) {
	p := NewCodeFree(CodeFreeConfig{
		Model:       "GLM-5.1",
		Credentials: CodeFreeCredentials{UserID: "272182", APIKey: "api-key"},
	})
	if p.Name() != "GLM-5.1" {
		t.Fatalf("Name() = %q, want GLM-5.1", p.Name())
	}
	if p.provider != "codefree" {
		t.Fatalf("provider = %q, want codefree", p.provider)
	}
	if p.baseURL != defaultCodeFreeBaseURL {
		t.Fatalf("baseURL = %q, want %q", p.baseURL, defaultCodeFreeBaseURL)
	}
}

func TestCodeFreeNonStreamUsesCredentialsAndPayload(t *testing.T) {
	var headers http.Header
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != codeFreeChatCompletionsPath {
			t.Fatalf("path = %q, want %s", r.URL.Path, codeFreeChatCompletionsPath)
		}
		headers = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"GLM-5.1","choices":[{"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`)
	}))
	defer server.Close()

	p := NewCodeFree(CodeFreeConfig{
		Model:       "GLM-5.1",
		BaseURL:     server.URL,
		HTTPClient:  server.Client(),
		Credentials: CodeFreeCredentials{UserID: "272182", APIKey: "api-key"},
		Headers:     map[string]string{"x-extra": "1"},
	})
	var text string
	var usage *model.Usage
	var finish string
	for evt, err := range p.Generate(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.Part{{Text: "ping"}}}},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		text += evt.TextDelta
		if evt.Usage != nil {
			usage = evt.Usage
		}
		if evt.FinishReason != "" {
			finish = evt.FinishReason
		}
	}

	if text != "pong" {
		t.Fatalf("text = %q, want pong", text)
	}
	if usage == nil || usage.PromptTokens != 10 || usage.CompletionTokens != 2 || usage.TotalTokens != 12 {
		t.Fatalf("usage = %+v, want response usage", usage)
	}
	if finish != "stop" {
		t.Fatalf("finish = %q, want stop", finish)
	}
	if headers.Get("Authorization") != codeFreeAuthorizationValue || headers.Get("Userid") != "272182" || headers.Get("Apikey") != "api-key" {
		t.Fatalf("headers auth/user/apikey = %q/%q/%q", headers.Get("Authorization"), headers.Get("Userid"), headers.Get("Apikey"))
	}
	if headers.Get("Modelname") != "GLM-5.1" || headers.Get("Subservice") != codeFreeDefaultSubservice || headers.Get("x-extra") != "1" {
		t.Fatalf("headers = %#v, want codefree defaults plus custom", headers)
	}
	if payload["temperature"] != float64(0) || payload["top_p"] != float64(1) {
		t.Fatalf("payload temp/top_p = %#v/%#v, want 0/1", payload["temperature"], payload["top_p"])
	}
	if payload["stream"] != false {
		t.Fatalf("stream = %#v, want false", payload["stream"])
	}
	if _, ok := payload["thinking"]; ok {
		t.Fatalf("thinking present = %#v, want omitted", payload["thinking"])
	}
}

func TestCodeFreeStreamParsesSSEAndUsage(t *testing.T) {
	var accept string
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept = r.Header.Get("Accept")
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"pong\"},\"finish_reason\":\"\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewCodeFree(CodeFreeConfig{
		Model:       "GLM-5.1",
		BaseURL:     server.URL,
		HTTPClient:  server.Client(),
		Credentials: CodeFreeCredentials{UserID: "272182", APIKey: "api-key"},
	})
	var text string
	var usage *model.Usage
	var finish string
	for evt, err := range p.Generate(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.Part{{Text: "ping"}}}},
		Metadata: map[string]any{"stream": true},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		text += evt.TextDelta
		if evt.Usage != nil {
			usage = evt.Usage
		}
		if evt.FinishReason != "" {
			finish = evt.FinishReason
		}
	}
	if text != "pong" || finish != "stop" {
		t.Fatalf("text/finish = %q/%q, want pong/stop", text, finish)
	}
	if usage == nil || usage.TotalTokens != 12 {
		t.Fatalf("usage = %+v, want total 12", usage)
	}
	if accept != codeFreeStreamAcceptValue {
		t.Fatalf("Accept = %q, want %q", accept, codeFreeStreamAcceptValue)
	}
	streamOptions := payload["stream_options"].(map[string]any)
	if streamOptions["include_usage"] != true {
		t.Fatalf("stream_options = %#v, want include_usage", streamOptions)
	}
}

func TestCodeFreeStreamFallsBackToJSONResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"GLM-5.1","choices":[{"message":{"role":"assistant","content":"json fallback"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}}`)
	}))
	defer server.Close()

	p := NewCodeFree(CodeFreeConfig{
		Model:       "GLM-5.1",
		BaseURL:     server.URL,
		HTTPClient:  server.Client(),
		Credentials: CodeFreeCredentials{UserID: "272182", APIKey: "api-key"},
	})
	var text string
	var usage *model.Usage
	for evt, err := range p.Generate(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.Part{{Text: "ping"}}}},
		Metadata: map[string]any{"stream": true},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		text += evt.TextDelta
		if evt.Usage != nil {
			usage = evt.Usage
		}
	}
	if text != "json fallback" {
		t.Fatalf("text = %q, want json fallback", text)
	}
	if usage == nil || usage.TotalTokens != 23 {
		t.Fatalf("usage = %+v, want total 23", usage)
	}
}

func TestCodeFreeRetCode51IsBackpressureError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, `{"retCode":51,"message":"busy","apikey":"secret"}`)
	}))
	defer server.Close()

	p := NewCodeFree(CodeFreeConfig{
		Model:       "GLM-5.1",
		BaseURL:     server.URL,
		HTTPClient:  server.Client(),
		Credentials: CodeFreeCredentials{UserID: "272182", APIKey: "api-key"},
	})
	var gotErr error
	for _, err := range p.Generate(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.Part{{Text: "ping"}}}},
		Metadata: map[string]any{"stream": true},
	}) {
		if err != nil {
			gotErr = err
		}
	}
	if gotErr == nil {
		t.Fatal("error = nil, want retCode 51")
	}
	if !strings.Contains(gotErr.Error(), "codefree server overloaded") || !strings.Contains(gotErr.Error(), "retCode=51") {
		t.Fatalf("error = %q, want retCode 51 backpressure", gotErr)
	}
	if strings.Contains(gotErr.Error(), "secret") {
		t.Fatalf("error = %q, leaked secret", gotErr)
	}
}

func TestCodeFreeStreamRetriesRetCode51ControlPacket(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			_, _ = fmt.Fprint(w, `{"retCode":51,"message":"busy"}`)
			return
		}
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewCodeFree(CodeFreeConfig{
		Model:       "GLM-5.1",
		BaseURL:     server.URL,
		HTTPClient:  server.Client(),
		Credentials: CodeFreeCredentials{UserID: "272182", APIKey: "api-key"},
	})
	var text string
	var gotErr error
	for evt, err := range p.Generate(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.Part{{Text: "ping"}}}},
		Metadata: map[string]any{"stream": true},
	}) {
		if err != nil {
			gotErr = err
			continue
		}
		text += evt.TextDelta
	}
	if gotErr != nil {
		t.Fatalf("Generate() error = %v", gotErr)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want one retry", requests)
	}
	if text != "ok" {
		t.Fatalf("text = %q, want ok", text)
	}
}

func TestDiscoverCodeFreeModelsUsesVersionEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != codeFreeVersionCheckPath {
			t.Fatalf("path = %q, want %s", r.URL.Path, codeFreeVersionCheckPath)
		}
		if r.Header.Get("Authorization") != codeFreeAuthorizationValue || r.Header.Get("Userid") != "272182" || r.Header.Get("Apikey") != "api-key" {
			t.Fatalf("headers = %#v, want codefree auth headers", r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data":[{"modelName":"GLM-5.1","modelType":"chat","maxTokens":128000,"maxOutputTokens":8192}]}`)
	}))
	defer server.Close()

	models, err := DiscoverCodeFreeModels(context.Background(), CodeFreeConfig{
		BaseURL:     server.URL,
		HTTPClient:  server.Client(),
		Credentials: CodeFreeCredentials{UserID: "272182", APIKey: "api-key"},
	})
	if err != nil {
		t.Fatalf("DiscoverCodeFreeModels() error = %v", err)
	}
	if len(models) != 1 || models[0].Name != "GLM-5.1" || models[0].ContextWindowTokens != 128000 || models[0].MaxOutputTokens != 8192 {
		t.Fatalf("models = %#v, want GLM-5.1 metadata", models)
	}
}
