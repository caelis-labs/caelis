package gatewayapp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/surfaces/headless"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
)

func TestRetiredMemoryAPIAcceptsLegacyShapeAndRejectsWithoutSaving(t *testing.T) {
	stack := newLocalStateTestHost(t)
	ctx := t.Context()
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	revision, err := stack.ControlStatus().ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(stack.composition.authorities.store.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, handle := range []agentbinding.Handle{"steward", "memory-verifier"} {
		for _, reset := range []bool{false, true} {
			var result appserver.CommandResult
			var commandErr error
			operation := fmt.Sprintf("retired-%s-%t", handle, reset)
			if reset {
				var req appserver.ResetAgentBindingRequest
				raw := fmt.Sprintf(`{"operation_id":%q,"expected_revision":"%d","handle":%q}`, operation, revision, handle)
				if err := wirev1.DecodeRequest([]byte(raw), &req); err != nil {
					t.Fatal(err)
				}
				result, commandErr = stack.AgentCommands().ResetAgentBinding(ctx, principal, req)
			} else {
				var req appserver.BindAgentBindingRequest
				raw := fmt.Sprintf(`{"operation_id":%q,"expected_revision":"%d","binding":{"handle":%q,"profile_id":"missing","effort":"high"}}`, operation, revision, handle)
				if err := wirev1.DecodeRequest([]byte(raw), &req); err != nil {
					t.Fatal(err)
				}
				result, commandErr = stack.AgentCommands().BindAgentBinding(ctx, principal, req)
			}
			if commandErr == nil || !strings.Contains(commandErr.Error(), "Core Memory is retired") || result.Outcome != appserver.OutcomeRejected {
				t.Fatalf("retired API = %#v, %v", result, commandErr)
			}
			after, err := os.ReadFile(stack.composition.authorities.store.path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("retired API wrote configuration: %v", err)
			}
		}
	}
}

func TestFreshHostDoesNotInitializeMemory(t *testing.T) {
	stack := newLocalStateTestHost(t)
	if _, err := os.Stat(stack.composition.authorities.storeDir + "/memory"); !os.IsNotExist(err) {
		t.Fatalf("Memory directory initialized: %v", err)
	}
}

func TestRetiredMemoryToolRequestsReturnUnavailableResults(t *testing.T) {
	requests := make(chan map[string]any, 4)
	client := &http.Client{Transport: gatewayAppRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		var payload map[string]any
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			return nil, err
		}
		requests <- payload
		hasResult := false
		for _, raw := range payload["messages"].([]any) {
			if raw.(map[string]any)["role"] == "tool" {
				hasResult = true
			}
		}
		delta := map[string]any{"role": "assistant", "content": "retired tools unavailable"}
		finish := "stop"
		if !hasResult {
			delta = map[string]any{"role": "assistant", "tool_calls": []map[string]any{
				{"index": 0, "id": "retired-remember", "type": "function", "function": map[string]any{"name": "Remember", "arguments": `{"text":"must not save"}`}},
				{"index": 1, "id": "retired-recall", "type": "function", "function": map[string]any{"name": "Recall", "arguments": `{"query":"must not read"}`}},
			}}
			finish = "tool_calls"
		}
		recorder := httptest.NewRecorder()
		recorder.Header().Set("Content-Type", "text/event-stream")
		writePluginSystemE2ESSE(recorder, map[string]any{"id": "retired-tool-test", "object": "chat.completion.chunk", "model": "fixture", "choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}}})
		_, _ = fmt.Fprint(recorder, "data: [DONE]\n\n")
		return recorder.Result(), nil
	})}
	stack, err := newGatewayAppTestStack(t, Config{AppName: "caelis", UserID: "retired-tools", StoreDir: t.TempDir(), WorkspaceCWD: t.TempDir(), WorkspaceKey: "workspace", SkillDirs: []string{}, Sandbox: SandboxConfig{RequestedType: "host"}, Model: ModelConfig{Provider: "openai-compatible", API: providers.APIOpenAICompatible, Model: "fixture", BaseURL: "http://retired-tools.test", HTTPClient: client, Token: "fixture-token", AuthType: providers.AuthBearerToken, ContextWindowTokens: 128000, MaxOutputTok: 4096}})
	if err != nil {
		t.Fatal(err)
	}
	active, err := startGatewayAppTestSession(t.Context(), stack, "retired-tools")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runHeadlessOnceForGatewayAppTest(t.Context(), stack, active, active.SessionID, "try retired tools", headless.Options{}); err != nil {
		t.Fatal(err)
	}
	first, second := <-requests, <-requests
	assertRetiredMemoryAbsentFromRequest(t, first)
	assertRetiredMemoryAbsentFromRequest(t, second)
	results := 0
	for _, raw := range second["messages"].([]any) {
		msg := raw.(map[string]any)
		if msg["role"] != "tool" {
			continue
		}
		content, _ := msg["content"].(string)
		if !strings.Contains(content, "not found") || strings.Contains(content, `"accepted":true`) || strings.Contains(content, `"saved":true`) {
			t.Fatalf("retired tool result claimed success: %s", content)
		}
		results++
	}
	if results != 2 {
		t.Fatalf("got %d rejected tool results", results)
	}
	if _, err := os.Stat(filepath.Join(stack.composition.authorities.storeDir, "memory")); !os.IsNotExist(err) {
		t.Fatalf("tool dispatch initialized Memory: %v", err)
	}
}
