package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/adapters/codex/internal/appserver"
)

func TestUserInputRequestReportsUnsupportedWithoutAnswerOrPermission(t *testing.T) {
	toAgent, fromClient := io.Pipe()
	toClient, fromAgent := io.Pipe()
	defer toAgent.Close()
	defer fromClient.Close()
	defer toClient.Close()
	defer fromAgent.Close()
	recorder := &mcpPermissionClient{outcome: acp.NewRequestPermissionOutcomeSelected("allow_once")}
	a := &agent{}
	a.connection = acp.NewAgentSideConnection(a, fromAgent, toAgent)
	defer a.connection.Close()
	client := acp.NewClientSideConnection(recorder, fromClient, toClient)
	defer client.Close()

	first := &sessionState{threadID: "first", activeTurnID: "first-turn", turnDone: make(chan turnResult, 1)}
	second := &sessionState{threadID: "second", activeTurnID: "second-turn", turnDone: make(chan turnResult, 1)}
	backend := &Backend{routes: map[string]*sessionRoute{
		"first":  {agent: a, state: first},
		"second": {agent: a, state: second},
	}}
	appInput, appOutput := io.Pipe()
	adapterInput, adapterOutput := io.Pipe()
	defer appInput.Close()
	defer appOutput.Close()
	defer adapterInput.Close()
	defer adapterOutput.Close()
	connection, err := appserver.NewConnection(appInput, adapterOutput, nil, backend.onRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	responses := make(chan []byte, 1)
	readErrors := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(adapterInput)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				readErrors <- err
				return
			}
			select {
			case responses <- line:
			case <-ctx.Done():
				return
			}
		}
	}()

	for _, tc := range []struct{ name, threadID, requestID string }{
		{"unscoped backend", "", `1`},
		{"unowned session", "unknown", `"unowned"`},
		{"first session route", "first", `"first-question"`},
		{"second session route", "second", `4`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := map[string]any{
				"id": json.RawMessage(tc.requestID), "method": "item/tool/requestUserInput",
				"params": map[string]any{
					"threadId": tc.threadID, "itemId": "question-item",
					"questions": []map[string]any{{"id": "choice", "question": "Choose a value", "options": []map[string]string{{"label": "One", "description": "First choice"}}}},
				},
			}
			encoded, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := appOutput.Write(append(encoded, '\n')); err != nil {
				t.Fatal(err)
			}
			var line []byte
			select {
			case line = <-responses:
			case err := <-readErrors:
				t.Fatal(err)
			case <-ctx.Done():
				t.Fatal(context.Cause(ctx))
			}
			var response struct {
				ID     json.RawMessage  `json:"id"`
				Result json.RawMessage  `json:"result"`
				Error  *appserver.Error `json:"error"`
			}
			if err := json.Unmarshal(line, &response); err != nil {
				t.Fatal(err)
			}
			if string(response.ID) != tc.requestID || len(response.Result) != 0 || response.Error == nil || response.Error.Code != -32601 || !strings.Contains(response.Error.Message, "structured user input is unavailable") {
				t.Fatalf("request was not explicitly rejected: %s", line)
			}
		})
	}
	if recorder.request != nil {
		t.Fatalf("user question became a permission request: %#v", recorder.request)
	}
	if first.activeTurnID != "first-turn" || second.activeTurnID != "second-turn" || len(first.turnDone) != 0 || len(second.turnDone) != 0 {
		t.Fatal("unsupported question changed another session or completed a turn")
	}
}
