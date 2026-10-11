package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/caelis-labs/caelis/adapters/codex/internal/appserver"
	"github.com/caelis-labs/caelis/internal/acpdraft"
)

func nativeEdgeProvider(t *testing.T, firstTool map[string]any, release ...<-chan struct{}) *httptest.Server {
	t.Helper()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.Error(w, "fixture only supports Responses", http.StatusNotFound)
			return
		}
		n := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		id := fmt.Sprintf("native-edge-%d", n)
		writeNativeSSE(w, map[string]any{"type": "response.created", "response": map[string]any{"id": id}})
		if n == 1 && len(release) > 0 {
			w.(http.Flusher).Flush()
			select {
			case <-release[0]:
			case <-r.Context().Done():
				return
			}
		}
		if n == 1 && firstTool != nil {
			arguments, _ := json.Marshal(firstTool)
			writeNativeSSE(w, map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "function_call", "name": "exec_command", "call_id": "edge-command", "arguments": string(arguments)}})
		} else {
			writeNativeSSE(w, map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "message", "role": "assistant", "id": id + "-answer", "content": []any{map[string]any{"type": "output_text", "text": "isolated fixture complete"}}}})
		}
		writeNativeSSE(w, map[string]any{"type": "response.completed", "response": map[string]any{"id": id, "usage": map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}}})
	}))
	t.Cleanup(server.Close)
	return server
}

