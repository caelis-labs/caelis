package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/caelis-labs/caelis/internal/acpdraft"
)

// This optional interoperability test runs a real, isolated native process.
// Its only model provider is an in-process fixture; it does not use the user's
// home, credentials, configuration, model service, or running app-server.
func TestDraftInstalledNativeAppServerInsertionAndSteering(t *testing.T) {
	if os.Getenv("CAELIS_TEST_NATIVE_CODEX") == "" {
		t.Skip("set CAELIS_TEST_NATIVE_CODEX to a Codex executable to test native interoperability")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	release := make(chan struct{})
	var released sync.Once
	defer released.Do(func() { close(release) })
	var calls atomic.Int64
	cancelReady := make(chan struct{}, 1)
	cwd := t.TempDir()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.Error(w, "fixture only supports Responses", http.StatusNotFound)
			return
		}
		n := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		writeNativeSSE(w, map[string]any{"type": "response.created", "response": map[string]any{"id": fmt.Sprintf("response-%d", n)}})
		w.(http.Flusher).Flush()
		if n == 1 {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			case <-ctx.Done():
				return
			}
		}
		switch n {
		case 3:
			arguments, _ := json.Marshal(map[string]any{"cmd": "printf approval-fixture", "workdir": cwd, "sandbox_permissions": "require_escalated", "justification": "Isolated native approval fixture"})
			writeNativeSSE(w, map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "function_call", "name": "exec_command", "call_id": "approval-call", "arguments": string(arguments)}})
		case 4:
			cancelReady <- struct{}{}
			select {
			case <-r.Context().Done():
			case <-ctx.Done():
			}
			return
		default:
			writeNativeSSE(w, map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "message", "role": "assistant", "id": fmt.Sprintf("answer-%d", n), "content": []any{map[string]any{"type": "output_text", "text": "fixture answer"}}}})
		}
		writeNativeSSE(w, map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprintf("response-%d", n), "usage": map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}}})
	}))
	defer provider.Close()
	backend, requests, responses := newInstalledDraftBackend(t, ctx, cwd, provider.URL)
	client, recorder := attachDraftTestClient(t, ctx, backend, cwd)
	opened, err := client.NewSession(ctx, v2.NewSessionRequest{Cwd: v2.AbsolutePath(cwd)})
	if err != nil {
		t.Fatal(err)
	}
	first, err := client.Prompt(ctx, v2.PromptRequest{SessionId: opened.SessionId, Prompt: []v2.ContentBlock{v2.TextBlock("first input")}})
	if err != nil {
		t.Fatal(err)
	}
	activity := acpdraft.ActivityID(first.Meta)
	if first.MessageId == "" || activity == "" {
		t.Fatalf("native prompt receipt is incomplete: %+v", first)
	}
	recorder.wait(t, func(update v2.UpdateSessionNotification) bool {
		return update.Update.UserMessage != nil && update.Update.UserMessage.MessageId == first.MessageId
	})
	steered := make(chan error, 1)
	var second v2.PromptResponse
	go func() {
		var err error
		second, err = client.Prompt(ctx, v2.PromptRequest{SessionId: opened.SessionId, Prompt: []v2.ContentBlock{v2.TextBlock("steered input")}, Meta: acpdraft.ActiveOnlyMeta(activity)})
		steered <- err
	}()
	steerRequest := requests.wait(t, ctx, func(message draftRPCMessage) bool { return message.Method == "turn/steer" })
	if stringValue(steerRequest.Params["expectedTurnId"]) != activity || stringValue(steerRequest.Params["clientUserMessageId"]) == "" {
		t.Fatalf("native CAS/correlation was lost: %+v", steerRequest)
	}
	ack := responses.wait(t, ctx, func(message draftRPCMessage) bool {
		return bytes.Equal(message.ID, steerRequest.ID) && (message.Result != nil || message.Error != nil)
	})
	if ack.Error != nil {
		t.Fatalf("native rejected active-only steering: %s", ack.Error)
	}
	released.Do(func() { close(release) })
	select {
	case err = <-steered:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(context.Cause(ctx))
	}
	if second.MessageId == "" || second.MessageId == first.MessageId || acpdraft.ActivityID(second.Meta) != activity {
		t.Fatalf("native steering receipt lost identity: %+v", second)
	}
	recorder.wait(t, func(update v2.UpdateSessionNotification) bool {
		return update.Update.UserMessage != nil && update.Update.UserMessage.MessageId == second.MessageId
	})
	recorder.wait(t, func(update v2.UpdateSessionNotification) bool {
		state, stop, turn := draftState(update)
		return state == "idle" && stop == "end_turn" && turn == activity
	})
	if calls.Load() == 0 {
		t.Fatal("native process never used the isolated model provider")
	}
	t.Logf("native start/steer receipts %s / %s, same activity %s; mock Responses calls %d", first.MessageId, second.MessageId, activity, calls.Load())
	if _, err := client.CloseSession(ctx, v2.CloseSessionRequest{SessionId: opened.SessionId}); err != nil {
		t.Fatal(err)
	}
	if backend.route(string(opened.SessionId)) != nil {
		t.Fatal("closed native Session retained an owner route")
	}
	_ = client.Close()
	reconnected, replay := attachDraftTestClient(t, ctx, backend, cwd)
	if _, err := reconnected.ResumeSession(ctx, v2.ResumeSessionRequest{SessionId: opened.SessionId, Cwd: v2.AbsolutePath(cwd), ReplayFrom: &v2.ReplayFrom{Start: &v2.ReplayFromStartInline{Type: "start"}}}); err != nil {
		t.Fatal(err)
	}
	for _, messageID := range []v2.MessageId{first.MessageId, second.MessageId} {
		replay.wait(t, func(update v2.UpdateSessionNotification) bool {
			return update.Update.UserMessageChunk != nil && update.Update.UserMessageChunk.MessageId == messageID
		})
	}
	if calls.Load() != 2 {
		t.Fatal("resume resubmitted previously accepted input")
	}
	third, err := reconnected.Prompt(ctx, v2.PromptRequest{SessionId: opened.SessionId, Prompt: []v2.ContentBlock{v2.TextBlock("exercise native approval then cancel")}})
	if err != nil {
		t.Fatal(err)
	}
	var approval v2.RequestPermissionRequest
	select {
	case approval = <-replay.permissions:
	case <-ctx.Done():
		t.Fatal(context.Cause(ctx))
	}
	if approval.SessionId != opened.SessionId || approval.Subject == nil || approval.Subject.ToolCall == nil || approval.Subject.ToolCall.ToolCall.ToolCallId == "" {
		t.Fatalf("lost native approval identity: %+v", approval)
	}
	var reject v2.PermissionOptionId
	for _, option := range approval.Options {
		if option.OptionId == "cancel" && string(option.Kind) == "reject_once" {
			reject = option.OptionId
		}
	}
	if reject == "" {
		t.Fatalf("lost native rejection option: %+v", approval.Options)
	}
	// Rejecting the real native approval keeps this test free of tool execution.
	replay.decisions <- v2.RequestPermissionResponse{Outcome: v2.RequestPermissionOutcome{Selected: &v2.RequestPermissionOutcomeSelected{Outcome: "selected", OptionId: reject}}}
	thirdActivity := acpdraft.ActivityID(third.Meta)
	replay.wait(t, func(update v2.UpdateSessionNotification) bool {
		state, _, turn := draftState(update)
		return state == "idle" && turn == thirdActivity
	})
	fourth, err := reconnected.Prompt(ctx, v2.PromptRequest{SessionId: opened.SessionId, Prompt: []v2.ContentBlock{v2.TextBlock("exercise native cancellation")}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelReady:
	case <-ctx.Done():
		t.Fatal(context.Cause(ctx))
	}
	if err := reconnected.Cancel(ctx, v2.CancelSessionNotification{SessionId: opened.SessionId}); err != nil {
		t.Fatal(err)
	}
	interrupt := requests.wait(t, ctx, func(message draftRPCMessage) bool { return message.Method == "turn/interrupt" })
	fourthActivity := acpdraft.ActivityID(fourth.Meta)
	if stringValue(interrupt.Params["threadId"]) != string(opened.SessionId) || stringValue(interrupt.Params["turnId"]) != fourthActivity {
		t.Fatalf("native cancellation targeted another activity: %+v", interrupt)
	}
	replay.wait(t, func(update v2.UpdateSessionNotification) bool {
		state, stop, turn := draftState(update)
		return state == "idle" && stop == "cancelled" && turn == fourthActivity
	})
	t.Logf("native reconnect replay retained both message IDs; native approval used %d original options; cancellation completed activity %s", len(approval.Options), fourthActivity)
}

