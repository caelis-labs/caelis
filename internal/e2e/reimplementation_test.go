package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/core/config"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/internal/app/local"
	"github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
	"github.com/OnslaughtSnail/caelis/internal/surface/acpserver"
	"github.com/OnslaughtSnail/caelis/protocol/acp/jsonrpc"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestReimplementationACPStackE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	pluginDir := filepath.Join(root, "plugins", "e2e")
	storePath := filepath.Join(root, "sessions.db")
	if err := os.MkdirAll(filepath.Join(pluginDir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("workspace e2e rule"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "prompts", "system.md"), []byte("plugin e2e prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{
		"id": "e2e-plugin",
		"name": "E2E Plugin",
		"stores": [{"name": "plugin-sqlite", "uses": "sqlite"}],
		"prompts": [{"id": "e2e.system", "priority": 50, "paths": ["prompts/system.md"]}]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	command := "printf e2e"
	if runtime.GOOS == "windows" {
		command = "echo e2e"
	}
	provider := newOpenAIStub(t, command)
	defer provider.Close()

	stack, err := local.New(local.Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "tester",
			Model:        "gpt-e2e",
			WorkspaceCWD: workspace,
			Store:        config.Store{Backend: "plugin-sqlite", URI: storePath},
			Sandbox:      config.Sandbox{Backend: "host"},
			Plugins: []config.Plugin{{
				Source:  pluginDir,
				Enabled: true,
			}},
		},
		Model: config.ModelProfile{
			Provider: "openai_compatible",
			BaseURL:  provider.URL + "/v1",
		},
		BuiltinTools: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	sessionID := runACPConversation(ctx, t, stack)
	if closer, ok := stack.Store().(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil {
			t.Fatal(err)
		}
	}

	requests := provider.Requests()
	if len(requests) != 2 {
		t.Fatalf("provider requests = %d, want tool call and continuation", len(requests))
	}
	firstMessages := renderMessages(requests[0].Messages)
	for _, want := range []string{"You are caelis", "plugin e2e prompt", "workspace e2e rule"} {
		if !strings.Contains(firstMessages, want) {
			t.Fatalf("first provider request = %s, missing %q", firstMessages, want)
		}
	}
	if !hasTool(requests[0], "run_command") {
		t.Fatalf("first provider request tools = %#v, missing run_command", requests[0].Tools)
	}
	if got := requests[1].Messages[len(requests[1].Messages)-1].Role; got != "tool" {
		t.Fatalf("second provider final message role = %q, want tool", got)
	}
	if got := renderMessages(requests[1].Messages); !strings.Contains(got, "e2e") {
		t.Fatalf("second provider request = %s, missing shell tool output", got)
	}

	reloaded, err := local.New(local.Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "tester",
			Model:        "gpt-e2e",
			WorkspaceCWD: workspace,
			Store:        config.Store{Backend: "plugin-sqlite", URI: storePath},
			Sandbox:      config.Sandbox{Backend: "host"},
			Plugins: []config.Plugin{{
				Source:  pluginDir,
				Enabled: true,
			}},
		},
		Model: config.ModelProfile{
			Provider: "openai_compatible",
			BaseURL:  provider.URL + "/v1",
		},
		BuiltinTools: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closer, ok := reloaded.Store().(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	})

	snapshot, err := reloaded.Services().Sessions().Load(ctx, session.Ref{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 5 {
		t.Fatalf("persisted events = %d, want user, assistant tool use, tool call, tool result, assistant", len(snapshot.Events))
	}
	if got := session.EventText(snapshot.Events[4]); got != "finished after tool" {
		t.Fatalf("final assistant text = %q, want finished after tool", got)
	}
	view, err := reloaded.Services().Views().Session(ctx, session.Ref{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if !transcriptContains(view.Transcript, "finished after tool") || !transcriptContains(view.Transcript, "stdout:\ne2e") {
		t.Fatalf("view transcript = %#v, want persisted tool output and final assistant", view.Transcript)
	}
}

func runACPConversation(ctx context.Context, t *testing.T, stack *local.Stack) string {
	t.Helper()
	clientToServerReader, clientToServerWriter := io.Pipe()
	serverToClientReader, serverToClientWriter := io.Pipe()
	t.Cleanup(func() {
		_ = clientToServerReader.Close()
		_ = clientToServerWriter.Close()
		_ = serverToClientReader.Close()
		_ = serverToClientWriter.Close()
	})

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- acpserver.ServeStdio(ctx, acpserver.Config{
			Engine:  stack.Services().Engine(),
			AppName: "caelis",
			UserID:  "tester",
			Implementation: schema.Implementation{
				Name:    "caelis-e2e",
				Version: "test",
			},
		}, clientToServerReader, serverToClientWriter)
	}()

	conn := jsonrpc.New(serverToClientReader, clientToServerWriter)
	updates := make(chan string, 8)
	go func() {
		_ = conn.Serve(ctx, nil, func(_ context.Context, msg jsonrpc.Message) {
			if msg.Method != schema.MethodSessionUpdate {
				return
			}
			var notification struct {
				Update struct {
					SessionUpdate string `json:"sessionUpdate"`
				} `json:"update"`
			}
			if err := json.Unmarshal(msg.Params, &notification); err == nil {
				updates <- notification.Update.SessionUpdate
			}
		})
	}()

	var initResp schema.InitializeResponse
	if err := conn.Call(ctx, schema.MethodInitialize, schema.InitializeRequest{
		ProtocolVersion:    schema.CurrentProtocolVersion,
		ClientCapabilities: map[string]any{},
	}, &initResp); err != nil {
		t.Fatalf("initialize call error = %v", err)
	}
	if initResp.ProtocolVersion != schema.CurrentProtocolVersion {
		t.Fatalf("protocol version = %d, want %d", initResp.ProtocolVersion, schema.CurrentProtocolVersion)
	}

	var newResp schema.NewSessionResponse
	if err := conn.Call(ctx, schema.MethodSessionNew, schema.NewSessionRequest{CWD: "/tmp/e2e"}, &newResp); err != nil {
		t.Fatalf("session/new call error = %v", err)
	}
	var promptResp schema.PromptResponse
	if err := conn.Call(ctx, schema.MethodSessionPrompt, schema.PromptRequest{
		SessionID: newResp.SessionID,
		Prompt: []json.RawMessage{
			jsonrpc.MustMarshalRaw(schema.TextContent{Type: "text", Text: "run e2e command"}),
		},
	}, &promptResp); err != nil {
		t.Fatalf("session/prompt call error = %v", err)
	}
	if promptResp.StopReason != schema.StopReasonEndTurn {
		t.Fatalf("stop reason = %q, want %q", promptResp.StopReason, schema.StopReasonEndTurn)
	}

	wantUpdates := []string{
		schema.UpdateUserMessage,
		schema.UpdateToolCall,
		schema.UpdateToolCallInfo,
		schema.UpdateAgentMessage,
	}
	var got []string
	for len(got) < len(wantUpdates) {
		select {
		case update := <-updates:
			got = append(got, update)
		case <-ctx.Done():
			t.Fatalf("timed out waiting for ACP updates, got %v", got)
		}
	}
	for i := range wantUpdates {
		if got[i] != wantUpdates[i] {
			t.Fatalf("ACP updates = %v, want prefix %v", got, wantUpdates)
		}
	}

	_ = clientToServerWriter.Close()
	_ = clientToServerReader.Close()
	_ = serverToClientWriter.Close()
	_ = serverToClientReader.Close()
	select {
	case <-serverErr:
	case <-time.After(time.Second):
		t.Fatal("ACP server did not stop")
	}
	return newResp.SessionID
}

type openAIStub struct {
	URL      string
	server   *httptest.Server
	mu       sync.Mutex
	requests []chatRequest
	command  string
}

func newOpenAIStub(t *testing.T, command string) *openAIStub {
	t.Helper()
	stub := &openAIStub{command: command}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		stub.mu.Lock()
		stub.requests = append(stub.requests, req)
		call := len(stub.requests)
		stub.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch call {
		case 1:
			arguments, _ := json.Marshal(map[string]string{"command": stub.command})
			_ = json.NewEncoder(w).Encode(map[string]any{
				"model": "gpt-e2e",
				"choices": []map[string]any{{
					"message": map[string]any{
						"role": "assistant",
						"tool_calls": []map[string]any{{
							"id":   "call-1",
							"type": "function",
							"function": map[string]any{
								"name":      "run_command",
								"arguments": string(arguments),
							},
						}},
					},
					"finish_reason": "tool_calls",
				}},
			})
		case 2:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"model": "gpt-e2e",
				"choices": []map[string]any{{
					"message": map[string]any{
						"role":    "assistant",
						"content": "finished after tool",
					},
					"finish_reason": "stop",
				}},
			})
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	}))
	stub.URL = stub.server.URL
	return stub
}

func (s *openAIStub) Close() {
	s.server.Close()
}

func (s *openAIStub) Requests() []chatRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]chatRequest, len(s.requests))
	copy(out, s.requests)
	return out
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Tools    []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
}

type chatMessage struct {
	Role       string `json:"role"`
	Content    any    `json:"content"`
	ToolCallID string `json:"tool_call_id"`
}

func renderMessages(messages []chatMessage) string {
	raw, _ := json.Marshal(messages)
	return string(raw)
}

func hasTool(req chatRequest, name string) bool {
	for _, candidate := range req.Tools {
		if candidate.Function.Name == name {
			return true
		}
	}
	return false
}

func transcriptContains(items []viewmodel.TranscriptItem, text string) bool {
	for _, item := range items {
		if strings.Contains(item.Text, text) {
			return true
		}
	}
	return false
}