func TestDraftInstalledNativeSignedExitCompletion(t *testing.T) {
	if os.Getenv("CAELIS_TEST_NATIVE_CODEX") == "" {
		t.Skip("set CAELIS_TEST_NATIVE_CODEX to test isolated native interoperability")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cwd := t.TempDir()
	release := make(chan struct{})
	var released sync.Once
	defer released.Do(func() { close(release) })
	provider := nativeEdgeProvider(t, nil, release)
	backend, _, responses := newInstalledDraftBackend(t, ctx, cwd, provider.URL)
	client, recorder := attachDraftTestClient(t, ctx, backend, cwd)
	opened, err := client.NewSession(ctx, v2.NewSessionRequest{Cwd: v2.AbsolutePath(cwd)})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := client.Prompt(ctx, v2.PromptRequest{SessionId: opened.SessionId, Prompt: []v2.ContentBlock{v2.TextBlock("run isolated timeout fixture")}})
	if err != nil {
		t.Fatal(err)
	}
	activity := acpdraft.ActivityID(receipt.Meta)
	// Native user-shell timeout uses the signed -1 sentinel. A zero timeout
	// expires the fixture command before it can execute. The actual ACP Turn
	// stays running behind the controlled model response until released below.
	if err := backend.rpc.Request(ctx, "thread/shellCommand", map[string]any{"threadId": string(opened.SessionId), "command": "printf native-timeout-fixture", "timeoutMs": 0}, nil); err != nil {
		t.Fatal(err)
	}
	completed := responses.wait(t, ctx, func(message draftRPCMessage) bool {
		var item threadItem
		return message.Method == "item/completed" && json.Unmarshal(message.Params["item"], &item) == nil && item.Type == "commandExecution"
	})
	var item threadItem
	if err := json.Unmarshal(completed.Params["item"], &item); err != nil || item.ExitCode == nil || *item.ExitCode != -1 {
		t.Fatalf("installed native timeout exit = %+v, %v; wire=%s", item, err, completed.Params["item"])
	}
	if stringValue(completed.Params["turnId"]) != activity {
		t.Fatalf("native command completed under a different activity: %s, want %s", completed.Params["turnId"], activity)
	}
	terminal := recorder.wait(t, func(update v2.UpdateSessionNotification) bool {
		return update.Update.TerminalUpdate != nil && update.Update.TerminalUpdate.ExitStatus != nil
	})
	if terminal.Update.TerminalUpdate.ExitStatus.ExitCode != nil {
		t.Fatalf("native -1 wrapped or invented an exit code: %+v", terminal.Update.TerminalUpdate)
	}
	assertDraftNoIdle(t, recorder, activity)
	released.Do(func() { close(release) })
	recorder.wait(t, func(update v2.UpdateSessionNotification) bool {
		state, stop, id := draftState(update)
		return state == "idle" && stop == "end_turn" && id == activity
	})
	if backend.route(string(opened.SessionId)) == nil {
		t.Fatal("valid native signed exit dropped the route")
	}
	t.Logf("native command %s exitCode=-1 became standard exitStatus without exitCode; actual activity %s completed", item.ID, activity)
}

func TestDraftInstalledNativeSkillMentionHistory(t *testing.T) {
	if os.Getenv("CAELIS_TEST_NATIVE_CODEX") == "" {
		t.Skip("set CAELIS_TEST_NATIVE_CODEX to test isolated native interoperability")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cwd := t.TempDir()
	skill := filepath.Join(cwd, ".agents", "skills", "edge-fixture", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("---\nname: edge-fixture\ndescription: Isolated native protocol fixture\n---\nReturn the fixture response.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	provider := nativeEdgeProvider(t, nil)
	backend, _, _ := newInstalledDraftBackend(t, ctx, cwd, provider.URL)
	client, live := attachDraftTestClient(t, ctx, backend, cwd)
	opened, err := client.NewSession(ctx, v2.NewSessionRequest{Cwd: v2.AbsolutePath(cwd)})
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	// Native selections may originate in another native surface. Exercise their
	// real wire form through the isolated owner, not a fabricated text selection.
	err = backend.rpc.Request(ctx, "turn/start", map[string]any{"threadId": string(opened.SessionId), "input": []any{map[string]any{"type": "text", "text": "use selected references"}, map[string]any{"type": "skill", "name": "edge-fixture", "path": skill}, map[string]any{"type": "mention", "name": "reference", "path": "mcp://edge-fixture"}}}, &started)
	if err != nil || started.Turn.ID == "" {
		t.Fatalf("native selection start: %+v, %v", started, err)
	}
	message := live.wait(t, func(update v2.UpdateSessionNotification) bool {
		return update.Update.UserMessage != nil && len(update.Update.UserMessage.Content) == 3
	})
	if message.Update.UserMessage.Content[1].ResourceLink == nil || message.Update.UserMessage.Content[1].ResourceLink.Name != "edge-fixture" || message.Update.UserMessage.Content[2].ResourceLink == nil || message.Update.UserMessage.Content[2].ResourceLink.Uri != "mcp://edge-fixture" {
		t.Fatalf("native selected references were flattened/lost: %+v", message)
	}
	live.wait(t, func(update v2.UpdateSessionNotification) bool {
		state, _, id := draftState(update)
		return state == "idle" && id == started.Turn.ID
	})
	if _, err := client.CloseSession(ctx, v2.CloseSessionRequest{SessionId: opened.SessionId}); err != nil {
		t.Fatal(err)
	}
	reconnected, replay := attachDraftTestClient(t, ctx, backend, cwd)
	_, err = reconnected.ResumeSession(ctx, v2.ResumeSessionRequest{SessionId: opened.SessionId, Cwd: v2.AbsolutePath(cwd), ReplayFrom: &v2.ReplayFrom{Start: &v2.ReplayFromStartInline{Type: "start"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"edge-fixture", "reference"} {
		replay.wait(t, func(update v2.UpdateSessionNotification) bool {
			chunk := update.Update.UserMessageChunk
			return chunk != nil && chunk.MessageId == message.Update.UserMessage.MessageId && chunk.Content.ResourceLink != nil && chunk.Content.ResourceLink.Name == name
		})
	}
	t.Log("installed native {name,path} selections kept the same item-derived message ID through live projection and actual thread/read replay")
}

func TestDraftInstalledNativeProjectionFailureCancelsApprovalAndKeepsOtherTransport(t *testing.T) {
	if os.Getenv("CAELIS_TEST_NATIVE_CODEX") == "" {
		t.Skip("set CAELIS_TEST_NATIVE_CODEX to test isolated native interoperability")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cwd := t.TempDir()
	provider := nativeEdgeProvider(t, map[string]any{"cmd": "printf approval-fixture", "workdir": cwd, "sandbox_permissions": "require_escalated", "justification": "Isolated native protocol fixture"})
	backend, requests, responses := newInstalledDraftBackend(t, ctx, cwd, provider.URL)
	client, recorder := attachDraftTestClient(t, ctx, backend, cwd)
	other, otherRecorder := attachDraftTestClient(t, ctx, backend, cwd)
	opened, err := client.NewSession(ctx, v2.NewSessionRequest{Cwd: v2.AbsolutePath(cwd)})
	if err != nil {
		t.Fatal(err)
	}
	otherOpened, err := other.NewSession(ctx, v2.NewSessionRequest{Cwd: v2.AbsolutePath(cwd)})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := client.Prompt(ctx, v2.PromptRequest{SessionId: opened.SessionId, Prompt: []v2.ContentBlock{v2.TextBlock("request approval fixture")}})
	if err != nil {
		t.Fatal(err)
	}
	permission := responses.wait(t, ctx, func(message draftRPCMessage) bool { return message.Method == "item/commandExecution/requestApproval" })
	select {
	case request := <-recorder.permissions:
		if len(request.Options) != 4 || request.Subject == nil || request.Subject.ToolCall == nil {
			t.Fatalf("native approval projection changed: %+v", request)
		}
	case <-ctx.Done():
		t.Fatal(context.Cause(ctx))
	}
	activity := acpdraft.ActivityID(receipt.Meta)
	params, _ := json.Marshal(map[string]any{"threadId": string(opened.SessionId), "turnId": activity, "item": map[string]any{"id": "injected-invalid-item", "type": "userMessage", "content": []any{map[string]any{"type": "future-native-content"}}}})
	// Inject only the failing native notification into the production router.
	// Native start/receipt, approval identity, cancellation and the second
	// Session all use the actual installed process and real SDK transports.
	backend.onNotification(appserver.Notification{Method: "item/started", Params: params})
	waitDraftConnectionLoss(t, ctx, client)
	interrupted, resolved := false, false
	for !interrupted || !resolved {
		select {
		case message := <-requests.messages:
			switch {
			case message.Method == "turn/interrupt" && stringValue(message.Params["threadId"]) == string(opened.SessionId):
				if stringValue(message.Params["turnId"]) != activity {
					t.Fatalf("interrupt does not target actual receipt activity: %+v", message)
				}
				interrupted = true
			case string(message.ID) == string(permission.ID) && message.Method == "":
				var result struct {
					Decision string `json:"decision"`
				}
				if json.Unmarshal(message.Result, &result) != nil || result.Decision != "cancel" {
					t.Fatalf("native permission was lost/granted: %+v", message)
				}
				resolved = true
			}
		case <-ctx.Done():
			t.Fatalf("original native cleanup missing: interrupt=%v permission=%v: %v", interrupted, resolved, context.Cause(ctx))
		}
	}
	assertDraftNoIdle(t, recorder, activity)
	select {
	case <-backend.Done():
		t.Fatalf("shared native process was closed: %v", backend.Err())
	case <-other.Done():
		t.Fatalf("other transport was closed: %v", other.Err())
	default:
	}
	otherReceipt, err := other.Prompt(ctx, v2.PromptRequest{SessionId: otherOpened.SessionId, Prompt: []v2.ContentBlock{v2.TextBlock("independent native work")}})
	if err != nil {
		t.Fatal(err)
	}
	otherActivity := acpdraft.ActivityID(otherReceipt.Meta)
	otherRecorder.wait(t, func(update v2.UpdateSessionNotification) bool {
		state, stop, id := draftState(update)
		return state == "idle" && stop == "end_turn" && id == otherActivity
	})
	t.Log("actual native approval ID resolved cancel, actual receipt Turn interrupted; one lost projection was observable without dropping the shared backend or independent native Session")
}
