package gatewayapp

import (
	"context"
	"encoding/json"
	"io"
	"iter"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func TestSystemAgentReasoningWrapperPreservesChatToolRepair(t *testing.T) {
	var requests, repairs, dispatches int
	client := &http.Client{Transport: gatewayAppRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body struct {
			Input      json.RawMessage
			ToolChoice string `json:"tool_choice"`
			Reasoning  struct{ Effort string }
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return nil, err
		}
		if body.Reasoning.Effort != "high" || body.ToolChoice != "auto" {
			t.Errorf("request effort = %q, tool choice = %q", body.Reasoning.Effort, body.ToolChoice)
		}
		if strings.Contains(string(body.Input), "None of its calls were dispatched") {
			repairs++
		}
		requests++
		output := `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}`
		switch requests {
		case 1, 2:
			output = `{"type":"function_call","call_id":"malformed","name":"Read","arguments":"{"}`
		case 3:
			output = `{"type":"function_call","call_id":"corrected","name":"Read","arguments":"{}"}`
		}
		data := "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[" + output + "]}}\n\n"
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(data))}, nil
	})}
	inner, err := providers.New(providers.Config{
		API: model.APIOpenAI, Model: "gpt-5.4",
		Auth: providers.AuthConfig{Type: model.AuthAPIKey, Token: "fixture"}, HTTPClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	llm := withSystemAgentReasoningEffort(kernel.ModelResolution{Model: inner, ReasoningEffort: "high"})
	public := &model.Request{
		Stream: true, Messages: []model.Message{model.NewTextMessage(model.RoleUser, "public SDK request")},
		Tools: []model.ToolSpec{model.NewFunctionToolSpec("Read", "fixture tool", nil)},
	}
	var failure error
	for event, err := range model.Generate(t.Context(), llm, public) {
		if event != nil && event.Response != nil {
			t.Fatal("public model.Generate accepted malformed JSON through the wrapper")
		}
		if err != nil {
			failure = err
		}
	}
	if errorcode.CodeOf(failure) != errorcode.InvalidArgument || !strings.Contains(failure.Error(), "tool arguments are not valid JSON") {
		t.Fatalf("public model.Generate error = %v", failure)
	}
	probe := tool.NamedTool{Def: tool.Definition{Name: "Read"}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		dispatches++
		return tool.Result{}, tool.NewError(tool.ErrorCodeNotFound, "fixture file is missing")
	}}
	parent := session.Session{SessionRef: session.SessionRef{AppName: "caelis", UserID: "owner", SessionID: "parent", WorkspaceKey: "fixture"}}
	result, err := newSystemManagedAgentRuntime(nil).Run(t.Context(), systemManagedAgentRunRequest{
		AgentID: guardianSceneID, Model: llm, ParentSession: parent, CapabilityProfile: systemManagedAgentCapabilityReadOnly,
		Tools: []tool.Tool{probe}, Input: "Read a synthetic file.",
	})
	if err != nil || result.Text != "done" || requests != 4 || repairs != 1 || dispatches != 1 {
		t.Fatalf("text = %q, requests = %d, repairs = %d, dispatches = %d, error = %v", result.Text, requests, repairs, dispatches, err)
	}
}

