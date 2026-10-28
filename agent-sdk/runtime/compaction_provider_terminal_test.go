package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type compactionTerminalTransport func(*http.Request) (*http.Response, error)

func (f compactionTerminalTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestCompactionProviderTerminalNeverRetriesOrFallsBack(t *testing.T) {
	for _, api := range []providers.APIType{providers.APIOllama, providers.APIGemini} {
		for _, stream := range []bool{false, true} {
			for _, reason := range []string{"length", "unknown", "filtered", "unspecified"} {
				if api == providers.APIOllama && (reason == "filtered" || reason == "unspecified") {
					continue
				}
				t.Run(fmt.Sprintf("%s/stream=%v/%s", api, stream, reason), func(t *testing.T) {
					calls, admissions, receipts := 0, 0, 0
					transport := compactionTerminalTransport(func(req *http.Request) (*http.Response, error) {
						calls++
						var body map[string]any
						if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
							return nil, err
						}
						wire := ""
						if api == providers.APIOllama {
							wire = fmt.Sprintf(`{"model":"fixture","message":{"role":"assistant","content":"partial summary with enough text to look useful"},"done":true,"done_reason":%q,"prompt_eval_count":11,"eval_count":7}`, reason)
						} else {
							rawReason := map[string]string{"length": "MAX_TOKENS", "unknown": "FUTURE_REASON", "filtered": "SAFETY", "unspecified": "FINISH_REASON_UNSPECIFIED"}[reason]
							wire = fmt.Sprintf(`{"candidates":[{"content":{"role":"model","parts":[{"text":"partial summary with enough text to look useful"}]},"finishReason":%q}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7,"totalTokenCount":18}}`, rawReason)
							if strings.Contains(req.URL.Path, "streamGenerateContent") {
								wire = "data: " + wire + "\n\n"
							}
						}
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(wire)), Request: req}, nil
					})
					factory := providers.NewFactory()
					if err := factory.Register(providers.Config{Alias: "terminal", API: api, Provider: string(api), Model: "fixture", BaseURL: "http://fixture.invalid", Auth: providers.AuthConfig{Type: providers.AuthAPIKey, Token: "fixture"}, HTTPClient: &http.Client{Transport: transport}, Retry: model.RetryConfig{MaxRetries: 1, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond}}); err != nil {
						t.Fatal(err)
					}
					llm, err := factory.NewByAlias("terminal")
					if err != nil {
						t.Fatal(err)
					}
					ctx := model.WithInvocationObserver(t.Context(), func(in model.Invocation) {
						receipts++
						if in.Usage.TotalTokens != 18 {
							t.Errorf("usage=%+v", in.Usage)
						}
					})
					ctx = model.WithInvocationAdmission(ctx, func(context.Context, *model.Request) error {
						admissions++
						return nil
					})
					c := newCodexStyleCompactor(CompactionConfig{Enabled: true, MaxRetryAttempts: 2, RetryBaseDelay: time.Nanosecond, RetryMaxDelay: time.Nanosecond}, nil).(compact.ForceEngine)
					result, err := c.Force(ctx, compact.Request{Model: llm, Events: []*session.Event{userTextEvent("retain original source")}, InContextRequest: &model.Request{Stream: stream, Messages: []model.Message{model.NewTextMessage(model.RoleUser, "source")}}}, "manual")
					if !isCompactionTerminalError(err) || result.Compacted || calls != 1 || admissions != 1 || receipts != 1 {
						t.Fatalf("terminal=%v compacted=%v HTTP=%d admissions=%d receipts=%d", err, result.Compacted, calls, admissions, receipts)
					}
				})
			}
		}
	}
}
