package codex

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/caelis-labs/caelis/internal/acpdraft"
)

type draftRPCMessage struct {
	ID     json.RawMessage            `json:"id"`
	Method string                     `json:"method"`
	Params map[string]json.RawMessage `json:"params"`
	Result json.RawMessage            `json:"result"`
	Error  json.RawMessage            `json:"error"`
}
type draftRPCFake struct {
	backend  *Backend
	tokens   map[string]string
	output   io.Writer
	mu       sync.Mutex
	messages chan draftRPCMessage
}

func newDraftRPCFake(input io.Reader, output io.Writer) *draftRPCFake {
	fake := &draftRPCFake{output: output, messages: make(chan draftRPCMessage, 64), tokens: map[string]string{}}
	go func() {
		reader := bufio.NewReader(input)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}
			var msg draftRPCMessage
			if json.Unmarshal(line, &msg) != nil {
				continue
			}
			switch msg.Method {
			case "initialize":
				fake.reply(msg, map[string]any{"userAgent": "caelis-codex-acp/0.160.0 (test)"})
			case "account/read":
				fake.reply(msg, map[string]any{"account": map[string]any{"type": "chatgpt"}})
			case "model/list":
				fake.reply(msg, map[string]any{"data": []any{map[string]any{"id": "gpt-test", "model": "gpt-test", "defaultReasoningEffort": "high", "supportedReasoningEfforts": []any{map[string]any{"reasoningEffort": "high"}}, "serviceTiers": []any{map[string]any{"id": "fast", "name": "Fast"}}}}})
			case "thread/unsubscribe":
				fake.reply(msg, map[string]any{"status": "unsubscribed"})
			default:
				if msg.Method == "turn/start" || msg.Method == "turn/steer" {
					fake.mu.Lock()
					fake.tokens[stringValue(msg.Params["threadId"])] = stringValue(msg.Params["clientUserMessageId"])
					fake.mu.Unlock()
				}
				fake.messages <- msg
			}
		}
	}()
	return fake
}
func (f *draftRPCFake) send(value any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := json.Marshal(value)
	_, _ = f.output.Write(append(raw, '\n'))
}
func (f *draftRPCFake) reply(msg draftRPCMessage, result any) {
	f.send(map[string]any{"id": msg.ID, "result": result})
}
func (f *draftRPCFake) notify(method string, params any) {
	f.send(map[string]any{"method": method, "params": params})
}
func (f *draftRPCFake) next(t *testing.T, method string) draftRPCMessage {
	t.Helper()
	select {
	case msg := <-f.messages:
		if msg.Method != method {
			t.Fatalf("native method = %s, want %s; result %s", msg.Method, method, msg.Result)
		}
		return msg
	case <-time.After(5 * time.Second):
		t.Fatalf("waiting native %s", method)
		return draftRPCMessage{}
	}
}

type draftRecorder struct {
	mu          sync.Mutex
	updates     []v2.UpdateSessionNotification
	permissions chan v2.RequestPermissionRequest
	decisions   chan v2.RequestPermissionResponse
}