func TestProductionReasoningWrapperCountsHTTPAttempts(t *testing.T) {
	for _, mode := range []string{"success", "retry", "malformed", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var calls atomic.Int32
			client := &http.Client{Transport: gatewayAppRoundTripFunc(func(*http.Request) (*http.Response, error) {
				call := calls.Add(1)

				data := "data: {\"choices\":[],\"usage\":{\"total_tokens\":12}}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"
				if mode == "retry" && call == 1 {
					// A lost stream after reported usage is recoverable; malformed
					// protocol data is not a transport failure.
					data = "data: {\"choices\":[],\"usage\":{\"total_tokens\":12}}\n\n"
				}
				if mode == "malformed" {
					data = "data: {\"choices\":[],\"usage\":{\"total_tokens\":12}}\n\ndata: {invalid}\n\n"
				}
				body := io.NopCloser(strings.NewReader(data))
				if mode == "cancel" {
					body = &reasoningCancelBody{Reader: strings.NewReader("data: {\"choices\":[],\"usage\":{\"total_tokens\":12}}\n\n"), cancel: cancel}
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body}, nil
			})}
			inner, err := providers.New(providers.Config{Provider: "openai-compatible", Model: "probe", API: model.APIOpenAICompatible, Auth: providers.AuthConfig{Type: model.AuthNone}, BaseURL: "https://provider.invalid", HTTPClient: client, Retry: model.RetryConfig{MaxRetries: 1, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond}})
			if err != nil {
				t.Fatal(err)
			}
			llm := withSystemAgentReasoningEffort(kernel.ModelResolution{Model: inner, ReasoningEffort: "high"})
			var receipts []model.Invocation
			ctx = model.WithInvocationObserver(ctx, func(in model.Invocation) { receipts = append(receipts, in) })
			var failure error
			for _, err := range model.Generate(ctx, llm, &model.Request{Stream: true}) {
				failure = err
				if mode != "cancel" && mode != "malformed" && err != nil {
					t.Fatal(err)
				}
			}
			if mode == "malformed" && failure == nil {
				t.Fatal("malformed protocol response unexpectedly succeeded")
			}
			total := 0
			for _, in := range receipts {
				total += in.Usage.TotalTokens
			}
			wantCalls, wantTokens := 1, 12
			if mode == "retry" {
				wantCalls = 2
				wantTokens = 24
			}
			if int(calls.Load()) != wantCalls || len(receipts) != wantCalls || total != wantTokens {
				t.Fatalf("HTTP=%d receipts=%d tokens=%d; want %d/%d/%d", calls.Load(), len(receipts), total, wantCalls, wantCalls, wantTokens)
			}
		})
	}
}

type reasoningCancelBody struct {
	*strings.Reader
	cancel context.CancelFunc
}

func (b *reasoningCancelBody) Read(p []byte) (int, error) {
	if b.Len() > 0 {
		return b.Reader.Read(p)
	}
	b.cancel()
	return 0, context.Canceled
}
func (*reasoningCancelBody) Close() error { return nil }

type reasoningDirectProvider struct {
	calls  int
	effort string
}

func (*reasoningDirectProvider) Name() string { return "direct" }
func (m *reasoningDirectProvider) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		m.calls++
		if req != nil {
			m.effort = req.Reasoning.Effort
		}
		yield(model.StreamEventFromResponse(&model.Response{TurnComplete: true, Usage: model.Usage{TotalTokens: 12}}), nil)
	}
}
func TestReasoningWrapperTracksDirectProviderAndNilRequest(t *testing.T) {
	for _, effort := range []string{"", "high"} {
		for _, nilRequest := range []bool{false, true} {
			direct := &reasoningDirectProvider{}
			llm := withSystemAgentReasoningEffort(kernel.ModelResolution{Model: direct, ReasoningEffort: effort})
			var receipts []model.Invocation
			ctx := model.WithInvocationObserver(t.Context(), func(in model.Invocation) { receipts = append(receipts, in) })
			var req *model.Request
			if !nilRequest {
				req = &model.Request{}
			}
			for _, err := range model.Generate(ctx, llm, req) {
				if err != nil {
					t.Fatal(err)
				}
			}
			if direct.calls != 1 || len(receipts) != 1 || receipts[0].Usage.TotalTokens != 12 {
				t.Fatalf("effort=%q nil=%v calls=%d receipts=%#v", effort, nilRequest, direct.calls, receipts)
			}
			if !nilRequest && (direct.effort != effort || req.Reasoning.Effort != "") {
				t.Fatalf("effort forwarding mutated source request: %#v", req)
			}
		}
	}
}
