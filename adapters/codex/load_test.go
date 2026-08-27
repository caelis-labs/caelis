package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk"
)

type recordingACPClient struct {
	mu      sync.Mutex
	updates []acp.SessionNotification
}

func (*recordingACPClient) RequestPermission(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{
		Selected: &acp.RequestPermissionOutcomeSelected{Outcome: "selected", OptionId: "allow_once"},
	}}, nil
}

func (c *recordingACPClient) SessionUpdate(_ context.Context, update acp.SessionNotification) error {
	c.mu.Lock()
	c.updates = append(c.updates, update)
	c.mu.Unlock()
	return nil
}

func (c *recordingACPClient) snapshot() []acp.SessionNotification {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]acp.SessionNotification(nil), c.updates...)
}

func TestLoadSessionReplaysStableHistoryBeforeReturning(t *testing.T) {
	cwd := t.TempDir()
	appInput, appOutput := io.Pipe()
	adapterInput, adapterOutput := io.Pipe()
	defer appInput.Close()
	defer appOutput.Close()
	defer adapterInput.Close()
	defer adapterOutput.Close()

	fakeErr := make(chan error, 1)
	go serveLoadFakeAppServer(adapterInput, appOutput, cwd, fakeErr)
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
			ConnectionID: "load-test",
			Workspace:    WorkspacePolicy{AllowedRoots: []string{cwd}, WritableRoots: []string{cwd}},
		}, clientToAgentReader, agentToClientWriter)
	}()

	recorder := &recordingACPClient{}
	client := acp.NewClientSideConnection(recorder, clientToAgentWriter, agentToClientReader)
	defer client.Close()
	initialize, err := client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersion(acp.WireProtocolVersion)})
	if err != nil {
		t.Fatal(err)
	}
	if !initialize.AgentCapabilities.LoadSession {
		t.Fatal("adapter did not advertise session/load")
	}
	if string(initialize.Meta["steering"]) != `{"supported":true}` {
		t.Fatalf("steering capability = %s", initialize.Meta["steering"])
	}
	response, err := client.LoadSession(ctx, acp.LoadSessionRequest{
		SessionId: acp.SessionId("thread-load-1"), Cwd: cwd, McpServers: []acp.McpServer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ConfigOptions) == 0 {
		t.Fatal("load response omitted model configuration")
	}
	updates := recorder.snapshot()
	if len(updates) != 2 || updates[0].Update.UserMessageChunk == nil || updates[1].Update.AgentMessageChunk == nil {
		t.Fatalf("replayed updates = %#v", updates)
	}
	if updates[0].Update.UserMessageChunk.Content.Text == nil || updates[0].Update.UserMessageChunk.Content.Text.Text != "remember this" {
		t.Fatalf("replayed user update = %#v", updates[0])
	}
	if updates[1].Update.AgentMessageChunk.Content.Text == nil || updates[1].Update.AgentMessageChunk.Content.Text.Text != "remembered" {
		t.Fatalf("replayed agent update = %#v", updates[1])
	}
	select {
	case err := <-fakeErr:
		if err != nil {
			t.Fatal(err)
		}
	default:
	}
	stopServe()
	select {
	case <-serveErr:
	case <-ctx.Done():
		t.Fatal(context.Cause(ctx))
	}
}

func serveLoadFakeAppServer(input io.Reader, output io.Writer, cwd string, result chan<- error) {
	reader := bufio.NewReader(input)
	historyReads := 0
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
		switch request.Method {
		case "initialize":
		case "account/read":
			response = map[string]any{"account": map[string]any{"type": "chatgpt"}, "requiresOpenaiAuth": true}
		case "thread/read":
			var includeTurns bool
			_ = json.Unmarshal(request.Params["includeTurns"], &includeTurns)
			if !includeTurns {
				response = map[string]any{"thread": map[string]any{"id": "thread-load-1", "cwd": cwd}}
			} else {
				historyReads++
				items := []map[string]any{
					{"id": "user-1", "type": "userMessage", "content": []map[string]any{{"type": "text", "text": "remember this"}}},
					{"id": "agent-1", "type": "agentMessage", "text": "remembered"},
				}
				turns := []map[string]any{{"id": "turn-1", "status": "completed", "items": items}}
				response = map[string]any{"thread": map[string]any{"id": "thread-load-1", "cwd": cwd, "turns": turns}}
			}
		case "thread/resume":
			if got := stringValue(request.Params["approvalPolicy"]); got != "on-request" {
				result <- errors.New("thread/resume omitted on-request approval policy")
				return
			}
			if got := stringValue(request.Params["sandbox"]); got != "workspace-write" {
				result <- errors.New("thread/resume omitted workspace-write sandbox")
				return
			}
			response = map[string]any{"thread": map[string]any{"id": "thread-load-1", "cwd": cwd}, "model": "gpt-test", "reasoningEffort": "high"}
		case "model/list":
			response = map[string]any{"data": []any{map[string]any{
				"id": "gpt-test", "model": "gpt-test", "displayName": "GPT Test", "defaultReasoningEffort": "high",
				"supportedReasoningEfforts": []any{map[string]any{"reasoningEffort": "high"}},
			}}, "nextCursor": nil}
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
		if historyReads == 2 {
			// Keep serving until the test closes the shared backend.
			historyReads++
		}
	}
}

func stringValue(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

type unexpectedFakeMethod struct{ method string }

func (e *unexpectedFakeMethod) Error() string {
	return "unexpected fake app-server method: " + e.method
}
