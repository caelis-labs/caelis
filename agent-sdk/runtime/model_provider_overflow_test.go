package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestRuntimeProviderOverflowRecoversAfterCommittedTool(t *testing.T) {
	const receipt = "durable effect receipt"
	const resumed = "recovered after provider overflow"
	for _, api := range []model.APIType{model.APIAnthropic, model.APIGemini} {
		for _, inBand := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/in-band=%v", api, inBand), func(t *testing.T) {
				store, active := newTestSessionService(t, "provider-overflow")
				var calls, effects atomic.Int32
				client := &http.Client{Transport: compactionTerminalTransport(func(req *http.Request) (*http.Response, error) {
					request, err := io.ReadAll(req.Body)
					if err != nil {
						return nil, err
					}
					body := string(request)
					stream := strings.Contains(body, `"stream":true`) || strings.Contains(req.URL.Path, ":streamGenerateContent")
					switch attempt := calls.Add(1); attempt {
					case 1:
						return providerRecoverySuccess(req, api, stream, true, ""), nil
					case 2:
						if !strings.Contains(body, receipt) || effects.Load() != 1 {
							return nil, fmt.Errorf("overflow attempt missing prior effect: %s", body)
						}
						return providerRecoveryOverflow(req, api, inBand), nil
					case 3:
						compaction := strings.Contains(body, "CONTEXT COMPACTION SUMMARY") || strings.Contains(body, inContextCompactionPrompt)
						if !compaction || !strings.Contains(body, receipt) {
							return nil, fmt.Errorf("recovery compaction missing prior effect: %s", body)
						}
						return providerRecoverySuccess(req, api, stream, false, asyncFixtureSummary+" The tool completed with a "+receipt+"."), nil
					case 4:
						if !strings.Contains(body, "CONTEXT CHECKPOINT") || !strings.Contains(body, receipt) {
							return nil, fmt.Errorf("recovered request missing checkpoint or effect: %s", body)
						}
						return providerRecoverySuccess(req, api, stream, false, resumed), nil
					default:
						return nil, fmt.Errorf("unexpected provider attempt %d", attempt)
					}
				})}
				llm, err := providers.New(providers.Config{API: api, Provider: string(api), Model: "fixture", BaseURL: "http://provider.invalid",
					Auth: providers.AuthConfig{Token: "fixture"}, HTTPClient: client})
				if err != nil {
					t.Fatal(err)
				}
				effect := tool.NamedTool{Def: tool.Definition{Name: "ECHO", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
					effects.Add(1)
					return tool.Result{Content: []tool.ContentBlock{tool.Text(receipt)}}, nil
				}}
				rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Compaction: CompactionConfig{Enabled: true}})
				if err != nil {
					t.Fatal(err)
				}
				run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "Run ECHO and finish.",
					AgentSpec: agent.AgentSpec{Model: llm, Tools: []tool.Tool{effect}, Request: agent.ModelRequestOptions{Stream: &inBand}}})
				if err != nil {
					t.Fatal(err)
				}
				if err := run.Handle.WaitCompletion(t.Context()); err != nil {
					t.Fatal(err)
				}
				loaded, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
				if err != nil {
					t.Fatal(err)
				}
				var checkpoints, toolResults int
				var final string
				for _, event := range loaded.Events {
					switch event.Type {
					case session.EventTypeCompact:
						checkpoints++
					case session.EventTypeToolResult:
						toolResults++
					case session.EventTypeAssistant:
						final = session.EventText(event)
					}
				}
				if calls.Load() != 4 || effects.Load() != 1 || toolResults != 1 || checkpoints != 1 || final != resumed {
					t.Fatalf("calls=%d effects=%d results=%d checkpoints=%d final=%q", calls.Load(), effects.Load(), toolResults, checkpoints, final)
				}
			})
		}
	}
}

func providerCorrectnessResponse(req *http.Request, status int, contentType, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}
}

func providerRecoveryOverflow(req *http.Request, api model.APIType, inBand bool) *http.Response {
	if api == model.APIAnthropic {
		wire := `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 232005 tokens > 200000 maximum"}}`
		if inBand {
			return providerCorrectnessResponse(req, http.StatusOK, "text/event-stream", "event: error\ndata: "+wire+"\n\n")
		}
		return providerCorrectnessResponse(req, http.StatusBadRequest, "application/json", wire)
	}
	wire := `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"The input token count (1191140) exceeds the maximum number of tokens allowed (1048576)."}}`
	if inBand {
		return providerCorrectnessResponse(req, http.StatusOK, "text/event-stream", "data: "+wire+"\n\n")
	}
	return providerCorrectnessResponse(req, http.StatusBadRequest, "application/json", wire)
}

func providerRecoverySuccess(req *http.Request, api model.APIType, stream, proposeTool bool, text string) *http.Response {
	quote, _ := json.Marshal(text)
	if api == model.APIGemini {
		part := `{"text":` + string(quote) + `}`
		if proposeTool {
			part = `{"functionCall":{"id":"only-call","name":"ECHO","args":{}}}`
		}
		wire := `{"candidates":[{"content":{"role":"model","parts":[` + part + `]},"finishReason":"STOP"}]}`
		if stream {
			return providerCorrectnessResponse(req, http.StatusOK, "text/event-stream", "data: "+wire+"\n\n")
		}
		return providerCorrectnessResponse(req, http.StatusOK, "application/json", wire)
	}
	part, stop := `{"type":"text","text":`+string(quote)+`}`, `"end_turn"`
	if proposeTool {
		part, stop = `{"type":"tool_use","id":"only-call","name":"ECHO","input":{}}`, `"tool_use"`
	}
	if stream {
		wire := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"fixture\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"fixture\",\"content\":[],\"usage\":{\"input_tokens\":10}}}\n\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":" + part + "}\n\n" +
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":" + stop + "},\"usage\":{\"output_tokens\":5}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
		return providerCorrectnessResponse(req, http.StatusOK, "text/event-stream", wire)
	}
	wire := `{"id":"fixture","type":"message","role":"assistant","model":"fixture","content":[` + part + `],"stop_reason":` + stop + `,"usage":{"input_tokens":10,"output_tokens":5}}`
	return providerCorrectnessResponse(req, http.StatusOK, "application/json", wire)
}
