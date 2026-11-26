package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRuntimeGeminiStreamCitationReplay(t *testing.T) {
	const wire = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"你好"}]},"groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://first.test","title":"第一"}}],"groundingSupports":[{"segment":{"endIndex":6},"groundingChunkIndices":[0]}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":" "}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"café 世界"}]},"groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://second.test","title":"Café"}}],"groundingSupports":[{"segment":{"endIndex":5},"groundingChunkIndices":[0,1]},{"segment":{"startIndex":6,"endIndex":12},"groundingChunkIndices":[2]}]}}]}

data: {"candidates":[{"groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://third.test","title":"世界"}}]},"finishReason":"STOP"}]}

`
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "citations", UserID: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var providerPrefixes []json.RawMessage
	llm, err := providers.New(providers.Config{API: model.APIGemini, Model: "fixture", BaseURL: "http://provider.invalid", Auth: providers.AuthConfig{Token: "fixture"},
		HTTPClient: &http.Client{Transport: compactionTerminalTransport(func(req *http.Request) (*http.Response, error) {
			payload := wire
			if calls.Add(1) > 1 {
				var body struct{ Contents []json.RawMessage }
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					return nil, err
				}
				if len(body.Contents) < 2 || !strings.Contains(string(body.Contents[1]), "你好 café 世界") {
					return nil, fmt.Errorf("missing merged assistant text on replay: %s", body.Contents)
				}
				providerPrefixes = append(providerPrefixes, body.Contents[1])
				payload = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"continued"}]},"finishReason":"STOP"}]}` + "\n\n"
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(payload)), Request: req}, nil
		})}})
	if err != nil {
		t.Fatal(err)
	}
	makeRuntime := func(store session.ManagedService) *Runtime {
		t.Helper()
		rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
		if err != nil {
			t.Fatal(err)
		}
		return rt
	}
	var requests [][]model.Message
	ctx := model.WithInvocationStart(t.Context(), func(_ context.Context, _ model.Invocation, request *model.Request) error {
		requests = append(requests, model.CloneMessages(request.Messages))
		return nil
	})
	runTurn := func(rt *Runtime, input string) {
		t.Helper()
		stream := true
		run, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: input,
			AgentSpec: agent.AgentSpec{Model: llm, Request: agent.ModelRequestOptions{Stream: &stream}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := run.Handle.WaitCompletion(ctx); err != nil {
			t.Fatal(err)
		}
	}
	rt := makeRuntime(store)
	runTurn(rt, "produce citations")
	runTurn(rt, "continue before reopen")
	runTurn(makeRuntime(sessionfile.NewStore(sessionfile.Config{RootDir: root})), "continue after reopen")
	first := model.CitationSource{RefID: "grounding-0", URL: "https://first.test", Title: "第一"}
	second := model.CitationSource{RefID: "grounding-1", URL: "https://second.test", Title: "Café"}
	third := model.CitationSource{RefID: "grounding-2", URL: "https://third.test", Title: "世界"}
	want := model.NewTextPartWithCitations("你好 café 世界", []model.Citation{
		{StartIndex: 0, EndIndex: 6, Sources: []model.CitationSource{first}},
		{StartIndex: 7, EndIndex: 12, Sources: []model.CitationSource{first, second}},
		{StartIndex: 13, EndIndex: 19, Sources: []model.CitationSource{third}},
	})
	if len(requests) != 3 || len(requests[1]) < 2 || len(requests[2]) < len(requests[1]) {
		t.Fatalf("expected three actual requests with live and reopened context: %+v", requests)
	}
	if !reflect.DeepEqual(requests[1][1].Parts, []model.Part{want}) {
		got, _ := json.Marshal(requests[1][1])
		t.Fatalf("live assistant message lost text or citations: %s", got)
	}
	if !reflect.DeepEqual(requests[1], requests[2][:len(requests[1])]) || len(providerPrefixes) != 2 || string(providerPrefixes[0]) != string(providerPrefixes[1]) {
		t.Fatal("model context or provider wire prefix changed after file Store reopen")
	}
}

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

