package gatewayapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/memorytool"
	"github.com/caelis-labs/caelis/surfaces/headless"
)

const memoryGoldenFact = "the preferred review language is Chinese"

func TestMemoryEmbeddedGoldenPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	storeDir := filepath.Join(root, "caelis")
	workspace := filepath.Join(root, "workspace")
	isolatedWorkspace := filepath.Join(root, "isolated-workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(isolatedWorkspace, 0o700); err != nil {
		t.Fatal(err)
	}
	provider := newMemoryGoldenProvider(t)

	stack := newMemoryGoldenStack(t, provider, storeDir, workspace)
	if stack.memoryRuntime == nil {
		t.Fatal("Host started without its embedded Memory runtime")
	}
	document, err := newAppConfigStore(storeDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if !isDefaultEmbeddedMemoryConfiguration(document.Memory) {
		t.Fatalf("automatically provisioned Memory = %#v", document.Memory)
	}
	if stack.memorySteward == nil || stack.memorySteward.active.Load() || !stack.memorySteward.policySynced.Load() {
		t.Fatalf("default Steward bridge = %#v, want static zero-model mode", stack.memorySteward)
	}
	configuration, err := stack.memoryRuntime.Management().GetStewardConfiguration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(configuration.Bindings) != 0 {
		t.Fatalf("default Steward bindings = %#v, want static zero-model mode", configuration.Bindings)
	}

	provider.Begin(memoryGoldenScenario{
		name: "embedded-first-run", actions: []memoryGoldenAction{
			{name: memorytool.RememberToolName, arguments: `{"text":"` + memoryGoldenFact + `"}`},
			{name: memorytool.RecallToolName, arguments: `{"query":"preferred review language"}`},
		},
	})
	active := runMemoryGoldenSession(t, ctx, stack, "session-embedded")
	before := memoryGoldenToolResults(t, stack, active.SessionRef)
	assertMemoryGoldenResult(t, before, memorytool.RememberToolName, `{"accepted":true}`)
	assertMemoryGoldenResult(t, before, memorytool.RecallToolName, memoryGoldenFact)
	provider.AssertComplete(t)
	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}

	stack = newMemoryGoldenStackForWorkspace(t, provider, storeDir, "memory-golden-isolated", isolatedWorkspace)
	provider.Begin(memoryGoldenScenario{
		name:    "embedded-other-workspace",
		actions: []memoryGoldenAction{{name: memorytool.RecallToolName, arguments: `{"query":"preferred review language"}`}},
	})
	isolated := runMemoryGoldenSession(t, ctx, stack, "session-isolated-workspace")
	assertMemoryGoldenResultAbsent(t, memoryGoldenToolResults(t, stack, isolated.SessionRef), memorytool.RecallToolName, memoryGoldenFact)
	provider.AssertComplete(t)
	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}

	stack = newMemoryGoldenStack(t, provider, storeDir, workspace)
	defer stack.Close()
	after := memoryGoldenToolResults(t, stack, active.SessionRef)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("Session Replay changed Memory ToolResults:\nbefore=%q\nafter=%q", before, after)
	}
	provider.Begin(memoryGoldenScenario{
		name:    "embedded-after-restart",
		actions: []memoryGoldenAction{{name: memorytool.RecallToolName, arguments: `{"query":"preferred review language"}`}},
	})
	restarted := runMemoryGoldenSession(t, ctx, stack, "session-after-restart")
	assertMemoryGoldenResult(t, memoryGoldenToolResults(t, stack, restarted.SessionRef), memorytool.RecallToolName, memoryGoldenFact)
	provider.AssertComplete(t)
	if _, err := os.Stat(filepath.Join(storeDir, "memory", "appliance", "memory.db")); err != nil {
		t.Fatalf("embedded Memory database: %v", err)
	}
}