func newInstalledDraftBackend(t *testing.T, ctx context.Context, cwd, providerURL string, extraConfig ...string) (*Backend, *nativeDraftWireTap, *nativeDraftWireTap) {
	t.Helper()
	native := os.Getenv("CAELIS_TEST_NATIVE_CODEX")
	if native == "" {
		t.Skip("set CAELIS_TEST_NATIVE_CODEX to a Codex executable to test native interoperability")
	}
	home := t.TempDir()
	config := fmt.Sprintf("model = \"mock-model\"\nmodel_provider = \"fixture\"\napproval_policy = \"on-request\"\nsandbox_mode = \"workspace-write\"\n[analytics]\nenabled = false\n[model_providers.fixture]\nname = \"Isolated ACP fixture\"\nbase_url = %q\nwire_api = \"responses\"\nrequest_max_retries = 0\nstream_max_retries = 0\nsupports_websockets = false\nrequires_openai_auth = false\n", providerURL+"/v1")
	config += strings.Join(extraConfig, "\n")
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	processCtx, stop := context.WithCancel(ctx)
	cmd := exec.CommandContext(processCtx, native, "app-server", "--stdio")
	cmd.Dir = cwd
	// Pass only process essentials and the dedicated native configuration home.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + os.TempDir(), "CODEX_HOME=" + home, "RUST_LOG=error"}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	nativeOutput, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	nativeInput, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		stop()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = nativeInput.Close()
		stop()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("isolated native stderr: %s", stderr.String())
		}
	})
	requests := &nativeDraftWireTap{writer: nativeInput, messages: make(chan draftRPCMessage, 256)}
	responses := &nativeDraftWireTap{reader: nativeOutput, messages: make(chan draftRPCMessage, 256)}
	backend, err := NewBackend(ctx, responses, requests)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	t.Logf("native user agent: %s", backend.nativeUserAgent)
	if !draftNativeCorrelationSupported(backend.nativeUserAgent) {
		t.Fatalf("native app-server does not meet the correlation contract: %s", backend.nativeUserAgent)
	}
	return backend, requests, responses
}

