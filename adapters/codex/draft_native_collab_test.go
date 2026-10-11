package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
)

// Native MA v1 input can start an adapter-owned root without traversing ACP
// admission. This records the native boundary rather than asserting that a
// local mutex implements app-server's missing start-if-idle compare-and-set.
func TestDraftInstalledNativeCollabCanStartOwnedRoot(t *testing.T) {
	if os.Getenv("CAELIS_TEST_NATIVE_CODEX") == "" {
		t.Skip("set CAELIS_TEST_NATIVE_CODEX to a Codex executable to test native interoperability")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cwd := t.TempDir()
	var receiver atomic.Value
	var sent atomic.Bool
	var calls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.Error(w, "fixture only supports Responses", http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		n := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		writeNativeSSE(w, map[string]any{"type": "response.created", "response": map[string]any{"id": fmt.Sprintf("response-%d", n)}})
		if strings.Contains(string(body), "sender root trigger") && sent.CompareAndSwap(false, true) {
			arguments, _ := json.Marshal(map[string]any{"target": receiver.Load().(string), "message": "native collab input to owned idle root"})
			writeNativeSSE(w, map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "function_call", "namespace": "multi_agent_v1", "name": "send_input", "call_id": "native-send", "arguments": string(arguments)}})
		} else {
			writeNativeSSE(w, map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "message", "role": "assistant", "id": fmt.Sprintf("answer-%d", n), "content": []any{map[string]any{"type": "output_text", "text": "native collab fixture complete"}}}})
		}
		writeNativeSSE(w, map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprintf("response-%d", n), "usage": map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}}})
	}))
	defer provider.Close()
	backend, requests, _ := newInstalledDraftBackend(t, ctx, cwd, provider.URL, "\n[features]\nmulti_agent = true\nmulti_agent_v2 = false\n")
	client, recorder := attachDraftTestClient(t, ctx, backend, cwd)
	first, err := client.NewSession(ctx, v2.NewSessionRequest{Cwd: v2.AbsolutePath(cwd)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.NewSession(ctx, v2.NewSessionRequest{Cwd: v2.AbsolutePath(cwd)})
	if err != nil {
		t.Fatal(err)
	}
	receiver.Store(string(first.SessionId))
	if _, err := client.Prompt(ctx, v2.PromptRequest{SessionId: second.SessionId, Prompt: []v2.ContentBlock{v2.TextBlock("sender root trigger")}}); err != nil {
		t.Fatal(err)
	}
	update := recorder.wait(t, func(update v2.UpdateSessionNotification) bool {
		state, _, activity := draftState(update)
		return update.SessionId == first.SessionId && state == "running" && activity != ""
	})
	_, _, activity := draftState(update)
	recorder.wait(t, func(update v2.UpdateSessionNotification) bool {
		return update.SessionId == first.SessionId && update.Update.UserMessage != nil
	})
	for {
		select {
		case request := <-requests.messages:
			if request.Method == "turn/start" && stringValue(request.Params["threadId"]) == string(first.SessionId) {
				t.Fatal("fixture accidentally started the receiver through ACP/native RPC admission")
			}
		default:
			t.Logf("native multi_agent_v1.send_input started adapter-owned idle root %s activity %s without an ACP/native turn/start RPC; native IdleOnly CAS is unavailable", first.SessionId, activity)
			return
		}
	}
}