func (r *draftRecorder) SessionUpdate(_ context.Context, notification v2.UpdateSessionNotification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updates = append(r.updates, notification)
	return nil
}
func (r *draftRecorder) RequestPermission(ctx context.Context, request v2.RequestPermissionRequest) (v2.RequestPermissionResponse, error) {
	select {
	case r.permissions <- request:
	case <-ctx.Done():
		return v2.RequestPermissionResponse{}, context.Cause(ctx)
	}
	select {
	case reply := <-r.decisions:
		return reply, nil
	case <-ctx.Done():
		return v2.RequestPermissionResponse{}, context.Cause(ctx)
	}
}
func (r *draftRecorder) snapshot() []v2.UpdateSessionNotification {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]v2.UpdateSessionNotification(nil), r.updates...)
}
func (r *draftRecorder) wait(t *testing.T, predicate func(v2.UpdateSessionNotification) bool) v2.UpdateSessionNotification {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, update := range r.snapshot() {
			if predicate(update) {
				return update
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("waiting ACP draft update")
	return v2.UpdateSessionNotification{}
}
func draftState(update v2.UpdateSessionNotification) (string, string, string) {
	if update.Update.StateUpdate == nil {
		return "", "", ""
	}
	raw, _ := json.Marshal(update.Update)
	var fields struct {
		State string                     `json:"state"`
		Stop  string                     `json:"stopReason"`
		Meta  map[string]json.RawMessage `json:"_meta"`
	}
	_ = json.Unmarshal(raw, &fields)
	return fields.State, fields.Stop, acpdraft.ActivityID(fields.Meta)
}
func newDraftFixture(t *testing.T) (*draftRPCFake, *v2.ClientSideConnection, *draftRecorder, context.Context, string) {
	t.Helper()
	appInput, appOutput := io.Pipe()
	adapterInput, adapterOutput := io.Pipe()
	fake := newDraftRPCFake(adapterInput, appOutput)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	backend, err := NewBackend(ctx, appInput, adapterOutput)
	if err != nil {
		t.Fatal(err)
	}
	fake.backend = backend
	peerInput, peerOutput := io.Pipe()
	serverInput, serverOutput := io.Pipe()
	workspace := t.TempDir()
	done := make(chan error, 1)
	go func() {
		done <- backend.ServeACPDraft(ctx, ConnectionOptions{Workspace: WorkspacePolicy{AllowedRoots: []string{workspace}, WritableRoots: []string{workspace}}}, acpdraft.SchemaTag, serverInput, peerOutput)
	}()
	recorder := &draftRecorder{permissions: make(chan v2.RequestPermissionRequest, 16), decisions: make(chan v2.RequestPermissionResponse, 16)}
	client, err := v2.NewClientSideConnection(recorder, serverOutput, peerInput)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = backend.Close()
		_ = appInput.Close()
		_ = appOutput.Close()
		_ = adapterInput.Close()
		_ = adapterOutput.Close()
		_ = peerInput.Close()
		_ = peerOutput.Close()
		_ = serverInput.Close()
		_ = serverOutput.Close()
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("draft connection did not close")
		}
	})
	response, err := client.Initialize(ctx, v2.InitializeRequest{ProtocolVersion: 2, Info: v2.Implementation{Name: "caelis-test", Version: "1"}, Meta: acpdraft.InitializeMeta()})
	if err != nil {
		t.Fatal(err)
	}
	if err = validateNativeDraftPeer(response); err != nil {
		t.Fatal(err)
	}
	return fake, client, recorder, ctx, workspace
}
func openDraftSession(t *testing.T, fake *draftRPCFake, client *v2.ClientSideConnection, ctx context.Context, cwd, threadID string) v2.SessionId {
	t.Helper()
	done := make(chan error, 1)
	var result v2.NewSessionResponse
	go func() {
		var err error
		result, err = client.NewSession(ctx, v2.NewSessionRequest{Cwd: v2.AbsolutePath(cwd)})
		done <- err
	}()
	req := fake.next(t, "thread/start")
	fake.reply(req, map[string]any{"thread": map[string]any{"id": threadID, "cwd": cwd}, "model": "gpt-test", "reasoningEffort": "high"})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if string(result.SessionId) != threadID {
		t.Fatalf("session=%s", result.SessionId)
	}
	return result.SessionId
}
func echoDraftUser(fake *draftRPCFake, session, turn, item, text string) {
	fake.mu.Lock()
	token := fake.tokens[session]
	fake.mu.Unlock()
	fake.notify("item/started", map[string]any{"threadId": session, "turnId": turn, "item": map[string]any{"id": item, "type": "userMessage", "clientId": token, "content": []any{map[string]any{"type": "text", "text": text}}}})
}
func finishDraftTurn(fake *draftRPCFake, session, turn, status string) {
	fake.notify("turn/completed", map[string]any{"threadId": session, "turn": map[string]any{"id": turn, "status": status}})
}