func TestMemoryStewardBindingControlsEmbeddedWorker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	storeDir := filepath.Join(root, "caelis")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	provider := newMemoryGoldenProvider(t)
	provider.EnableSteward()
	stack := newMemoryGoldenStack(t, provider, storeDir, workspace)
	defer stack.Close()

	document, err := newAppConfigStore(storeDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.testAgentBindings().BindAgentBinding(ctx, agentbinding.Binding{
		Handle:    agentbinding.HandleSteward,
		ProfileID: document.ModelProfiles.DefaultProfileID,
		Effort:    document.ModelProfiles.DefaultEffort,
	}); err != nil {
		t.Fatalf("bind Memory Steward: %v", err)
	}
	waitMemoryGoldenCondition(t, ctx, "Steward semantic policy", func() bool {
		return stack.memorySteward != nil && stack.memorySteward.active.Load()
	})

	provider.Begin(memoryGoldenScenario{
		name:    "semantic-steward",
		actions: []memoryGoldenAction{{name: memorytool.RememberToolName, arguments: `{"text":"the release review language is Chinese"}`}},
	})
	runMemoryGoldenSession(t, ctx, stack, "session-semantic-steward")
	provider.AssertComplete(t)
	waitMemoryGoldenCondition(t, ctx, "completed Steward job", func() bool {
		inspection, inspectErr := stack.memoryRuntime.Management().Inspect(ctx)
		return inspectErr == nil && inspection.Steward.CompletedJobs >= 1 && inspection.Steward.ActiveRecords >= 1
	})
	if provider.StewardCalls() == 0 {
		t.Fatal("explicit Steward binding completed no model callback")
	}

	if _, err := stack.testAgentBindings().ResetAgentBinding(ctx, agentbinding.HandleSteward); err != nil {
		t.Fatalf("reset Memory Steward: %v", err)
	}
	waitMemoryGoldenCondition(t, ctx, "Steward static policy", func() bool {
		configuration, configErr := stack.memoryRuntime.Management().GetStewardConfiguration(ctx)
		return configErr == nil && stack.memorySteward != nil && !stack.memorySteward.active.Load() && len(configuration.Bindings) == 0
	})
}

func newMemoryGoldenStack(t *testing.T, provider *memoryGoldenProvider, storeDir, workspace string) *Stack {
	t.Helper()
	return newMemoryGoldenStackForWorkspace(t, provider, storeDir, "memory-golden", workspace)
}

func newMemoryGoldenStackForWorkspace(
	t *testing.T,
	provider *memoryGoldenProvider,
	storeDir string,
	workspaceKey string,
	workspace string,
) *Stack {
	t.Helper()
	stack, err := newGatewayAppTestStack(t, Config{
		AppName: "caelis-memory", UserID: "memory-golden", StoreDir: storeDir,
		WorkspaceKey: workspaceKey, WorkspaceCWD: workspace, SkillDirs: []string{},
		Sandbox: SandboxConfig{RequestedType: "host"},
		Model: ModelConfig{
			Provider: "openai-compatible", API: providers.APIOpenAICompatible,
			Model: "memory-golden", BaseURL: provider.URL, HTTPClient: provider.Client(),
			Token: "memory-model-test-token", AuthType: providers.AuthBearerToken,
			ContextWindowTokens: 128000, MaxOutputTok: 4096, Timeout: 5 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("start embedded Memory stack: %v", err)
	}
	return stack
}

func waitMemoryGoldenCondition(t *testing.T, ctx context.Context, name string, condition func() bool) {
	t.Helper()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for %s: %v", name, ctx.Err())
		case <-ticker.C:
		}
	}
}