func writeNativeSSE(w io.Writer, event any) {
	raw, _ := json.Marshal(event)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
}

type nativeDraftWireTap struct {
	reader   io.Reader
	writer   io.Writer
	mu       sync.Mutex
	pending  []byte
	messages chan draftRPCMessage
}

func (t *nativeDraftWireTap) observe(data []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending = append(t.pending, data...)
	for {
		line, remaining, ok := bytes.Cut(t.pending, []byte{'\n'})
		if !ok {
			return
		}
		t.pending = remaining
		var message draftRPCMessage
		if json.Unmarshal(line, &message) == nil {
			select {
			case t.messages <- message:
			default:
			}
		}
	}
}
func (t *nativeDraftWireTap) Read(data []byte) (int, error) {
	n, err := t.reader.Read(data)
	t.observe(data[:n])
	return n, err
}
func (t *nativeDraftWireTap) Write(data []byte) (int, error) {
	n, err := t.writer.Write(data)
	t.observe(data[:n])
	return n, err
}
func (tap *nativeDraftWireTap) wait(t *testing.T, ctx context.Context, accept func(draftRPCMessage) bool) draftRPCMessage {
	t.Helper()
	for {
		select {
		case message := <-tap.messages:
			if accept(message) {
				return message
			}
		case <-ctx.Done():
			t.Fatal(context.Cause(ctx))
			return draftRPCMessage{}
		}
	}
}

func attachDraftTestClient(t *testing.T, ctx context.Context, backend *Backend, cwd string) (*v2.ClientSideConnection, *draftRecorder) {
	t.Helper()
	peerInput, peerOutput := io.Pipe()
	serverInput, serverOutput := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- backend.ServeACPDraft(ctx, ConnectionOptions{Workspace: WorkspacePolicy{AllowedRoots: []string{cwd}, WritableRoots: []string{cwd}}}, acpdraft.SchemaTag, serverInput, peerOutput)
	}()
	recorder := &draftRecorder{permissions: make(chan v2.RequestPermissionRequest, 16), decisions: make(chan v2.RequestPermissionResponse, 16)}
	client, err := v2.NewClientSideConnection(recorder, serverOutput, peerInput)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = peerInput.Close()
		_ = peerOutput.Close()
		_ = serverInput.Close()
		_ = serverOutput.Close()
		select {
		case err := <-done:
			if err != nil && ctx.Err() == nil {
				t.Logf("draft connection closed: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("draft connection did not close")
		}
	})
	response, err := client.Initialize(ctx, v2.InitializeRequest{ProtocolVersion: 2, Info: v2.Implementation{Name: "native-interop-test", Version: "1"}, Meta: acpdraft.InitializeMeta()})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateNativeDraftPeer(response); err != nil {
		t.Fatal(err)
	}
	return client, recorder
}