func TestDraftPromptAcknowledgesNativeInsertionAndCompletesAsynchronously(t *testing.T) {
	fake, client, recorder, ctx, cwd := newDraftFixture(t)
	session := openDraftSession(t, fake, client, ctx, cwd, "thread-1")
	done := make(chan error, 1)
	var response v2.PromptResponse
	go func() {
		var err error
		response, err = client.Prompt(ctx, v2.PromptRequest{SessionId: session, Prompt: []v2.ContentBlock{v2.TextBlock("hello")}})
		done <- err
	}()
	req := fake.next(t, "turn/start")
	fake.reply(req, map[string]any{"turn": map[string]any{"id": "turn-1"}})
	select {
	case err := <-done:
		t.Fatalf("receipt arrived before real user insertion: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	echoDraftUser(fake, "thread-1", "turn-1", "user-1", "hello")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	want := v2.MessageId(stableID("thread-1", "user-1", "user-message"))
	if response.MessageId != want {
		t.Fatalf("messageId=%s, want %s", response.MessageId, want)
	}
	recorder.wait(t, func(u v2.UpdateSessionNotification) bool {
		return u.Update.UserMessage != nil && u.Update.UserMessage.MessageId == response.MessageId
	})
	recorder.wait(t, func(u v2.UpdateSessionNotification) bool {
		s, _, id := draftState(u)
		return s == "running" && id == "turn-1"
	})
	for _, u := range recorder.snapshot() {
		s, _, id := draftState(u)
		if s == "idle" && id == "turn-1" {
			t.Fatal("receipt fabricated completion")
		}
	}
	fake.notify("item/agentMessage/delta", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "itemId": "answer-1", "delta": "partial"})
	fake.notify("item/completed", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "item": map[string]any{"id": "answer-1", "type": "agentMessage", "text": "complete"}})
	recorder.wait(t, func(u v2.UpdateSessionNotification) bool {
		return u.Update.AgentMessage != nil && u.Update.AgentMessage.MessageId == v2.MessageId(stableID("thread-1", "answer-1", "agent-message"))
	})
	finishDraftTurn(fake, "thread-1", "turn-1", "completed")
	recorder.wait(t, func(u v2.UpdateSessionNotification) bool {
		s, stop, id := draftState(u)
		return s == "idle" && stop == "end_turn" && id == "turn-1"
	})
}