func runMemoryGoldenSession(t *testing.T, ctx context.Context, stack *Stack, sessionID string) session.Session {
	t.Helper()
	active, err := startGatewayAppTestSession(ctx, stack, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runHeadlessOnceForGatewayAppTest(ctx, stack, active, sessionID, "execute Memory Golden Path step", headless.Options{})
	if err != nil {
		t.Fatalf("run Memory Golden Path Session: %v", err)
	}
	if strings.TrimSpace(result.Output) != "memory-golden-ok" {
		t.Fatalf("Memory Golden Path output = %q", result.Output)
	}
	return active
}

type memoryGoldenResult struct {
	name string
	data string
}

func memoryGoldenToolResults(t *testing.T, stack *Stack, ref session.SessionRef) []memoryGoldenResult {
	t.Helper()
	events, err := stack.composition.sessions.Events(context.Background(), session.EventsRequest{SessionRef: ref})
	if err != nil {
		t.Fatal(err)
	}
	var results []memoryGoldenResult
	for _, event := range events {
		if event == nil || event.Type != session.EventTypeToolResult {
			continue
		}
		message, ok := session.ModelMessageOf(event)
		if !ok || message.ToolResponse() == nil {
			continue
		}
		response := message.ToolResponse()
		if response.Name != memorytool.RememberToolName && response.Name != memorytool.RecallToolName {
			continue
		}
		data, err := json.Marshal(response.Result)
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, memoryGoldenResult{name: response.Name, data: string(data)})
	}
	return results
}

func assertMemoryGoldenResult(t *testing.T, results []memoryGoldenResult, name, contains string) {
	t.Helper()
	for _, result := range results {
		if result.name == name && strings.Contains(result.data, contains) {
			return
		}
	}
	t.Fatalf("Memory %s ToolResult missing %q: %#v", name, contains, results)
}

func assertMemoryGoldenResultAbsent(t *testing.T, results []memoryGoldenResult, name, absent string) {
	t.Helper()
	found := false
	for _, result := range results {
		if result.name != name {
			continue
		}
		found = true
		if strings.Contains(result.data, absent) {
			t.Fatalf("Memory %s ToolResult crossed workspace LabelSet: %#v", name, results)
		}
	}
	if !found {
		t.Fatalf("Memory %s ToolResult is missing: %#v", name, results)
	}
}

type memoryGoldenAction struct {
	name      string
	arguments string
}

type memoryGoldenScenario struct {
	name    string
	actions []memoryGoldenAction
}

type memoryGoldenProvider struct {
	*gatewayTestHTTPServer
	mu             sync.Mutex
	scenario       memoryGoldenScenario
	calls          int
	errors         []string
	stewardEnabled bool
	stewardCalls   int
}

func newMemoryGoldenProvider(t *testing.T) *memoryGoldenProvider {
	t.Helper()
	provider := &memoryGoldenProvider{}
	provider.gatewayTestHTTPServer = newGatewayTestHTTPServer(http.HandlerFunc(provider.handle))
	t.Cleanup(provider.Close)
	return provider
}

func (p *memoryGoldenProvider) Begin(scenario memoryGoldenScenario) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scenario = scenario
	p.calls = 0
	p.errors = nil
}

func (p *memoryGoldenProvider) EnableSteward() {
	p.mu.Lock()
	p.stewardEnabled = true
	p.mu.Unlock()
}

func (p *memoryGoldenProvider) StewardCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stewardCalls
}

func (p *memoryGoldenProvider) handle(w http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/chat/completions" {
		http.NotFound(w, request)
		return
	}
	var payload map[string]any
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if p.handleSteward(w, payload) {
		return
	}
	p.mu.Lock()
	call := p.calls
	p.calls++
	scenario := p.scenario
	p.checkTools(payload)
	p.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	if call < len(scenario.actions) {
		action := scenario.actions[call]
		writePluginSystemE2ESSE(w, map[string]any{
			"id": "memory-golden-" + scenario.name, "object": "chat.completion.chunk", "model": "memory-golden",
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{{
						"index": 0, "id": fmt.Sprintf("memory_%s_%d", scenario.name, call), "type": "function",
						"function": map[string]any{"name": action.name, "arguments": action.arguments},
					}},
				},
				"finish_reason": "tool_calls",
			}},
		})
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	writePluginSystemE2ESSE(w, map[string]any{
		"id": "memory-golden-final-" + scenario.name, "object": "chat.completion.chunk", "model": "memory-golden",
		"choices": []map[string]any{{
			"index": 0, "delta": map[string]any{"role": "assistant", "content": "memory-golden-ok"}, "finish_reason": "stop",
		}},
	})
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

