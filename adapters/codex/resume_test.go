package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk"
)

func TestResumeSessionPreservesHandshakeNotificationsAndPolicy(t *testing.T) {
	cwd := t.TempDir()
	appInput, appOutput := io.Pipe()
	adapterInput, adapterOutput := io.Pipe()
	defer appInput.Close()
	defer appOutput.Close()
	defer adapterInput.Close()
	defer adapterOutput.Close()

	fakeErr := make(chan error, 1)
	unsubscribed := make(chan string, 1)
	go serveResumeFakeAppServer(adapterInput, appOutput, cwd, fakeErr, unsubscribed)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	backend, err := NewBackend(ctx, appInput, adapterOutput)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	clientToAgentReader, clientToAgentWriter := io.Pipe()
	agentToClientReader, agentToClientWriter := io.Pipe()
	defer clientToAgentReader.Close()
	defer clientToAgentWriter.Close()
	defer agentToClientReader.Close()
	defer agentToClientWriter.Close()
	serveCtx, stopServe := context.WithCancel(context.Background())
	defer stopServe()
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- backend.ServeACP(serveCtx, ConnectionOptions{
			ConnectionID: "resume-test",
			Workspace:    WorkspacePolicy{AllowedRoots: []string{cwd}, WritableRoots: []string{cwd}},
		}, clientToAgentReader, agentToClientWriter)
	}()

	recorder := &recordingACPClient{}
	client := acp.NewClientSideConnection(recorder, clientToAgentWriter, agentToClientReader)
	defer client.Close()
	if _, err := client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersion(acp.WireProtocolVersion)}); err != nil {
		t.Fatal(err)
	}
	response, err := client.ResumeSession(ctx, acp.ResumeSessionRequest{
		SessionId: acp.SessionId("thread-resume-1"), Cwd: cwd, McpServers: []acp.McpServer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ConfigOptions) == 0 {
		t.Fatal("resume response omitted model configuration")
	}
	updates := recorder.snapshot()
	if len(updates) != 1 || updates[0].Update.AgentThoughtChunk == nil || updates[0].Update.AgentThoughtChunk.Content.Text == nil {
		t.Fatalf("resume handshake updates = %#v", updates)
	}
	if got := updates[0].Update.AgentThoughtChunk.Content.Text.Text; got != "resume warning" {
		t.Fatalf("resume warning = %q", got)
	}
	prompt, err := client.Prompt(ctx, acp.PromptRequest{
		SessionId: acp.SessionId("thread-resume-1"),
		Prompt:    []acp.ContentBlock{acp.TextBlock("apply the change")},
	})
	if err != nil {
		t.Fatalf("Prompt(object-kind file change) error = %v", err)
	}
	if prompt.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("Prompt(object-kind file change) stop reason = %q, want end_turn", prompt.StopReason)
	}
	updates = recorder.snapshot()
	if len(updates) != 3 || updates[1].Update.ToolCall == nil || updates[2].Update.ToolCallUpdate == nil {
		t.Fatalf("object-kind file change prompt updates = %#v", updates)
	}

	// Production Runtime release closes the ACP client connection. The hosted
	// adapter must translate that peer close into app-server thread/unsubscribe
	// even though the shared Codex backend process remains alive.
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-serveErr:
	case <-ctx.Done():
		t.Fatal(context.Cause(ctx))
	}
	select {
	case threadID := <-unsubscribed:
		if threadID != "thread-resume-1" {
			t.Fatalf("unsubscribed Thread = %q, want thread-resume-1", threadID)
		}
	case <-ctx.Done():
		t.Fatal("closing the ACP connection did not unsubscribe the loaded Codex Thread")
	}
	select {
	case err := <-fakeErr:
		if err != nil {
			t.Fatal(err)
		}
	default:
	}
}

func serveResumeFakeAppServer(
	input io.Reader,
	output io.Writer,
	cwd string,
	result chan<- error,
	unsubscribed chan<- string,
) {
	reader := bufio.NewReader(input)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
				result <- nil
				return
			}
			result <- err
			return
		}
		var request struct {
			ID     json.RawMessage            `json:"id"`
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(line, &request); err != nil {
			result <- err
			return
		}
		var response any = map[string]any{}
		var afterResponse []map[string]any
		switch request.Method {
		case "initialize":
		case "account/read":
			response = map[string]any{"account": map[string]any{"type": "chatgpt"}, "requiresOpenaiAuth": true}
		case "thread/read":
			response = map[string]any{"thread": map[string]any{"id": "thread-resume-1", "cwd": cwd}}
		case "thread/resume":
			if got := stringValue(request.Params["approvalPolicy"]); got != "on-request" {
				result <- errors.New("thread/resume omitted on-request approval policy")
				return
			}
			if got := stringValue(request.Params["sandbox"]); got != "workspace-write" {
				result <- errors.New("thread/resume omitted workspace-write sandbox")
				return
			}
			warning, _ := json.Marshal(map[string]any{
				"method": "warning", "params": map[string]any{"threadId": "thread-resume-1", "message": "resume warning"},
			})
			if _, err := output.Write(append(warning, '\n')); err != nil {
				result <- err
				return
			}
			response = map[string]any{"thread": map[string]any{"id": "thread-resume-1", "cwd": cwd}, "model": "gpt-test", "reasoningEffort": "high"}
		case "model/list":
			response = map[string]any{"data": []any{map[string]any{
				"id": "gpt-test", "model": "gpt-test", "displayName": "GPT Test", "defaultReasoningEffort": "high",
				"supportedReasoningEfforts": []any{map[string]any{"reasoningEffort": "high"}},
			}}, "nextCursor": nil}
		case "thread/unsubscribe":
			unsubscribed <- stringValue(request.Params["threadId"])
			response = map[string]any{"status": "unsubscribed"}
		case "turn/start":
			response = map[string]any{"turn": map[string]any{"id": "turn-object-kind"}}
			fileChange := map[string]any{
				"id": "change-1", "type": "fileChange", "status": "inProgress",
				"changes": []map[string]any{{
					"path": "/workspace/main.go", "kind": map[string]any{"type": "update", "move_path": nil}, "diff": "@@ -1 +1 @@",
				}},
			}
			completedFileChange := map[string]any{
				"id": "change-1", "type": "fileChange", "status": "completed",
				"changes": fileChange["changes"],
			}
			afterResponse = []map[string]any{
				{"method": "item/started", "params": map[string]any{"threadId": "thread-resume-1", "item": fileChange}},
				{"method": "item/completed", "params": map[string]any{"threadId": "thread-resume-1", "item": completedFileChange}},
				{"method": "turn/completed", "params": map[string]any{
					"threadId": "thread-resume-1", "turn": map[string]any{"id": "turn-object-kind", "status": "completed"},
				}},
			}
		default:
			result <- &unexpectedFakeMethod{method: request.Method}
			return
		}
		encoded, err := json.Marshal(map[string]any{"id": request.ID, "result": response})
		if err == nil {
			_, err = output.Write(append(encoded, '\n'))
		}
		if err != nil {
			result <- err
			return
		}
		for _, notification := range afterResponse {
			encoded, err := json.Marshal(notification)
			if err == nil {
				_, err = output.Write(append(encoded, '\n'))
			}
			if err != nil {
				result <- err
				return
			}
		}
	}
}
