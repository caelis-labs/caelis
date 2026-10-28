package providers

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func assertFinishReasonReported(t *testing.T, response *model.Response) {
	t.Helper()
	if !response.FinishReasonReported {
		t.Error("final response must report its finish reason, including unknown")
	}
}

func TestFinishReasonNormalization(t *testing.T) {
	t.Run("anthropic refusal", func(t *testing.T) {
		if got := normalizeAnthropicFinishReason(anthropic.StopReasonRefusal); got != model.FinishReasonRefusal {
			t.Fatalf("finish reason = %q, want refusal", got)
		}
	})
	for _, tc := range []struct {
		raw  string
		want model.FinishReason
	}{
		{"", model.FinishReasonUnknown},
		{" FUTURE_REASON ", model.FinishReasonUnknown},
		{"stop", model.FinishReasonStop},
		{"length", model.FinishReasonLength},
		{"max_tokens", model.FinishReasonLength},
		{"tool_calls", model.FinishReasonToolCalls},
		{"function_call", model.FinishReasonToolCalls},
		{"content_filter", model.FinishReasonContentFilter},
		{"refusal", model.FinishReasonRefusal},
		{" REFUSAL ", model.FinishReasonRefusal},
	} {
		t.Run("compat/"+tc.raw, func(t *testing.T) {
			if got := normalizeOpenAICompatFinishReason(tc.raw); got != tc.want {
				t.Fatalf("finish reason = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOpenAICompatFinishReasonEvidence(t *testing.T) {
	for _, mode := range []struct{ stream, done bool }{{false, false}, {true, true}, {true, false}} {
		for _, reason := range []string{"length", " FUTURE_REASON ", ""} {
			if mode.stream && !mode.done && reason == "" {
				continue // An empty reason still requires the DONE marker.
			}
			t.Run(fmt.Sprintf("stream=%v/done=%v/reason=%q", mode.stream, mode.done, reason), func(t *testing.T) {
				server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if mode.stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":%q}]}\n\ndata: {\"choices\":[],\"usage\":{\"total_tokens\":1}}\n\n", reason)
						if mode.done {
							_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
						}
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":%q}]}`, reason)
					}
				}))
				defer server.Close()
				llm := newOpenAICompat(Config{Provider: "openai-compatible", Model: "test", BaseURL: server.URL, HTTPClient: server.Client()}, "token")
				var final *model.Response
				for event, err := range llm.Generate(t.Context(), &model.Request{Stream: mode.stream, Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")}}) {
					if err != nil {
						t.Fatal(err)
					}
					if event != nil && event.Response != nil {
						final = event.Response
					}
				}
				if final == nil {
					t.Fatal("missing final response")
				}
				// Presence must mirror what the endpoint reported: an omitted
				// finish_reason is unreported, not an explicit unknown.
				if reason == "" {
					if final.FinishReasonReported {
						t.Error("missing finish_reason must stay unreported")
					}
				} else {
					assertFinishReasonReported(t, final)
				}
				want := model.FinishReasonUnknown
				if reason == "length" {
					want = model.FinishReasonLength
				}
				if final.FinishReason != want || final.RawFinishReason != reason {
					t.Errorf("finish = %q/%q, want %q/%q", final.FinishReason, final.RawFinishReason, want, reason)
				}
			})
		}
	}
}

func TestResponsesFinishReasonEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, events, raw, text string
		want                    model.FinishReason
	}{
		{"refusal output", `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"declined"}]}]}}`, "", "declined", model.FinishReasonRefusal},
		{"refusal delta", "data: {\"type\":\"response.refusal.delta\",\"delta\":\"declined\",\"output_index\":0}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}", "", "declined", model.FinishReasonRefusal},
		{"incomplete missing details", `data: {"type":"response.incomplete","response":{"status":"incomplete"}}`, "incomplete", "", model.FinishReasonUnknown},
		{"incomplete with tool", `data: {"type":"response.incomplete","response":{"status":"incomplete","output":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}]}}`, "incomplete", "", model.FinishReasonUnknown},
		{"incomplete empty details", `data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{}}}`, "incomplete", "", model.FinishReasonUnknown},
		{"incomplete event without status", `data: {"type":"response.incomplete","response":{}}`, "incomplete", "", model.FinishReasonUnknown},
		{"length", `data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`, "max_output_tokens", "", model.FinishReasonLength},
		{"content filter", `data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}}`, "content_filter", "", model.FinishReasonContentFilter},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var final *model.Response
			readResponsesStream(t.Context(), io.NopCloser(strings.NewReader(tc.events+"\n\n")), &model.Request{Stream: true}, responsesStreamConfig{}, func(event *model.StreamEvent, err error) bool {
				if err != nil {
					t.Fatal(err)
				}
				if event != nil && event.Response != nil {
					final = event.Response
				}
				return true
			})
			if final == nil {
				t.Fatal("missing final response")
			}
			assertFinishReasonReported(t, final)
			if final.FinishReason != tc.want || final.RawFinishReason != tc.raw || final.Message.TextContent() != tc.text {
				t.Errorf("finish/text = %q/%q/%q, want %q/%q/%q", final.FinishReason, final.RawFinishReason, final.Message.TextContent(), tc.want, tc.raw, tc.text)
			}
		})
	}
}

func TestOpenAIResponsesJSONFinishReasonEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, wire, raw string
		want            model.FinishReason
	}{
		{"refusal", `{"status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"declined"}]}]}`, "", model.FinishReasonRefusal},
		{"incomplete", `{"status":"incomplete","output":[]}`, "incomplete", model.FinishReasonUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, tc.wire)
			}))
			defer server.Close()
			final, _, _, err := collectOpenAICodexTestResponse(newOpenAIResponsesTestLLM(server, APIOpenAIResponses), &model.Request{
				Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
			})
			if err != nil {
				t.Fatal(err)
			}
			if final == nil {
				t.Fatal("missing final response")
			}
			assertFinishReasonReported(t, final)
			if final.FinishReason != tc.want || final.RawFinishReason != tc.raw {
				t.Errorf("finish = %q/%q, want %q/%q", final.FinishReason, final.RawFinishReason, tc.want, tc.raw)
			}
		})
	}
}

func TestNativeFinalFinishReasonReported(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, provider := range []string{"anthropic", "minimax", "gemini", "ollama"} {
			t.Run(fmt.Sprintf("%s/stream=%v", provider, stream), func(t *testing.T) {
				server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch provider {
					case "anthropic", "minimax":
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"test\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"refusal\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
						} else {
							_, _ = fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"test","content":[{"type":"text","text":"declined"}],"stop_reason":"refusal","usage":{"input_tokens":1,"output_tokens":1}}`)
						}
					case "gemini":
						payload := `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = fmt.Fprint(w, "data: "+payload+"\n\n")
						} else {
							_, _ = fmt.Fprint(w, payload)
						}
					case "ollama":
						_, _ = fmt.Fprintln(w, `{"model":"test","message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop"}`)
					}
				}))
				defer server.Close()
				cfg := Config{Provider: provider, Model: "test", BaseURL: server.URL, HTTPClient: server.Client()}
				var llm model.LLM
				switch provider {
				case "anthropic", "minimax":
					llm = newAnthropic(cfg, "token")
				case "gemini":
					llm = newGemini(cfg, "token")
				case "ollama":
					llm = newOllama(cfg, "")
				}
				var final *model.Response
				for event, err := range llm.Generate(t.Context(), &model.Request{Stream: stream, Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")}}) {
					if err != nil {
						t.Fatal(err)
					}
					if event != nil && event.Response != nil {
						final = event.Response
					}
				}
				if final == nil {
					t.Fatal("missing final response")
				}
				assertFinishReasonReported(t, final)
				if (provider == "anthropic" || provider == "minimax") && (final.FinishReason != model.FinishReasonRefusal || final.RawFinishReason != "refusal") {
					t.Errorf("finish = %q/%q, want refusal/refusal", final.FinishReason, final.RawFinishReason)
				}
			})
		}
	}
}
