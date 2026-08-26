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
	go serveResumeFakeAppServer(adapterInput, appOutput, cwd, fakeErr)
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

	stopServe()
	select {
	case <-serveErr:
	case <-ctx.Done():
		t.Fatal(context.Cause(ctx))
	}
	select {
	case err := <-fakeErr:
		if err != nil {
			t.Fatal(err)
		}
	default:
	}
}

func serveResumeFakeAppServer(input io.Reader, output io.Writer, cwd string, result chan<- error) {
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
		switch request.Method {
		case "initialize":
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
	}
}
