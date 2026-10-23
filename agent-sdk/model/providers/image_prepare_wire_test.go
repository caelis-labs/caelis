package providers

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestFactoryPreparesUserAndHistoricalToolImagesForEveryWireFamily(t *testing.T) {
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewNRGBA(image.Rect(0, 0, 9, 12249))); err != nil {
		t.Fatal(err)
	}
	part := model.NewMediaPart(model.MediaModalityImage, model.MediaSource{Kind: model.MediaSourceInline, Data: base64.StdEncoding.EncodeToString(pngData.Bytes())}, "image/png", "long.png")
	req := &model.Request{Messages: []model.Message{
		model.NewMessage(model.RoleUser, model.NewTextPart("inspect"), part),
		model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "view", Name: "ViewImage", Args: `{}`}}, ""),
		model.NewMessage(model.RoleTool, model.Part{Kind: model.PartKindToolResult, ToolResult: &model.ToolResultPart{ToolUseID: "view", Name: "ViewImage", Content: []model.Part{model.NewTextPart("tool evidence"), part}}}),
		model.NewTextMessage(model.RoleUser, "continue"),
	}}
	original := model.CloneRequest(req)
	for _, api := range []APIType{APIOpenAI, APIOpenAIResponses, APIOpenAICompatible, APIOpenRouter, APIAnthropic, APIAnthropicCompatible, APIDeepSeek, APIMiniMax, APIMimo, APIVolcengine, APIVolcengineCoding, APIGemini, APIOllama, APIOpenAICodex, APIXAIResponses} {
		t.Run(string(api), func(t *testing.T) {
			requests := 0
			server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var body any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				images := wireInlineImages(body)
				if len(images) != 14 {
					t.Errorf("wire image count=%d, want seven crops for both user and tool images", len(images))
				}
				for _, data := range images {
					raw, err := base64.StdEncoding.DecodeString(data)
					if err != nil {
						t.Error(err)
						continue
					}
					img, _, err := image.Decode(bytes.NewReader(raw))
					if err != nil || img.Bounds().Dx() != 9 || img.Bounds().Dy() != 2048 {
						t.Errorf("wire image was not prepared: %v", err)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/messages"):
					_, _ = fmt.Fprint(w, `{"id":"msg","type":"message","role":"assistant","model":"test-model","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
				case strings.HasSuffix(r.URL.Path, "/generateContent") || strings.HasSuffix(r.URL.Path, ":generateContent"):
					_, _ = fmt.Fprint(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
				case strings.HasSuffix(r.URL.Path, "/api/chat"):
					_, _ = fmt.Fprint(w, `{"model":"test-model","message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop"}`)
				case strings.HasSuffix(r.URL.Path, "/responses"):
					if api == APIOpenAICodex || api == APIXAIResponses {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp\",\"model\":\"test-model\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n")
					} else {
						writeOpenAIResponsesJSON(t, w, openAIResponsesCompletedJSON("test-model", "ok"))
					}
				default:
					_, _ = fmt.Fprint(w, `{"model":"test-model","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
				}
			}))
			defer server.Close()
			cfg := Config{Alias: "test", API: api, Provider: "gateway", Model: "deepseek/deepseek-flash", BaseURL: server.URL, HTTPClient: server.Client(), ImageInput: true, MaxOutputTok: 1024,
				Auth: AuthConfig{Type: AuthAPIKey, Token: "synthetic"}, Retry: model.RetryConfig{MaxRetries: 1, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond}}
			if api == APIDeepSeek {
				cfg.BaseURL += "/anthropic"
			}
			if api == APIOpenAICodex || api == APIXAIResponses {
				cfg.Auth.Type = AuthOAuthToken
			}
			factory := NewFactory()
			if err := factory.Register(cfg); err != nil {
				t.Fatal(err)
			}
			llm, err := factory.NewByAlias("test")
			if err != nil {
				t.Fatal(err)
			}
			var final *model.Response
			for event, err := range llm.Generate(t.Context(), req) {
				if err != nil {
					t.Fatal(err)
				}
				if event != nil && event.Response != nil {
					final = event.Response
				}
			}
			if requests != 1 || final == nil || final.Message.TextContent() != "ok" {
				t.Fatalf("requests=%d response=%+v", requests, final)
			}
			if !reflect.DeepEqual(req, original) {
				t.Fatal("provider preparation changed canonical history")
			}
		})
	}
}

