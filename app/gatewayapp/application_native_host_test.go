//go:build darwin

package gatewayapp_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
)

type nativeModelTool struct{ Name, Input string }
type nativeModelScript struct {
	mu           sync.Mutex
	calls        []nativeModelTool
	index        int
	callSequence int
	seen         [][]byte
	resourcePath string
}

func (m *nativeModelScript) set(calls ...nativeModelTool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = calls
	m.index = 0
	m.resourcePath = ""
}
func (m *nativeModelScript) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.seen = append(m.seen, append([]byte(nil), raw...))
	// The next model request includes the actual ReadResource tool result.
	// Resolve its returned path once, rather than manufacturing a Host path
	// from resource IDs or exposing the Store's internal directory.
	if m.index == 1 && len(m.calls) > 0 && m.calls[0].Name == "ReadResource" {
		match := regexp.MustCompile(`\.resources/[a-f0-9]{64}`).Find(raw)
		if len(match) == 0 {
			m.mu.Unlock()
			return nil, fmt.Errorf("ReadResource returned no model-visible relative path")
		}
		m.resourcePath = string(match)
	}
	var message map[string]any
	finish := "stop"
	if m.index < len(m.calls) {
		call := m.calls[m.index]
		m.index++
		m.callSequence++
		if strings.Contains(call.Input, "$RESOURCE_PATH") {
			if m.resourcePath == "" {
				m.mu.Unlock()
				return nil, fmt.Errorf("resource path is unavailable to the model")
			}
			call.Input = strings.ReplaceAll(call.Input, "$RESOURCE_PATH", m.resourcePath)
		}
		message = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": fmt.Sprintf("native-%d", m.callSequence), "index": 0, "type": "function", "function": map[string]any{"name": call.Name, "arguments": call.Input}}}}
		finish = "tool_calls"
	} else {
		message = map[string]any{"role": "assistant", "content": "Native operation complete."}
	}
	m.mu.Unlock()
	body, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": message, "finish_reason": finish}}})
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(body) + "\n\ndata: [DONE]\n\n")), Request: req}, nil
}

func TestApplicationNativeProviderUsesReturnedResourcePath(t *testing.T) {
	provider := &nativeModelScript{}
	provider.set(nativeModelTool{"ReadResource", `{"resource_id":"opaque"}`}, nativeModelTool{"Read", `{"path":"$RESOURCE_PATH"}`}, nativeModelTool{"RunCommand", `{"command":"/bin/cat $RESOURCE_PATH"}`})
	request := func(body string) string {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, "https://provider.invalid", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		response, err := provider.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	request(`{"messages":[{"role":"user","content":"input"}]}`)
	path := ".resources/" + strings.Repeat("a", 64)
	result := request(fmt.Sprintf(`{"messages":[{"role":"tool","content":%q}]}`, `{"resource":{"id":"opaque"},"path":"`+path+`"}`))
	if !strings.Contains(result, path) {
		t.Fatalf("native Read did not use returned resource path: %s", result)
	}
	result = request(`{"messages":[{"role":"tool","content":"read result"}]}`)
	if !strings.Contains(result, path) {
		t.Fatalf("native command did not use returned resource path: %s", result)
	}
}

func nativeHostSession(t *testing.T, ctx context.Context, host *applicationHTTPHost, cwd string) (*httpclient.Client, string) {
	t.Helper()
	status, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	connected, err := host.host.ConnectModel(ctx, appserver.ConnectModelRequest{WriteBase: appserver.WriteBase{OperationID: "connect-native", ExpectedRevision: &status.Configuration.Revision}, Config: appserver.ConnectConfig{Provider: "openai-compatible", Model: "gpt-4.1", BaseURL: "https://provider.invalid/v1", APIKey: "SYNTHETIC_ONLY"}})
	if err != nil || connected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("connect = %+v, %v", connected, err)
	}
	client, _ := registerApplicationHTTP(t, ctx, host, "native", filepath.Join(filepath.Dir(cwd), "native.credential"))
	profile := application.Profile{Version: "native/1", Instructions: "Use the selected native tools.", Model: "openai-compatible/gpt-4.1", ToolsVersion: "native/1", Execution: "workspace-write", Workspace: application.Workspace{CWD: cwd}}
	created, err := client.CreateApplicationSession(ctx, appserver.CreateApplicationSessionRequest{WriteBase: appserver.WriteBase{OperationID: "create-native"}, Profile: profile})
	if err != nil || created.SessionID == "" {
		t.Fatalf("create = %+v, %v", created, err)
	}
	return client, created.SessionID
}
func nativePrompt(t *testing.T, ctx context.Context, client *httpclient.Client, id, op string) {
	t.Helper()
	result, err := client.PromptApplication(ctx, appserver.ApplicationPromptRequest{PromptRequest: appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: id, OperationID: op}, Input: "Carry out the selected synthetic operation."}, SourceKind: "user"})
	if err != nil || (result.Outcome != appserver.OutcomeCommitted && result.Outcome != appserver.OutcomeAccepted) {
		t.Fatalf("prompt = %+v, %v", result, err)
	}
	waitApplicationHTTPIdle(t, ctx, client, id)
	for _, event := range applicationHTTPHistory(t, ctx, client, id) {
		if event.TurnID != result.Target.TurnID {
			continue
		}
		if update, ok := event.Update.(eventstream.ToolCallUpdate); ok && update.Status != nil && *update.Status == "failed" {
			t.Fatalf("native operation %s tool %s failed: %+v", op, update.ToolCallID, update.RawOutput)
		}
	}
}