func TestProviderIncrementalTextWithReasoning(t *testing.T) {
	for _, tc := range []struct {
		api  model.APIType
		wire string
	}{
		{model.APIOllama, `{"message":{"role":"assistant","thinking":"think"},"done":false}
{"message":{"role":"assistant","content":"hello"},"done":true,"done_reason":"stop"}
`},
		{model.APIOpenAICompatible, `data: {"choices":[{"delta":{"content":"hello"}}]}

data: {"choices":[{"delta":{"reasoning_content":"think"},"finish_reason":"stop"}]}

data: [DONE]

`},
	} {
		t.Run(string(tc.api), func(t *testing.T) {
			transport := compactionTerminalTransport(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.wire)), Request: req}, nil
			})
			llm, err := providers.New(providers.Config{Model: "fixture", API: tc.api, BaseURL: "http://fixture.invalid", Auth: providers.AuthConfig{Type: model.AuthNone}, HTTPClient: &http.Client{Transport: transport}})
			if err != nil {
				t.Fatal(err)
			}
			store, active := newTestSessionService(t, "provider-stream")
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hook.Set{StreamMode: hook.StreamIncremental}})
			if err != nil {
				t.Fatal(err)
			}
			enabled := true
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "hello", AgentSpec: agent.AgentSpec{Model: llm, Request: agent.ModelRequestOptions{Stream: &enabled}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if event.Type == session.EventTypeAssistant && event.Message != nil {
					if event.Message.TextContent() != "hello" || event.Message.ReasoningText() != "think" {
						t.Fatalf("assistant parts: %+v", event.Message.Parts)
					}
					return
				}
			}
			t.Fatal("missing final assistant message")
		})
	}
}

type responsesPrefixRoute struct {
	name    string
	api     model.APIType
	baseURL string
	stream  bool
	ordered bool
}

func TestResponsesPrefixThroughRepairToolFailureAndRecovery(t *testing.T) {
	for _, route := range []responsesPrefixRoute{
		{name: "official-responses-json", api: model.APIOpenAI, ordered: true},
		{name: "compatible-responses-sse", api: model.APIOpenAIResponses, baseURL: "https://compatible.invalid/v1", stream: true},
		{name: "codex-sse", api: model.APIOpenAICodex, stream: true, ordered: true},
		{name: "xai-sse", api: model.APIXAIResponses, stream: true},
	} {
		t.Run(route.name, func(t *testing.T) {
			testResponsesPrefixThroughRepair(t, route)
		})
	}
}