func wireInlineImages(value any) []string {
	var images []string
	switch value := value.(type) {
	case string:
		if strings.HasPrefix(value, "data:image/") {
			_, data, ok := strings.Cut(value, ",")
			if ok {
				images = append(images, data)
			}
		} else if strings.HasPrefix(value, "iVBORw0KGgo") {
			images = append(images, value)
		}
	case map[string]any:
		for _, value := range value {
			images = append(images, wireInlineImages(value)...)
		}
	case []any:
		for _, value := range value {
			images = append(images, wireInlineImages(value)...)
		}
	}
	return images
}

func TestFactoryDoesNotRetryPermanentImageHTTPFailures(t *testing.T) {
	for _, api := range []APIType{APIOpenAIResponses, APIOpenAICompatible, APIAnthropic, APIGemini, APIOllama, APIOpenAICodex, APIXAIResponses} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", api, stream), func(t *testing.T) {
				requests := 0
				server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests++
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = fmt.Fprint(w, `{"error":{"code":400,"type":"invalid_request_error","status":"INVALID_ARGUMENT","message":"You have uploaded an unsupported image"}}`)
				}))
				defer server.Close()
				factory := NewFactory()
				cfg := Config{Alias: "test", API: api, Model: "test-model", BaseURL: server.URL, HTTPClient: server.Client(), Auth: AuthConfig{Token: "synthetic"}, Retry: model.RetryConfig{MaxRetries: 2, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond}}
				if api == APIOpenAICodex || api == APIXAIResponses {
					cfg.Auth.Type = AuthOAuthToken
				}
				if err := factory.Register(cfg); err != nil {
					t.Fatal(err)
				}
				llm, err := factory.NewByAlias("test")
				if err != nil {
					t.Fatal(err)
				}
				var last error
				for _, err := range llm.Generate(t.Context(), &model.Request{Messages: []model.Message{model.NewTextMessage(model.RoleUser, "test")}, Stream: stream}) {
					last = err
				}
				if last == nil || model.IsRetryableLLMError(last) || requests != 1 {
					t.Fatalf("requests=%d error=%v", requests, last)
				}
			})
		}
	}
}

func TestFactoryDoesNotRetryPermanentImageResponsesEvents(t *testing.T) {
	for _, api := range []APIType{APIOpenAIResponses, APIOpenAICodex, APIXAIResponses} {
		for _, event := range []string{
			`{"type":"response.failed","response":{"status":"failed","error":{"code":"image_too_large"}}}`,
			`{"type":"error","code":"unsupported_image"}`,
			`{"type":"response.failed","response":{"error":{"message":"could not decode image"}}}`,
		} {
			t.Run(string(api)+"/"+event, func(t *testing.T) {
				requests := 0
				server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					requests++
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
				}))
				defer server.Close()
				factory := NewFactory()
				if err := factory.Register(Config{Alias: "test", API: api, Model: "test-model", BaseURL: server.URL, HTTPClient: server.Client(), Auth: AuthConfig{Type: AuthOAuthToken, Token: "synthetic"}, Retry: model.RetryConfig{MaxRetries: 2, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond}}); err != nil {
					t.Fatal(err)
				}
				llm, err := factory.NewByAlias("test")
				if err != nil {
					t.Fatal(err)
				}
				var last error
				for _, err := range llm.Generate(t.Context(), &model.Request{Messages: []model.Message{model.NewTextMessage(model.RoleUser, "test")}, Stream: true}) {
					last = err
				}
				if !errorcode.Is(last, errorcode.InvalidArgument) || model.IsRetryableLLMError(last) || requests != 1 {
					t.Fatalf("requests=%d error=%v", requests, last)
				}
			})
		}
	}
}