// TestApplicationNativeHTTPB01B02B10 exercises the public HTTP Host and
// controlled provider with real Seatbelt effects; outer sandbox may disallow
// sandbox_apply, so native platform acceptance is explicit opt-in.
func TestApplicationNativeHTTPB01B02B10(t *testing.T) {
	if os.Getenv("CAELIS_TEST_APPLICATION_NATIVE") != "1" {
		t.Skip("set CAELIS_TEST_APPLICATION_NATIVE=1 for native macOS acceptance")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	root := t.TempDir()
	cwd := filepath.Join(root, "notebook")
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("UNINHERITED_CONTEXT_SENTINEL"), 0600); err != nil {
		t.Fatal(err)
	}
	provider := &nativeModelScript{}
	host := startApplicationHTTPHost(t, filepath.Join(root, "store"), cwd, provider)
	defer host.close(t)
	client, id := nativeHostSession(t, ctx, host, cwd)
	provider.set(nativeModelTool{"Write", `{"path":"MEMORY.md","content":"notebook identity"}`}, nativeModelTool{"Write", `{"path":"2026/01/02/diary.md","content":"daily note"}`}, nativeModelTool{"RunCommand", `{"command":"/bin/cat MEMORY.md 2026/01/02/diary.md > command-output.txt"}`})
	nativePrompt(t, ctx, client, id, "notebook-write")
	for path, want := range map[string]string{"MEMORY.md": "notebook identity", "2026/01/02/diary.md": "daily note", "command-output.txt": "notebook identitydaily note"} {
		got, err := os.ReadFile(filepath.Join(cwd, path))
		if err != nil || string(got) != want {
			t.Fatalf("native %s=%q, %v", path, got, err)
		}
	}
	provider.set(nativeModelTool{"Read", `{"path":"MEMORY.md"}`})
	nativePrompt(t, ctx, client, id, "notebook-reread")
	provider.mu.Lock()
	var reread struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(provider.seen[len(provider.seen)-1], &reread); err != nil {
		provider.mu.Unlock()
		t.Fatal(err)
	}
	last := reread.Messages[len(reread.Messages)-1]
	if last.Role != "tool" || !strings.Contains(fmt.Sprint(last.Content), "notebook identity") {
		provider.mu.Unlock()
		t.Fatal("next Turn did not receive the native Notebook Read result")
	}
	for _, request := range provider.seen {
		if strings.Contains(string(request), "UNINHERITED_CONTEXT_SENTINEL") || strings.Contains(string(request), `"name":"Skill"`) {
			provider.mu.Unlock()
			t.Fatal("Notebook CWD implicitly injected AGENTS instructions or global Skill")
		}
	}
	provider.mu.Unlock()
	worker := filepath.Join(root, "worker")
	if err := os.Mkdir(worker, 0700); err != nil {
		t.Fatal(err)
	}
	workerProfile := application.Profile{Version: "worker/1", Instructions: "Separate worker", Model: "openai-compatible/gpt-4.1", ToolsVersion: "worker/1", Execution: "workspace-write", Workspace: application.Workspace{CWD: worker}, NativeTools: []string{}}
	workerResult, err := client.CreateApplicationSession(ctx, appserver.CreateApplicationSessionRequest{WriteBase: appserver.WriteBase{OperationID: "create-worker"}, Profile: workerProfile})
	if err != nil || workerResult.SessionID == "" {
		t.Fatalf("worker create = %+v, %v", workerResult, err)
	}
	provider.set()
	nativePrompt(t, ctx, client, workerResult.SessionID, "worker-independent")
	if _, err := os.Stat(filepath.Join(worker, "MEMORY.md")); !os.IsNotExist(err) {
		t.Fatalf("worker unexpectedly inherited Notebook file: %v", err)
	}
	resourceBytes := []byte("input-resource-bytes\n")
	sum := sha256.Sum256(resourceBytes)
	uploaded, err := client.UploadApplicationResource(ctx, appserver.ApplicationResourceRequest{WriteBase: appserver.WriteBase{SessionID: id, OperationID: "native-upload"}, Name: "input.txt", MediaType: "text/plain", Data: resourceBytes, SHA256: hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}
	// The controlled model takes the relative path from the ReadResource
	// result in its next request. No application code guesses a Store path.
	provider.set(nativeModelTool{"ReadResource", fmt.Sprintf(`{"resource_id":%q}`, uploaded.ID)}, nativeModelTool{"Read", `{"path":"$RESOURCE_PATH"}`}, nativeModelTool{"RunCommand", `{"command":"/bin/cat $RESOURCE_PATH > result.txt"}`}, nativeModelTool{"PublishArtifact", `{"path":"result.txt","name":"result.txt","media_type":"text/plain"}`})
	nativePrompt(t, ctx, client, id, "native-resource")
	got, err := os.ReadFile(filepath.Join(cwd, "result.txt"))
	if err != nil || string(got) != string(resourceBytes) {
		provider.mu.Lock()
		seen := append([][]byte(nil), provider.seen...)
		provider.mu.Unlock()
		t.Fatalf("processed resource = %q, %v; provider requests=%d", got, err, len(seen))
	}
	provider.mu.Lock()
	requests := append([][]byte(nil), provider.seen...)
	provider.mu.Unlock()
	var artifactID string
	idPattern := regexp.MustCompile(`app-resource-[a-f0-9]{64}`)
	for _, raw := range requests {
		for _, candidate := range idPattern.FindAllString(string(raw), -1) {
			if candidate != uploaded.ID {
				artifactID = candidate
			}
		}
	}
	if artifactID == "" {
		t.Fatal("published native resource ID absent from model-visible tool result")
	}
	artifact, err := client.ReadApplicationResource(ctx, id, artifactID)
	if err != nil || string(artifact.Data) != string(resourceBytes) || artifact.Resource.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("published HTTP artifact bytes/digest = %+v, %v", artifact, err)
	}
	other, _ := registerApplicationHTTP(t, ctx, host, "other", filepath.Join(root, "other.credential"))
	if _, err := other.ReadApplicationResource(ctx, id, artifactID); err == nil {
		t.Fatal("other application read published artifact")
	}
}
