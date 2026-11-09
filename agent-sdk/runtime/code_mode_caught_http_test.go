package runtime

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

// This is real JS/Runtime/native Read and real loopback HTTP transport, not a
// live model. Both failed reads must retain their receipts and summary output.
func TestCodeModeCaughtReadsReachNextHTTPRequest(t *testing.T) {
	store, active, root := codeModeStore(t)
	sandbox, err := host.New(host.Config{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.Close()
	read, err := filesystem.NewRead(filesystem.DefaultReadConfig(), sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "good.txt"), []byte("VALID_READ_RESULT"), 0600); err != nil {
		t.Fatal(err)
	}
	paths, _ := json.Marshal([]string{root, "missing.txt", "good.txt"})
	code := `const errors=[], values=[]; for(const path of ` + string(paths) + `) {try {const r=await tools.call("Read",{path,limit:1});values.push(r.parts[0].json.content);}catch(e){errors.push(e.message);}} return {errors,values,done:true};`
	args, _ := json.Marshal(map[string]any{"code": code})
	var mu sync.Mutex
	var requests [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, e := io.ReadAll(r.Body)
		if e != nil {
			t.Error(e)
			return
		}
		mu.Lock()
		requests = append(requests, raw)
		n := len(requests)
		mu.Unlock()
		if n > 2 {
			t.Error("unexpected extra request")
			http.Error(w, "extra", 500)
			return
		}
		output := []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "summary received"}}}}
		if n == 1 {
			output = []any{map[string]any{"type": "function_call", "name": "js_exec", "call_id": "caught-reads", "arguments": string(args)}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "object": "response", "model": "fixture", "status": "completed", "output": output, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}})
	}))
	defer server.Close()
	factory := providers.NewFactory()
	if err = factory.Register(providers.Config{Alias: "fixture/catch", Provider: "fixture", API: providers.APIOpenAIResponses, Model: "fixture", BaseURL: server.URL, HTTPClient: server.Client(), Auth: providers.AuthConfig{Type: providers.AuthNone}, ContextWindowTokens: 200000}); err != nil {
		t.Fatal(err)
	}
	llm, err := factory.NewByAlias("fixture/catch")
	if err != nil {
		t.Fatal(err)
	}
	runCodeMode(t, store, active, agent.AgentSpec{Model: llm, Tools: []tool.Tool{read, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}, Sandbox: sandbox}}}, nil, nil)
	mu.Lock()
	wire := append([][]byte(nil), requests...)
	mu.Unlock()
	if len(wire) != 2 {
		t.Fatalf("requests=%d", len(wire))
	}
	for _, text := range []string{"VALID_READ_RESULT", "invalid_input", "not_found", `done`, `true`} {
		if !bytes.Contains(wire[1], []byte(text)) {
			t.Fatalf("missing %q in next HTTP request: %s", text, wire[1])
		}
	}
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	failed, succeeded, parentFailed := 0, 0, false
	for _, ev := range events {
		if ev.Journal != nil && ev.Journal.ToolExecution != nil {
			j := ev.Journal.ToolExecution
			if j.Parent != nil && j.ToolName == "Read" {
				if j.Status == session.ToolExecutionFailed {
					failed++
				}
				if j.Status == session.ToolExecutionSucceeded {
					succeeded++
				}
			}
		}
		if ev.Message != nil {
			for _, part := range ev.Message.Parts {
				if part.ToolResult != nil && part.ToolResult.Name == codemode.ToolName {
					parentFailed = part.ToolResult.IsError
				}
			}
		}
	}
	if failed != 2 || succeeded != 1 || !parentFailed {
		t.Fatalf("failed=%d succeeded=%d parentError=%v", failed, succeeded, parentFailed)
	}
	if dir := os.Getenv("CAELIS_CODE_MODE_CATCH_ARTIFACTS"); dir != "" {
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for i, raw := range wire {
			if err = os.WriteFile(filepath.Join(dir, []string{"first.request.json", "next.request.json"}[i]), raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
		_ = os.WriteFile(filepath.Join(dir, "script.js"), []byte(code), 0600)
		raw, _ := json.MarshalIndent(events, "", "  ")
		_ = os.WriteFile(filepath.Join(dir, "events.json"), raw, 0600)
	}
}
