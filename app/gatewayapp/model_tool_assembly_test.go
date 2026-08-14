package gatewayapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/sendmessage"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
	"github.com/caelis-labs/caelis/surfaces/headless"
)

func TestProductAgentFinalModelRequestIncludesCanonicalTaskCommunicationTools(t *testing.T) {
	provider := newFinalToolAssemblyProvider(t)
	workdir := t.TempDir()
	stack, err := newGatewayAppTestStack(t, Config{
		AppName: "caelis", UserID: "final-tool-assembly", StoreDir: t.TempDir(),
		WorkspaceKey: workdir, WorkspaceCWD: workdir, ApprovalMode: "auto-review",
		SkillDirs: []string{t.TempDir()}, Sandbox: SandboxConfig{RequestedType: "host"},
		Model: ModelConfig{
			Provider: "openai-compatible", API: providers.APIOpenAICompatible,
			Model: "final-tool-assembly", BaseURL: provider.URL, HTTPClient: provider.Client(),
			Token: "test-token", AuthType: providers.AuthBearerToken,
			ContextWindowTokens: 128000, MaxOutputTok: 1024, Timeout: 5 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	defer stack.Close()
	activeSession, err := startGatewayAppTestSession(context.Background(), stack, "")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	result, err := runHeadlessOnceForGatewayAppTest(
		context.Background(), stack, activeSession, "final-tool-assembly", "inspect available tools", headless.Options{},
	)
	if err != nil {
		t.Fatalf("RunSessionOnce() error = %v", err)
	}
	if strings.TrimSpace(result.Output) != "ok" {
		t.Fatalf("RunSessionOnce() output = %q, want ok", result.Output)
	}

	functions := provider.Functions(t)
	for _, name := range []string{spawn.ToolName, tasktool.ToolName, sendmessage.ToolName} {
		if got := countFinalToolFunctions(functions, name); got != 1 {
			t.Fatalf("final model tools contain %d %s definitions, want exactly 1: %#v", got, name, functions)
		}
	}
	spawnFunction := finalToolFunction(t, functions, spawn.ToolName)
	spawnParameters := finalToolParameters(spawnFunction)
	spawnProperties, _ := spawnParameters["properties"].(map[string]any)
	agentProperty, _ := spawnProperties["agent"].(map[string]any)
	agents := stringSliceFromFinalToolValue(agentProperty["enum"])
	if !slices.Contains(agents, "self") || slices.Contains(stringSliceFromFinalToolValue(spawnParameters["required"]), "agent") {
		t.Fatalf("final Spawn schema agents/required = %#v/%#v, want optional self default", agents, spawnParameters["required"])
	}

	taskFunction := finalToolFunction(t, functions, tasktool.ToolName)
	assertFinalToolProperties(t, taskFunction, "action", "handle", "input")
	sendFunction := finalToolFunction(t, functions, sendmessage.ToolName)
	assertFinalToolProperties(t, sendFunction, "to", "message")
	sendParameters := finalToolParameters(sendFunction)
	if required := stringSliceFromFinalToolValue(sendParameters["required"]); !slices.Equal(required, []string{"to", "message"}) {
		t.Fatalf("final SendMessage required = %#v, want [to message]", required)
	}
}

type finalToolAssemblyProvider struct {
	*gatewayTestHTTPServer
	mu        sync.Mutex
	calls     int
	functions []map[string]any
}

func newFinalToolAssemblyProvider(t *testing.T) *finalToolAssemblyProvider {
	t.Helper()
	provider := &finalToolAssemblyProvider{}
	provider.gatewayTestHTTPServer = newGatewayTestHTTPServer(http.HandlerFunc(provider.handle))
	t.Cleanup(provider.Close)
	return provider
}

func (p *finalToolAssemblyProvider) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/chat/completions" {
		http.NotFound(w, r)
		return
	}
	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.calls++
	for _, raw := range anySliceFromFinalToolValue(payload["tools"]) {
		entry, _ := raw.(map[string]any)
		function, _ := entry["function"].(map[string]any)
		if function != nil {
			p.functions = append(p.functions, function)
		}
	}
	p.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	writePluginSystemE2ESSE(w, map[string]any{
		"id": "final-tool-assembly-1", "object": "chat.completion.chunk", "model": "final-tool-assembly",
		"choices": []map[string]any{{
			"index": 0, "delta": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop",
		}},
	})
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

func (p *finalToolAssemblyProvider) Functions(t *testing.T) []map[string]any {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", p.calls)
	}
	return append([]map[string]any(nil), p.functions...)
}

func finalToolFunction(t *testing.T, functions []map[string]any, name string) map[string]any {
	t.Helper()
	for _, function := range functions {
		if function["name"] == name {
			return function
		}
	}
	t.Fatalf("final model tools missing %s: %#v", name, functions)
	return nil
}

func countFinalToolFunctions(functions []map[string]any, name string) int {
	count := 0
	for _, function := range functions {
		if function["name"] == name {
			count++
		}
	}
	return count
}

func assertFinalToolProperties(t *testing.T, function map[string]any, names ...string) {
	t.Helper()
	properties, _ := finalToolParameters(function)["properties"].(map[string]any)
	for _, name := range names {
		if properties[name] == nil {
			t.Fatalf("final %s schema missing property %s: %#v", function["name"], name, properties)
		}
	}
}

func finalToolParameters(function map[string]any) map[string]any {
	parameters, _ := function["parameters"].(map[string]any)
	return parameters
}

func anySliceFromFinalToolValue(value any) []any {
	values, _ := value.([]any)
	return values
}

func stringSliceFromFinalToolValue(value any) []string {
	values := anySliceFromFinalToolValue(value)
	out := make([]string, 0, len(values))
	for _, value := range values {
		text, _ := value.(string)
		out = append(out, text)
	}
	return out
}