func TestDraftActiveOnlyUsesNativeCASAndEchoFence(t *testing.T) {
	fake, client, recorder, ctx, cwd := newDraftFixture(t)
	session := openDraftSession(t, fake, client, ctx, cwd, "thread-1")
	_, err := client.Prompt(ctx, v2.PromptRequest{SessionId: session, Meta: acpdraft.ActiveOnlyMeta("missing"), Prompt: []v2.ContentBlock{v2.TextBlock("never")}})
	if !acpdraft.IsInactivePromptError(err) {
		t.Fatalf("idle guard=%v", err)
	}
	select {
	case req := <-fake.messages:
		t.Fatalf("idle guard dispatched %s", req.Method)
	default:
	}
	fake.notify("turn/started", map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": "turn-1"}})
	recorder.wait(t, func(u v2.UpdateSessionNotification) bool {
		s, _, id := draftState(u)
		return s == "running" && id == "turn-1"
	})
	done := make(chan error, 1)
	var reply v2.PromptResponse
	go func() {
		var err error
		reply, err = client.Prompt(ctx, v2.PromptRequest{SessionId: session, Meta: acpdraft.ActiveOnlyMeta("turn-1"), Prompt: []v2.ContentBlock{v2.TextBlock("steer")}})
		done <- err
	}()
	req := fake.next(t, "turn/steer")
	if stringValue(req.Params["expectedTurnId"]) != "turn-1" {
		t.Fatalf("steer target=%s", req.Params)
	}
	fake.reply(req, map[string]any{"turnId": "turn-1"})
	select {
	case err := <-done:
		t.Fatalf("turn ack fabricated insertion=%v", err)
	case <-time.After(20 * time.Millisecond):
	}
	echoDraftUser(fake, "thread-1", "turn-1", "user-steer", "steer")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if reply.MessageId != v2.MessageId(stableID("thread-1", "user-steer", "user-message")) {
		t.Fatalf("receipt=%+v", reply)
	}
	finishDraftTurn(fake, "thread-1", "turn-1", "completed")
	recorder.wait(t, func(u v2.UpdateSessionNotification) bool {
		s, _, id := draftState(u)
		return s == "idle" && id == "turn-1"
	})
	_, err = client.Prompt(ctx, v2.PromptRequest{SessionId: session, Meta: acpdraft.ActiveOnlyMeta("turn-1"), Prompt: []v2.ContentBlock{v2.TextBlock("stale")}})
	if !acpdraft.IsInactivePromptError(err) {
		t.Fatalf("stale guard=%v", err)
	}
}

func TestDraftNativeTerminalUsesStandardByteChunksAndExit(t *testing.T) {
	fake, client, recorder, ctx, cwd := newDraftFixture(t)
	_ = openDraftSession(t, fake, client, ctx, cwd, "thread-1")
	fake.notify("item/started", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "item": map[string]any{"id": "command-1", "type": "commandExecution", "cwd": cwd, "command": "echo hi"}})
	fake.notify("item/commandExecution/outputDelta", map[string]any{"threadId": "thread-1", "itemId": "command-1", "delta": "hi\n"})
	fake.notify("item/completed", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "item": map[string]any{"id": "command-1", "type": "commandExecution", "cwd": cwd, "command": "echo hi", "aggregatedOutput": "hi\n", "exitCode": 0, "status": "completed"}})
	terminal := v2.TerminalId(stableID("thread-1", "command-1", "tool"))
	recorder.wait(t, func(u v2.UpdateSessionNotification) bool {
		return u.Update.TerminalUpdate != nil && u.Update.TerminalUpdate.TerminalId == terminal && u.Update.TerminalUpdate.ExitStatus != nil
	})
	var output string
	for _, u := range recorder.snapshot() {
		if u.Update.TerminalOutputChunk != nil {
			data, err := base64.StdEncoding.DecodeString(u.Update.TerminalOutputChunk.Data)
			if err != nil {
				t.Fatal(err)
			}
			output += string(data)
		}
		if u.Update.ToolCallUpdate != nil {
			for _, key := range []string{terminalInfoMetaKey, terminalOutputMetaKey, terminalOutputDeltaMetaKey, terminalExitMetaKey} {
				if _, present := u.Update.ToolCallUpdate.Meta[key]; present {
					t.Fatalf("legacy metadata survived: %s", key)
				}
			}
		}
	}
	if output != "hi\n" {
		t.Fatalf("terminal output=%q", output)
	}
}

var _ acp.Client = (*recordingACPClient)(nil)

func acceptDraftPrompt(t *testing.T, fake *draftRPCFake, client *v2.ClientSideConnection, ctx context.Context, session, turn, item, text string) v2.PromptResponse {
	t.Helper()
	done := make(chan error, 1)
	var response v2.PromptResponse
	go func() {
		var err error
		response, err = client.Prompt(ctx, v2.PromptRequest{SessionId: v2.SessionId(session), Prompt: []v2.ContentBlock{v2.TextBlock(text)}})
		done <- err
	}()
	req := fake.next(t, "turn/start")
	if stringValue(req.Params["threadId"]) != session {
		t.Fatalf("prompt Thread=%s", req.Params)
	}
	fake.reply(req, map[string]any{"turn": map[string]any{"id": turn}})
	echoDraftUser(fake, session, turn, item, text)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return response
}

