package gatewayapp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/bot"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func assertBotNotebookWireTools(t *testing.T, payload map[string]any) {
	t.Helper()
	var names []string
	for _, raw := range payload["tools"].([]any) {
		entry := raw.(map[string]any)
		function := entry["function"].(map[string]any)
		names = append(names, function["name"].(string))
	}
	if !reflect.DeepEqual(names, []string{"Read", "Write", "Patch", "Glob", "Grep"}) {
		t.Fatalf("Bot notebook tool set = %v", names)
	}
}

func TestBotNotebookToolEffectsPrefixAndContextRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var mu sync.Mutex
	var requests []map[string]any
	actions := []struct{ name, args string }{
		{"Read", `{ "path": "index.md" }`},
		{"Write", `{"path":"notes/preferences.md","content":"# Preferences\nUser stated: prefers tea.\n"}`},
		{"Write", `{"path": "index.md", "content": "# Notebook <index> & notes\n- [Preferences](notes/preferences.md)\n"}`},
		{},
		{"Read", `{"path":"notes/preferences.md"}`},
		{"Patch", `{"path":"notes/preferences.md","edits":[{"old":"prefers tea","new":"prefers cocoa"}]}`},
		{"Write", fmt.Sprintf(`{"path":"notes/preferences.md","content":"stale overwrite","if_revision":"sha256:%x"}`, sha256.Sum256([]byte("# Preferences\nUser stated: prefers tea.\n")))},
		{}, {}, {}, // finish edit, ordinary continuation, reconstruction
		{"Read", `{ "path": "index.md" }`},
		{"Read", `{"path":"notes/preferences.md"}`},
		{},
	}
	client := &http.Client{Transport: gatewayAppRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		var payload map[string]any
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			return nil, err
		}
		mu.Lock()
		i := len(requests)
		requests = append(requests, payload)
		mu.Unlock()
		if i >= len(actions) {
			return nil, fmt.Errorf("unexpected notebook model call %d", i)
		}
		action := actions[i]
		message := map[string]any{"role": "assistant", "content": "done"}
		finish := "stop"
		if action.name != "" {
			message = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": fmt.Sprintf("note-%d", i), "type": "function", "function": map[string]any{"name": action.name, "arguments": action.args}}}}
			finish = "tool_calls"
		}
		data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": finish}}})
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(data))), Request: req}, nil
	})}
	storeDir := t.TempDir()
	stack, err := newGatewayAppTestStack(t, Config{StoreDir: storeDir, WorkspaceCWD: t.TempDir(), Model: ModelConfig{Provider: "openai-compatible", API: providers.APIOpenAICompatible, BaseURL: "https://provider.invalid/v1", Model: "gpt-4.1", Token: "test-token", HTTPClient: client}})
	if err != nil {
		t.Fatal(err)
	}
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	created, err := stack.Bots().CreateBot(ctx, principal, appserver.CreateBotRequest{WriteBase: appserver.WriteBase{OperationID: "notebook-effects"}, Config: bot.Config{Name: "Notebook"}})
	if err != nil {
		t.Fatal(err)
	}
	ref := session.SessionRef{SessionID: created.SessionID}
	active := activateSessionRuntime(t, stack, ref.SessionID)
	prompt := func(input string) {
		t.Helper()
		stream := false
		turn, err := active.instance.currentGateway().BeginTurn(ctx, kernel.BeginTurnRequest{SessionRef: ref, Input: input, Request: agent.ModelRequestOptions{Stream: &stream}})
		if err != nil {
			t.Fatal(err)
		}
		if err := turn.Handle.WaitCompletion(ctx); err != nil {
			t.Fatal(err)
		}
	}
	prompt("save my preference")
	prompt("revise my preference")
	prompt("ordinary continuation after editing")
	root, err := bot.NotebookRoot(storeDir, created.Resource.Ref)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "notes", "preferences.md"))
	if err != nil || string(data) != "# Preferences\nUser stated: prefers cocoa.\n" {
		t.Fatalf("notebook effects = %q, %v", data, err)
	}
	mu.Lock()
	for i, request := range requests {
		assertBotNotebookWireTools(t, request)
		if i > 0 {
			previous := requests[i-1]["messages"].([]any)
			current := request["messages"].([]any)
			if len(current) <= len(previous) || !reflect.DeepEqual(previous, current[:len(previous)]) || !reflect.DeepEqual(requests[i-1]["tools"], request["tools"]) {
				t.Fatalf("notebook mutation rewrote request prefix at %d", i)
			}
		}
	}
	original := requests[len(requests)-1]
	mu.Unlock()

	// Reopen canonical persistence and reconstruct the complete request including
	// tool calls/results, without reading current notebook bytes into the prefix.
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(storeDir, "sessions")})
	loaded, err := reopened.LoadSession(ctx, session.LoadSessionRequest{SessionRef: ref})
	if err != nil {
		t.Fatal(err)
	}
	foundStaleError := false
	for _, event := range loaded.Events {
		if event.Message != nil {
			for _, result := range event.Message.ToolResults() {
				if result.ToolUseID == "note-6" && result.IsError {
					foundStaleError = true
				}
			}
		}
	}
	if !foundStaleError {
		t.Fatal("stale write did not produce a canonical model-visible error")
	}
	var events []*session.Event
	for i, event := range loaded.Events {
		if event.Message != nil && event.Message.Role == model.RoleUser && event.Message.TextContent() == "ordinary continuation after editing" {
			events = loaded.Events[:i+1]
			break
		}
	}
	tools, err := active.instance.exec.(*bot.Notebook).Tools()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := (&botTurnResolver{composition: &active.instance.runtimeComposition, notebookTools: tools}).ResolveTurn(ctx, kernel.TurnIntent{SessionRef: ref})
	if err != nil {
		t.Fatal(err)
	}
	stream := false
	resolved.RunRequest.AgentSpec.Request.Stream = &stream
	replay, err := (chat.Factory{}).NewAgent(ctx, resolved.RunRequest.AgentSpec)
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range replay.Run(agent.NewContext(agent.ContextSpec{Context: ctx, Session: loaded.Session, State: loaded.State, Events: events})) {
		if err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	if !reflect.DeepEqual(original, requests[len(requests)-1]) {
		t.Fatal("canonical notebook tool history did not rebuild the exact provider request")
	}
	mu.Unlock()
	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}
	stack, err = NewLocalStack(Config{StoreDir: storeDir, WorkspaceCWD: t.TempDir(), Sandbox: SandboxConfig{RequestedType: "host"}, ResolveProviderHTTPClient: func(context.Context, ModelConfig) (*http.Client, error) { return client, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	active = activateSessionRuntime(t, stack, ref.SessionID)
	prompt("read notes after restart")
	mu.Lock()
	defer mu.Unlock()
	last := requests[len(requests)-1]
	messages, _ := json.Marshal(last["messages"])
	if !strings.Contains(string(messages), "prefers cocoa") || len(requests) != len(actions) {
		t.Fatal("restart did not read the revised notebook through canonical tool results")
	}
}

func TestBotNotebookIndexDiscoverableAfterWatermarkCompaction(t *testing.T) {
	ctx := t.Context()
	server := newGatewayAppCompactionOllamaServer(t)
	stack, err := newGatewayAppTestStack(t, Config{StoreDir: t.TempDir(), WorkspaceCWD: t.TempDir(), ContextWindow: 64, Model: ModelConfig{Provider: "ollama", API: providers.APIOllama, Model: "compact-test", BaseURL: server.URL, HTTPClient: server.Client()}})
	if err != nil {
		t.Fatal(err)
	}
	active := startBotRuntimeTestSession(t, stack, bot.Config{Name: "Watermark", Model: stack.composition.lookup.DefaultID()})
	for range 5 {
		appendGatewayAppEvent(t, stack, active.SessionRef, gatewayAppUserEvent("A long past conversation, already saved in the private notebook when useful."))
		appendGatewayAppEvent(t, stack, active.SessionRef, gatewayAppAssistantEvent("Acknowledged."))
	}
	instance := activateSessionRuntime(t, stack, active.SessionID).instance
	stream := false
	turn, err := instance.currentGateway().BeginTurn(ctx, kernel.BeginTurnRequest{SessionRef: active.SessionRef, Input: "continue", Request: agent.ModelRequestOptions{Stream: &stream}})
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := turn.Handle.WaitCompletion(waitCtx); err != nil {
		t.Fatal(err)
	}
	loaded, err := stack.composition.sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := compact.LatestCompactEvent(loaded.Events); !ok || server.compactionCalls.Load() == 0 {
		t.Fatal("system watermark did not compact Bot context")
	}
	tools, err := instance.exec.(*bot.Notebook).Tools()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := (&botTurnResolver{composition: &instance.runtimeComposition, notebookTools: tools}).ResolveTurn(ctx, kernel.TurnIntent{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	instructions, _ := resolved.RunRequest.AgentSpec.Metadata["system_prompt"].(string)
	if instructions != buildBotSystemPrompt(stack.composition.authorities.appName) || !strings.Contains(instructions, bot.NotebookIndex) || len(resolved.RunRequest.AgentSpec.Tools) != 5 {
		t.Fatal("compaction removed the stable notebook entry point")
	}
}
