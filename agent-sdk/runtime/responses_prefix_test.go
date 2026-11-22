package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

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
	hooks := agent.Hooks{ModelPrepare: []agent.Hook[agent.ModelPreparation, agent.ModelPreparation]{{
		Name: "append-context", Apply: func(_ context.Context, e agent.HookContext, p agent.ModelPreparation) (agent.ModelPreparation, error) {
			last := p.Request.Messages[len(p.Request.Messages)-1]
			if last.TextContent() == "resume turn" && updated.CompareAndSwap(false, true) {
				p.ContextEdit = &agent.ContextEdit{Source: e.ContextSource, Append: []model.Part{model.NewTextPart("durable context update")}}
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