func testResponsesPrefixThroughRepair(t *testing.T, route responsesPrefixRoute) {
	t.Helper()
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "prefix", UserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	type wire struct {
		Input        []json.RawMessage `json:"input"`
		Tools        json.RawMessage   `json:"tools"`
		Key          string            `json:"prompt_cache_key"`
		Instructions string            `json:"instructions"`
	}
	var requests []wire
	publicProbe := true
	transport := compactionTerminalTransport(func(req *http.Request) (*http.Response, error) {
		var body wire
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return nil, err
		}
		requests = append(requests, body)
		output := `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}`
		switch {
		case publicProbe || len(requests) == 2:
			output = `{"type":"function_call","call_id":"malformed","name":"Read","arguments":"{"}`
		case len(requests) == 3:
			output = `{"type":"function_call","call_id":"dispatched","name":"Read","arguments":"{}"}`
		}
		payload := `{"status":"completed","output":[` + output + `]}`
		if route.stream {
			payload = `data: {"type":"response.completed","response":` + payload + "}\n\n"
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(payload))}, nil
	})
	auth := providers.AuthConfig{Type: model.AuthAPIKey, Token: "fixture"}
	if route.api == model.APIOpenAICodex || route.api == model.APIXAIResponses {
		auth = providers.AuthConfig{Type: model.AuthOAuthToken}
	}
	llm, err := providers.New(providers.Config{
		API: route.api, Model: "gpt-6.2-sol", BaseURL: route.baseURL,
		Auth:       auth,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, disabled := range []bool{false, true} {
		public := &model.Request{
			Stream: route.stream, DisableTools: disabled,
			Messages: []model.Message{model.NewTextMessage(model.RoleUser, "public SDK request")},
			Tools:    []model.ToolSpec{model.NewFunctionToolSpec("Read", "fixture tool", nil)},
		}
		var failure error
		for event, err := range model.Generate(t.Context(), llm, public) {
			if event != nil && event.Response != nil {
				t.Fatalf("public model.Generate accepted malformed JSON (tools disabled: %t)", disabled)
			}
			if err != nil {
				failure = err
			}
		}
		if errorcode.CodeOf(failure) != errorcode.InvalidArgument || !strings.Contains(failure.Error(), "tool arguments are not valid JSON") {
			t.Fatalf("public model.Generate error = %v (tools disabled: %t)", failure, disabled)
		}
	}
	publicProbe = false
	requests = nil

	var dispatched atomic.Int32
	target := tool.NamedTool{Def: tool.Definition{Name: "Read"}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		dispatched.Add(1)
		return tool.Result{}, tool.NewError(tool.ErrorCodeNotFound, "fixture read failed")
	}}
	var updated atomic.Bool
	hooks := hook.Set{ModelPrepare: []hook.ModelPrepare{{
		Name: "append-context", Apply: func(_ context.Context, e hook.PrepareContext, p hook.ModelPreparation) (hook.ModelPreparation, error) {
			last := p.Request.Messages[len(p.Request.Messages)-1]
			if last.TextContent() == "resume turn" && updated.CompareAndSwap(false, true) {
				p.ContextEdit = &hook.ContextEdit{Source: e.ContextSource, Append: []model.Part{model.NewTextPart("durable context update")}}
			}
			return p, nil
		},
	}}}
	for _, input := range []string{"seed stable history", "initial turn", "resume turn", "follow-up turn"} {
		reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
		rt, err := New(Config{Sessions: reopened, AgentFactory: chat.Factory{SystemPrompt: "stable base"}, Hooks: hooks})
		if err != nil {
			t.Fatal(err)
		}
		run, err := rt.Run(t.Context(), agent.RunRequest{
			SessionRef: active.SessionRef, Input: input, AgentSpec: agent.AgentSpec{
				Model: llm, Tools: []tool.Tool{target}, Request: agent.ModelRequestOptions{Stream: &route.stream},
				ResolveSessionInstructions: func(context.Context, string) (string, func(context.Context) error, error) {
					return "fixed Skills", nil, nil
				},
			},
		})
		if err == nil {
			err = run.Handle.WaitCompletion(t.Context())
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(requests) != 6 || dispatched.Load() != 1 {
		t.Fatalf("requests=%d tool dispatches=%d", len(requests), dispatched.Load())
	}
	seed, first, repair, result, resume, followup := requests[0], requests[1], requests[2], requests[3], requests[4], requests[5]
	assertPrefix := func(previous, next wire) {
		t.Helper()
		if len(next.Input) <= len(previous.Input) || !slices.EqualFunc(previous.Input, next.Input[:len(previous.Input)], func(a, b json.RawMessage) bool { return bytes.Equal(a, b) }) {
			t.Fatalf("previous input %s is not a prefix of %s", previous.Input, next.Input)
		}
	}
	assertPrefix(seed, first)
	assertPrefix(first, result)
	assertPrefix(result, resume)
	assertPrefix(resume, followup)
	// The repair-only tail may disappear. Both requests must retain the complete
	// input that was already stable before the malformed proposal arrived.
	const repairMarker = "None of its calls were dispatched"
	if route.ordered {
		assertPrefix(first, repair)
		if len(repair.Input) != len(first.Input)+1 ||
			!bytes.Contains(repair.Input[len(first.Input)], []byte(`"role":"developer"`)) ||
			!bytes.Contains(repair.Input[len(first.Input)], []byte(repairMarker)) ||
			bytes.Equal(repair.Input[len(first.Input)], result.Input[len(first.Input)]) {
			t.Fatalf("expected first repair-to-normal difference at input[%d]: repair=%s normal=%s", len(first.Input), repair.Input, result.Input)
		}
		t.Logf("repair → normal first differs at input[%d] (zero-based): temporary developer instruction → function_call. All %d preceding items match.", len(first.Input), len(first.Input))
	} else {
		if !slices.EqualFunc(first.Input, repair.Input, func(a, b json.RawMessage) bool { return bytes.Equal(a, b) }) ||
			!strings.HasPrefix(repair.Instructions, first.Instructions+"\n") ||
			!strings.Contains(repair.Instructions, repairMarker) || result.Instructions != first.Instructions {
			t.Fatal("expected repair-only change in top-level instructions, with stable input intact")
		}
		t.Logf("repair → normal first differs in top-level instructions at byte %d (zero-based). All %d previously established input items match.", len(first.Instructions), len(first.Input))
	}
	if len(result.Input) != len(first.Input)+2 ||
		!strings.Contains(string(result.Input[len(first.Input)]), `"call_id":"dispatched"`) ||
		!strings.Contains(string(result.Input[len(first.Input)+1]), `"type":"function_call_output"`) ||
		!strings.Contains(string(result.Input[len(first.Input)+1]), "fixture read failed") {
		t.Fatalf("tool error did not append a call/result pair: %s", result.Input)
	}
	if !strings.Contains(string(resume.Input[len(resume.Input)-1]), "durable context update") {
		t.Fatalf("lost the durable update: %s", resume.Input)
	}
	expectedKey := active.SessionID
	if route.api == model.APIOpenAIResponses {
		expectedKey = ""
	}
	for i, req := range requests {
		if req.Key != expectedKey || !bytes.Equal(req.Tools, first.Tools) {
			t.Fatalf("request %d changed stable affinity or tools", i+1)
		}
		if route.ordered {
			if req.Instructions != "" || !bytes.Contains(req.Input[0], []byte(`stable base\nfixed Skills`)) {
				t.Fatalf("request %d changed the stable developer prefix", i+1)
			}
		} else if i != 2 && req.Instructions != "stable base\nfixed Skills" {
			t.Fatalf("request %d changed stable instructions: %q", i+1, req.Instructions)
		}
		for _, item := range req.Input {
			if bytes.Contains(item, []byte(`"call_id":"malformed"`)) {
				t.Fatalf("request %d replayed malformed output", i+1)
			}
		}
	}
}