func (p *memoryGoldenProvider) handleSteward(w http.ResponseWriter, payload map[string]any) bool {
	p.mu.Lock()
	enabled := p.stewardEnabled
	p.mu.Unlock()
	if !enabled || memoryGoldenPayloadHasTools(payload) {
		return false
	}
	input, ok := memoryGoldenStewardInput(payload)
	if !ok {
		p.mu.Lock()
		p.errors = append(p.errors, "Steward model input is missing")
		p.mu.Unlock()
		http.Error(w, "missing Steward input", http.StatusBadRequest)
		return true
	}
	proposal := map[string]any{
		"operation": "ADD", "kind": "fact", "text": input.Receipt.Text,
		"evidence_refs": []string{string(input.Receipt.ReceiptID)},
	}
	content, err := json.Marshal(proposal)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}
	p.mu.Lock()
	p.stewardCalls++
	p.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	writePluginSystemE2ESSE(w, map[string]any{
		"id": "memory-steward", "object": "chat.completion.chunk", "model": "memory-steward",
		"choices": []map[string]any{{
			"index": 0, "delta": map[string]any{"role": "assistant", "content": string(content)}, "finish_reason": "stop",
		}},
	})
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	return true
}

func memoryGoldenPayloadHasTools(payload map[string]any) bool {
	return len(anySliceFromFinalToolValue(payload["tools"])) > 0
}

func memoryGoldenStewardInput(payload map[string]any) (memoryStewardModelInput, bool) {
	messages, _ := payload["messages"].([]any)
	for index := len(messages) - 1; index >= 0; index-- {
		message, _ := messages[index].(map[string]any)
		for _, text := range memoryGoldenMessageTexts(message["content"]) {
			var input memoryStewardModelInput
			if json.Unmarshal([]byte(text), &input) == nil && input.Receipt.ReceiptID != "" {
				return input, true
			}
		}
	}
	return memoryStewardModelInput{}, false
}

func memoryGoldenMessageTexts(content any) []string {
	if text, ok := content.(string); ok {
		return []string{text}
	}
	var out []string
	for _, raw := range anySliceFromFinalToolValue(content) {
		part, _ := raw.(map[string]any)
		if text, _ := part["text"].(string); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func (p *memoryGoldenProvider) checkTools(payload map[string]any) {
	counts := map[string]int{}
	for _, raw := range anySliceFromFinalToolValue(payload["tools"]) {
		entry, _ := raw.(map[string]any)
		function, _ := entry["function"].(map[string]any)
		name, _ := function["name"].(string)
		if name == memorytool.RememberToolName || name == memorytool.RecallToolName {
			counts[name]++
			parameters, _ := function["parameters"].(map[string]any)
			properties, _ := parameters["properties"].(map[string]any)
			want := "text"
			if name == memorytool.RecallToolName {
				want = "query"
			}
			if len(properties) != 1 || properties[want] == nil {
				p.errors = append(p.errors, fmt.Sprintf("%s properties=%v", name, properties))
			}
		}
	}
	if counts[memorytool.RememberToolName] != 1 || counts[memorytool.RecallToolName] != 1 {
		p.errors = append(p.errors, fmt.Sprintf("Memory tool counts=%v", counts))
	}
}

func (p *memoryGoldenProvider) AssertComplete(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls != len(p.scenario.actions)+1 || len(p.errors) != 0 {
		t.Fatalf("Memory provider scenario %q calls=%d errors=%v", p.scenario.name, p.calls, p.errors)
	}
}