func TestDraftPermissionKeepsNativeOptionsAndCancelsOutstandingRequest(t *testing.T) {
	fake, client, recorder, ctx, cwd := newDraftFixture(t)
	session := openDraftSession(t, fake, client, ctx, cwd, "thread-1")
	acceptDraftPrompt(t, fake, client, ctx, "thread-1", "turn-1", "user-1", "run")
	params := map[string]any{"threadId": "thread-1", "turnId": "turn-1", "itemId": "cmd-1", "command": "touch test", "cwd": cwd}
	fake.send(map[string]any{"id": 900, "method": "item/commandExecution/requestApproval", "params": params})
	var approval v2.RequestPermissionRequest
	select {
	case approval = <-recorder.permissions:
	case <-time.After(5 * time.Second):
		t.Fatal("permission did not reach client")
	}
	if approval.SessionId != session || approval.Subject == nil || approval.Subject.ToolCall == nil || approval.Subject.ToolCall.ToolCall.ToolCallId == "" || len(approval.Options) != 4 || approval.Options[1].OptionId != "allow_for_session" {
		t.Fatalf("approval lost native subject/options: %+v", approval)
	}
	recorder.wait(t, func(u v2.UpdateSessionNotification) bool {
		s, _, id := draftState(u)
		return s == "requires_action" && id == "turn-1"
	})
	recorder.decisions <- v2.RequestPermissionResponse{Outcome: v2.RequestPermissionOutcome{Selected: &v2.RequestPermissionOutcomeSelected{Outcome: "selected", OptionId: "allow_for_session"}}}
	response := fake.next(t, "")
	var result struct {
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if string(response.ID) != "900" || result.Decision != "acceptForSession" {
		t.Fatalf("native resolve=%s id=%s", response.Result, response.ID)
	}
	fake.send(map[string]any{"id": "approval-2", "method": "item/commandExecution/requestApproval", "params": params})
	select {
	case <-recorder.permissions:
	case <-time.After(5 * time.Second):
		t.Fatal("second approval missing")
	}
	if err := client.Cancel(ctx, v2.CancelSessionNotification{SessionId: session}); err != nil {
		t.Fatal(err)
	}
	// The original native request resolves as cancellation, and cancellation
	// independently targets this exact Turn rather than another Session.
	var cancellation, interrupt draftRPCMessage
	for i := 0; i < 2; i++ {
		select {
		case msg := <-fake.messages:
			if msg.Method == "turn/interrupt" {
				interrupt = msg
				fake.reply(msg, map[string]any{})
			} else {
				cancellation = msg
			}
		case <-time.After(5 * time.Second):
			t.Fatal("cancel did not release approval/Turn")
		}
	}
	if stringValue(interrupt.Params["threadId"]) != "thread-1" || stringValue(interrupt.Params["turnId"]) != "turn-1" {
		t.Fatalf("interrupt target=%s", interrupt.Params)
	}
	_ = json.Unmarshal(cancellation.Result, &result)
	if string(cancellation.ID) != `"approval-2"` || result.Decision != "cancel" {
		t.Fatalf("cancelled native resolve=%s id=%s", cancellation.Result, cancellation.ID)
	}
	finishDraftTurn(fake, "thread-1", "turn-1", "interrupted")
	recorder.wait(t, func(u v2.UpdateSessionNotification) bool {
		s, stop, id := draftState(u)
		return s == "idle" && stop == "cancelled" && id == "turn-1"
	})
}

func TestDraftIndependentSessionsConcurrentPromptsAndTargetedCancellation(t *testing.T) {
	fake, client, recorder, ctx, cwd := newDraftFixture(t)
	first := openDraftSession(t, fake, client, ctx, cwd, "thread-1")
	second := openDraftSession(t, fake, client, ctx, cwd, "thread-2")
	done := make(chan error, 2)
	for _, id := range []v2.SessionId{first, second} {
		go func() {
			_, err := client.Prompt(ctx, v2.PromptRequest{SessionId: id, Prompt: []v2.ContentBlock{v2.TextBlock(string(id))}})
			done <- err
		}()
	}
	for range 2 {
		req := fake.next(t, "turn/start")
		id := stringValue(req.Params["threadId"])
		turn := "turn-" + id
		fake.reply(req, map[string]any{"turn": map[string]any{"id": turn}})
		echoDraftUser(fake, id, turn, "user-"+id, id)
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if err := client.Cancel(ctx, v2.CancelSessionNotification{SessionId: first}); err != nil {
		t.Fatal(err)
	}
	interrupt := fake.next(t, "turn/interrupt")
	if stringValue(interrupt.Params["threadId"]) != "thread-1" {
		t.Fatalf("cross-session interrupt=%s", interrupt.Params)
	}
	fake.reply(interrupt, map[string]any{})
	finishDraftTurn(fake, "thread-1", "turn-thread-1", "interrupted")
	recorder.wait(t, func(u v2.UpdateSessionNotification) bool {
		s, stop, id := draftState(u)
		return u.SessionId == first && s == "idle" && stop == "cancelled" && id == "turn-thread-1"
	})
	for _, update := range recorder.snapshot() {
		s, _, id := draftState(update)
		if update.SessionId == second && s == "idle" && id == "turn-thread-2" {
			t.Fatal("cancellation ended unrelated Session")
		}
	}
	finishDraftTurn(fake, "thread-2", "turn-thread-2", "failed")
	recorder.wait(t, func(u v2.UpdateSessionNotification) bool {
		s, stop, id := draftState(u)
		return u.SessionId == second && s == "idle" && stop == "error" && id == "turn-thread-2"
	})
}

func TestDraftConfigAndAuthorizedSessionListUseExistingOwners(t *testing.T) {
	fake, client, _, ctx, cwd := newDraftFixture(t)
	session := openDraftSession(t, fake, client, ctx, cwd, "thread-1")
	response, err := client.SetSessionConfigOption(ctx, v2.SetSessionConfigOptionRequest{Id: &v2.SetSessionConfigOptionId{Type: "id", SessionId: session, ConfigId: "service_tier", Value: "fast"}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, option := range response.ConfigOptions {
		if option.Select != nil && option.Select.ConfigId == "service_tier" && option.Select.CurrentValue == "fast" {
			found = true
		}
	}
	if !found {
		t.Fatalf("tier projection=%+v", response)
	}
	done := make(chan error, 1)
	go func() {
		_, err := client.Prompt(ctx, v2.PromptRequest{SessionId: session, Prompt: []v2.ContentBlock{v2.TextBlock("fast")}})
		done <- err
	}()
	req := fake.next(t, "turn/start")
	if stringValue(req.Params["serviceTier"]) != "fast" || stringValue(req.Params["model"]) != "gpt-test" || stringValue(req.Params["effort"]) != "high" {
		t.Fatalf("lost model configuration=%s", req.Params)
	}
	fake.reply(req, map[string]any{"turn": map[string]any{"id": "turn-1"}})
	echoDraftUser(fake, "thread-1", "turn-1", "user-1", "fast")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_, err = client.SetSessionConfigOption(ctx, v2.SetSessionConfigOptionRequest{Id: &v2.SetSessionConfigOptionId{Type: "id", SessionId: session, ConfigId: "service_tier", Value: "default"}})
	if err == nil {
		t.Fatal("configuration changed during active Turn")
	}
	var listed v2.ListSessionsResponse
	go func() { var err error; listed, err = client.ListSessions(ctx, v2.ListSessionsRequest{}); done <- err }()
	req = fake.next(t, "thread/list")
	foreign := t.TempDir()
	fake.reply(req, map[string]any{"data": []any{map[string]any{"id": "owned", "cwd": cwd, "name": "Visible"}, map[string]any{"id": "foreign", "cwd": foreign, "name": "Hidden"}}, "nextCursor": "page-2"})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(listed.Sessions) != 1 || listed.Sessions[0].SessionId != "owned" || listed.NextCursor == nil || *listed.NextCursor != "page-2" {
		t.Fatalf("authorized list=%+v", listed)
	}
}

func TestDraftResumeReplaysNativeMessageIdentityBehindStableBarrier(t *testing.T) {
	fake, client, recorder, ctx, cwd := newDraftFixture(t)
	history := map[string]any{"id": "thread-1", "cwd": cwd, "turns": []any{map[string]any{"id": "turn-1", "status": "completed", "items": []any{map[string]any{"id": "user-1", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": "retained"}}}, map[string]any{"id": "answer-1", "type": "agentMessage", "text": "answer"}, map[string]any{"id": "plan-1", "type": "plan", "text": "retained plan"}}}}}
	done := make(chan error, 1)
	go func() {
		_, err := client.ResumeSession(ctx, v2.ResumeSessionRequest{SessionId: "thread-1", Cwd: v2.AbsolutePath(cwd), ReplayFrom: &v2.ReplayFrom{Start: &v2.ReplayFromStartInline{Type: "start"}}})
		done <- err
	}()
	req := fake.next(t, "thread/read")
	fake.reply(req, map[string]any{"thread": map[string]any{"id": "thread-1", "cwd": cwd}})
	req = fake.next(t, "thread/resume")
	fake.reply(req, map[string]any{"thread": map[string]any{"id": "thread-1", "cwd": cwd}, "model": "gpt-test"})
	for range 2 {
		req = fake.next(t, "thread/read")
		fake.reply(req, map[string]any{"thread": history})
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	id := v2.MessageId(stableID("thread-1", "user-1", "user-message"))
	recorder.wait(t, func(u v2.UpdateSessionNotification) bool {
		return u.Update.UserMessageChunk != nil && u.Update.UserMessageChunk.MessageId == id
	})
	recorder.wait(t, func(u v2.UpdateSessionNotification) bool {
		s, stop, activity := draftState(u)
		return s == "idle" && stop == "end_turn" && activity == "turn-1"
	})
	// The resumed channel accepts new work; replay never submits an old prompt.
	acceptDraftPrompt(t, fake, client, ctx, "thread-1", "turn-2", "user-2", "new")
}

func TestDraftInsertionFenceRejectsUncorrelatedEchoAndPreservesUnknownOutcome(t *testing.T) {
	fake, client, _, ctx, cwd := newDraftFixture(t)
	session := openDraftSession(t, fake, client, ctx, cwd, "thread-1")
	done := make(chan error, 1)
	go func() {
		_, err := client.Prompt(ctx, v2.PromptRequest{SessionId: session, Prompt: []v2.ContentBlock{v2.TextBlock("same text")}})
		done <- err
	}()
	req := fake.next(t, "turn/start")
	fake.reply(req, map[string]any{"turn": map[string]any{"id": "turn-1"}})
	for _, token := range []string{"different-client", ""} {
		fake.notify("item/started", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "item": map[string]any{"id": "foreign-" + token, "clientId": token, "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": "same text"}}}})
	}
	select {
	case err := <-done:
		t.Fatalf("an uncorrelated echo settled this submission: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	finishDraftTurn(fake, "thread-1", "turn-1", "completed")
	var requestError *acp.RequestError
	if err := <-done; !errors.As(err, &requestError) || requestError.Code != -32603 {
		t.Fatalf("submitted native work lost unknown-outcome classification: %v", err)
	}
}

func TestDraftCancellationBeforeNativeTurnAcknowledgementTargetsActualTurn(t *testing.T) {
	fake, client, _, ctx, cwd := newDraftFixture(t)
	session := openDraftSession(t, fake, client, ctx, cwd, "thread-1")
	done := make(chan error, 1)
	go func() {
		_, err := client.Prompt(ctx, v2.PromptRequest{SessionId: session, Prompt: []v2.ContentBlock{v2.TextBlock("cancel before acknowledgement")}})
		done <- err
	}()
	req := fake.next(t, "turn/start")
	if err := client.Cancel(ctx, v2.CancelSessionNotification{SessionId: session}); err != nil {
		t.Fatal(err)
	}
	// The native acknowledgement supplies the only valid cancellation target.
	fake.reply(req, map[string]any{"turn": map[string]any{"id": "actual-turn"}})
	interrupt := fake.next(t, "turn/interrupt")
	if stringValue(interrupt.Params["threadId"]) != "thread-1" || stringValue(interrupt.Params["turnId"]) != "actual-turn" {
		t.Fatalf("pending cancellation lost native identity: %s", interrupt.Params)
	}
	fake.reply(interrupt, map[string]any{})
	finishDraftTurn(fake, "thread-1", "actual-turn", "interrupted")
	if err := <-done; err == nil {
		t.Fatal("cancellation fabricated an accepted-message receipt")
	}
}

func TestDraftStdioMCPOptionalFieldsUseOriginalInjectionOwner(t *testing.T) {
	fake, client, _, ctx, cwd := newDraftFixture(t)
	done := make(chan error, 1)
	go func() {
		_, err := client.NewSession(ctx, v2.NewSessionRequest{Cwd: v2.AbsolutePath(cwd), McpServers: []v2.McpServer{{Stdio: &v2.McpServerStdioInline{Type: "stdio", Name: "fixture", Command: "/usr/bin/true"}}}})
		done <- err
	}()
	req := fake.next(t, "thread/start")
	var config map[string]struct {
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
		Enabled bool              `json:"enabled"`
	}
	if err := json.Unmarshal(req.Params["config"], &config); err != nil {
		t.Fatal(err)
	}
	server, present := config["mcp_servers.fixture"]
	if !present || server.Command != "/usr/bin/true" || server.Args == nil || server.Env == nil || !server.Enabled {
		t.Fatalf("optional draft stdio injection lost owner normalization: %+v", config)
	}
	fake.reply(req, map[string]any{"thread": map[string]any{"id": "thread-1", "cwd": cwd}, "model": "gpt-test"})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDraftNativeCorrelationVersionGate(t *testing.T) {
	for _, test := range []struct {
		userAgent string
		want      bool
	}{
		{"caelis-codex-acp/0.159.0 (test)", false},
		{"caelis-codex-acp/0.160.0-alpha.1 (test)", false},
		{"caelis-codex-acp/0.160.0 (test)", true},
		{"caelis-codex-acp/0.161.0 (test)", true},
		{"caelis-codex-acp/unknown (test)", false},
		{"", false},
	} {
		if got := draftNativeCorrelationSupported(test.userAgent); got != test.want {
			t.Fatalf("native version %q = %t, want %t", test.userAgent, got, test.want)
		}
	}
	old := &Backend{nativeUserAgent: "caelis-codex-acp/0.159.0"}
	if err := old.ServeACPDraft(t.Context(), ConnectionOptions{}, acpdraft.SchemaTag, strings.NewReader(""), io.Discard); err == nil || !strings.Contains(err.Error(), "0.160.0") {
		t.Fatalf("an older native backend entered draft work: %v", err)
	}
}

func TestDraftIdleOnlyIsExplicitlyUnsupportedBeforeAnyNativeWork(t *testing.T) {
	fake, client, _, ctx, _ := newDraftFixture(t)
	_, err := client.Prompt(ctx, v2.PromptRequest{SessionId: "not-opened", Prompt: []v2.ContentBlock{v2.TextBlock("must not be submitted")}, Meta: acpdraft.IdleOnlyMeta()})
	var requestError *acp.RequestError
	if !errors.As(err, &requestError) || requestError.Code != -32602 || !strings.Contains(err.Error(), "idle-only") {
		t.Fatalf("unsupported native idle guard = %v", err)
	}
	select {
	case request := <-fake.messages:
		t.Fatalf("unsupported idle guard caused native work: %+v", request)
	case <-time.After(20 * time.Millisecond):
	}
}

func validateNativeDraftPeer(response v2.InitializeResponse) error {
	if err := response.Validate(); err != nil {
		return err
	}
	var schema string
	if json.Unmarshal(response.Meta[acpdraft.SchemaMetaKey], &schema) != nil || schema != acpdraft.SchemaTag || response.ProtocolVersion != 2 || response.Capabilities.Session == nil {
		return fmt.Errorf("invalid native experimental draft handshake")
	}
	var conditional struct {
		Version    int  `json:"version"`
		ActiveOnly bool `json:"activeOnly"`
		IdleOnly   bool `json:"idleOnly"`
	}
	if json.Unmarshal(response.Meta[acpdraft.ConditionalPromptMetaKey], &conditional) != nil || conditional.Version != 1 || !conditional.ActiveOnly || conditional.IdleOnly {
		return fmt.Errorf("native draft falsely advertised conditional admission: %s", response.Meta[acpdraft.ConditionalPromptMetaKey])
	}
	return nil
}
