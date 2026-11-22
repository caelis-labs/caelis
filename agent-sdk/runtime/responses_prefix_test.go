package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestResponsesPrefixThroughRepairToolFailureAndRecovery(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			testResponsesPrefixThroughRepair(t, stream)
		})
	}
}

func testResponsesPrefixThroughRepair(t *testing.T, stream bool) {
	t.Helper()
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "prefix", UserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	type wire struct {
		Input []json.RawMessage `json:"input"`
		Tools json.RawMessage   `json:"tools"`
		Key   string            `json:"prompt_cache_key"`
	}
	var requests []wire
	transport := compactionTerminalTransport(func(req *http.Request) (*http.Response, error) {
		var body wire
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return nil, err
		}
		requests = append(requests, body)
		output := `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}`
		switch len(requests) {
		case 1:
			output = `{"type":"function_call","call_id":"malformed","name":"Read","arguments":"{"}`
		case 2:
			output = `{"type":"function_call","call_id":"dispatched","name":"Read","arguments":"{}"}`
		}
		payload := `{"status":"completed","output":[` + output + `]}`
		if stream {
			payload = `data: {"type":"response.completed","response":` + payload + "}\n\n"
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(payload))}, nil
	})
	llm, err := providers.New(providers.Config{
		API: model.APIOpenAI, Model: "gpt-6.2-sol",
		Auth:       providers.AuthConfig{Type: model.AuthAPIKey, Token: "fixture"},
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
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
	for _, input := range []string{"initial turn", "resume turn", "follow-up turn"} {
		reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
		rt, err := New(Config{Sessions: reopened, AgentFactory: chat.Factory{SystemPrompt: "stable base"}, Hooks: hooks})
		if err != nil {
			t.Fatal(err)
		}
		run, err := rt.Run(t.Context(), agent.RunRequest{
			SessionRef: active.SessionRef, Input: input, AgentSpec: agent.AgentSpec{
				Model: llm, Tools: []tool.Tool{target}, Request: agent.ModelRequestOptions{Stream: &stream},
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
	if len(requests) != 5 || dispatched.Load() != 1 {
		t.Fatalf("requests=%d tool dispatches=%d", len(requests), dispatched.Load())
	}
	first, repair, result, resume, followup := requests[0], requests[1], requests[2], requests[3], requests[4]
	assertPrefix := func(previous, next wire) {
		t.Helper()
		if len(next.Input) <= len(previous.Input) || !slices.EqualFunc(previous.Input, next.Input[:len(previous.Input)], func(a, b json.RawMessage) bool { return bytes.Equal(a, b) }) {
			t.Fatalf("previous input %s is not a prefix of %s", previous.Input, next.Input)
		}
	}
	assertPrefix(first, repair)
	assertPrefix(first, result)
	assertPrefix(result, resume)
	assertPrefix(resume, followup)
	if len(repair.Input) != len(first.Input)+1 || !strings.Contains(string(repair.Input[len(first.Input)]), `"role":"developer"`) ||
		!strings.Contains(string(repair.Input[len(first.Input)]), "None of its calls were dispatched") {
		t.Fatalf("repair did not append one instruction: %s", repair.Input)
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
	for i, req := range requests {
		if !strings.Contains(string(req.Input[0]), `stable base\nfixed Skills`) || req.Key != active.SessionID ||
			!bytes.Equal(req.Tools, first.Tools) {
			t.Fatalf("request %d changed stable assembly", i+1)
		}
		for _, item := range req.Input {
			if bytes.Contains(item, []byte(`"call_id":"malformed"`)) {
				t.Fatalf("request %d replayed malformed output", i+1)
			}
		}
	}
}
