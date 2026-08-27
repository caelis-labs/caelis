package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk"
)

type promptRPCRequest struct {
	ID     json.RawMessage
	Method string
	Params map[string]json.RawMessage
}

type promptRPCFake struct {
	output   io.Writer
	writeMu  sync.Mutex
	requests chan promptRPCRequest
}

func newPromptRPCFake(input io.Reader, output io.Writer) *promptRPCFake {
	return newPromptRPCFakeWithAccount(input, output, "chatgpt")
}

func newPromptRPCFakeWithAccount(input io.Reader, output io.Writer, accountType string) *promptRPCFake {
	fake := &promptRPCFake{output: output, requests: make(chan promptRPCRequest, 16)}
	go func() {
		reader := bufio.NewReader(input)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				close(fake.requests)
				return
			}
			var request struct {
				ID     json.RawMessage            `json:"id"`
				Method string                     `json:"method"`
				Params map[string]json.RawMessage `json:"params"`
			}
			if json.Unmarshal(line, &request) != nil {
				continue
			}
			if request.Method == "initialize" {
				fake.respond(promptRPCRequest{ID: request.ID}, map[string]any{})
				continue
			}
			if request.Method == "account/read" {
				fake.respond(promptRPCRequest{ID: request.ID}, map[string]any{
					"account": map[string]any{"type": accountType}, "requiresOpenaiAuth": true,
				})
				continue
			}
			fake.requests <- promptRPCRequest{ID: request.ID, Method: request.Method, Params: request.Params}
		}
	}()
	return fake
}

func (f *promptRPCFake) respond(request promptRPCRequest, result any) {
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	encoded, _ := json.Marshal(map[string]any{"id": request.ID, "result": result})
	_, _ = f.output.Write(append(encoded, '\n'))
}

func (f *promptRPCFake) respondError(request promptRPCRequest, code int, message string) {
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	encoded, _ := json.Marshal(map[string]any{
		"id": request.ID, "error": map[string]any{"code": code, "message": message},
	})
	_, _ = f.output.Write(append(encoded, '\n'))
}

func (f *promptRPCFake) notify(method string, params any) {
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	encoded, _ := json.Marshal(map[string]any{"method": method, "params": params})
	_, _ = f.output.Write(append(encoded, '\n'))
}

func TestReplacementPromptWaitsForCancelledTurnHandshake(t *testing.T) {
	appInput, appOutput := io.Pipe()
	adapterInput, adapterOutput := io.Pipe()
	defer appInput.Close()
	defer appOutput.Close()
	defer adapterInput.Close()
	defer adapterOutput.Close()
	fake := newPromptRPCFake(adapterInput, appOutput)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	backend, err := NewBackend(ctx, appInput, adapterOutput)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	a := &agent{backend: backend, sessions: make(map[string]*sessionState)}
	if _, err := a.reserveSession("thread-1", t.TempDir(), nil, routeLive); err != nil {
		t.Fatal(err)
	}
	prompt := acp.PromptRequest{
		SessionId: acp.SessionId("thread-1"),
		Prompt:    []acp.ContentBlock{acp.TextBlock("test")},
	}
	firstCtx, cancelFirst := context.WithCancel(ctx)
	firstResult := make(chan acp.PromptResponse, 1)
	firstErr := make(chan error, 1)
	go func() {
		response, err := a.Prompt(firstCtx, prompt)
		firstResult <- response
		firstErr <- err
	}()
	firstStart := expectPromptRPCRequest(t, ctx, fake.requests, "turn/start")
	if got := stringValue(firstStart.Params["summary"]); got != "auto" {
		t.Fatalf("turn/start summary = %q, want auto", got)
	}
	cancelFirst()

	secondResult := make(chan acp.PromptResponse, 1)
	secondErr := make(chan error, 1)
	go func() {
		response, err := a.Prompt(ctx, prompt)
		secondResult <- response
		secondErr <- err
	}()
	select {
	case request := <-fake.requests:
		t.Fatalf("replacement emitted %s before cancelled turn/start resolved", request.Method)
	case <-time.After(100 * time.Millisecond):
	}

	fake.respond(firstStart, map[string]any{"turn": map[string]any{"id": "turn-1"}})
	interrupt := expectPromptRPCRequest(t, ctx, fake.requests, "turn/interrupt")
	fake.respondError(interrupt, -32600, "turn is already interrupting")
	select {
	case request := <-fake.requests:
		t.Fatalf("replacement emitted %s before cancelled Turn terminal", request.Method)
	case <-time.After(100 * time.Millisecond):
	}
	fake.notify("turn/completed", map[string]any{
		"threadId": "thread-1", "turn": map[string]any{"id": "turn-1", "status": "interrupted"},
	})
	if err := <-firstErr; err != nil {
		t.Fatal(err)
	}
	if response := <-firstResult; response.StopReason != acp.StopReasonCancelled {
		t.Fatalf("cancelled Prompt stop reason = %q", response.StopReason)
	}

	secondStart := expectPromptRPCRequest(t, ctx, fake.requests, "turn/start")
	fake.respond(secondStart, map[string]any{"turn": map[string]any{"id": "turn-2"}})
	fake.notify("turn/completed", map[string]any{
		"threadId": "thread-1", "turn": map[string]any{"id": "turn-2", "status": "completed"},
	})
	if err := <-secondErr; err != nil {
		t.Fatal(err)
	}
	if response := <-secondResult; response.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("replacement Prompt stop reason = %q", response.StopReason)
	}
}

func TestInitializeCachesAccountAndTerminalOutputMode(t *testing.T) {
	appInput, appOutput := io.Pipe()
	adapterInput, adapterOutput := io.Pipe()
	defer appInput.Close()
	defer appOutput.Close()
	defer adapterInput.Close()
	defer adapterOutput.Close()
	_ = newPromptRPCFakeWithAccount(adapterInput, appOutput, "apiKey")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	backend, err := NewBackend(ctx, appInput, adapterOutput)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	a := &agent{backend: backend, sessions: make(map[string]*sessionState)}
	_, err = a.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersion(acp.WireProtocolVersion),
		ClientCapabilities: acp.ClientCapabilities{Meta: map[string]json.RawMessage{
			"terminal_output": json.RawMessage(`true`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.cachedAccountType(); got != "apiKey" {
		t.Fatalf("cached account type = %q, want apiKey", got)
	}
	if got := a.negotiatedTerminalOutputMode(); got != terminalOutputCanonical {
		t.Fatalf("terminal output mode = %v, want canonical", got)
	}
}

func TestPromptSelectsSupportedReasoningSummaryMode(t *testing.T) {
	for _, test := range []struct {
		name        string
		accountType string
		efforts     []string
		want        string
	}{
		{name: "reasoning model", accountType: "chatgpt", efforts: []string{"low", "high"}, want: "auto"},
		{name: "API key", accountType: "apiKey", efforts: []string{"low", "high"}, want: "none"},
		{name: "model with no reasoning", accountType: "chatgpt", efforts: []string{"none"}, want: "none"},
	} {
		t.Run(test.name, func(t *testing.T) {
			appInput, appOutput := io.Pipe()
			adapterInput, adapterOutput := io.Pipe()
			defer appInput.Close()
			defer appOutput.Close()
			defer adapterInput.Close()
			defer adapterOutput.Close()
			fake := newPromptRPCFake(adapterInput, appOutput)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			backend, err := NewBackend(ctx, appInput, adapterOutput)
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()

			a := &agent{backend: backend, sessions: make(map[string]*sessionState), accountType: test.accountType}
			route, err := a.reserveSession("thread-1", t.TempDir(), nil, routeLive)
			if err != nil {
				t.Fatal(err)
			}
			state := route.state
			model := codexModel{ID: "gpt-test", Model: "gpt-test"}
			for _, effort := range test.efforts {
				model.SupportedEfforts = append(model.SupportedEfforts, struct {
					ReasoningEffort string `json:"reasoningEffort"`
					Description     string `json:"description"`
				}{ReasoningEffort: effort})
			}
			state.mu.Lock()
			state.model = "gpt-test"
			state.models = []codexModel{model}
			state.mu.Unlock()

			result := make(chan error, 1)
			go func() {
				_, err := a.Prompt(ctx, acp.PromptRequest{
					SessionId: "thread-1", Prompt: []acp.ContentBlock{acp.TextBlock("test")},
				})
				result <- err
			}()
			start := expectPromptRPCRequest(t, ctx, fake.requests, "turn/start")
			if got := stringValue(start.Params["summary"]); got != test.want {
				t.Fatalf("turn/start summary = %q, want %q", got, test.want)
			}
			fake.respond(start, map[string]any{"turn": map[string]any{"id": "turn-1"}})
			fake.notify("turn/completed", map[string]any{
				"threadId": "thread-1", "turn": map[string]any{"id": "turn-1", "status": "completed"},
			})
			if err := <-result; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCancelledPromptUsesAlreadyObservedNaturalTerminal(t *testing.T) {
	done := make(chan turnResult, 1)
	done <- turnResult{stopReason: acp.StopReasonEndTurn}
	state := &sessionState{turnDone: done, activeTurnID: "turn-1"}

	response, err := (&agent{}).finishCancelledTurn(state, done)
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("natural terminal stop reason = %q", response.StopReason)
	}
	if state.turnDone != nil || state.activeTurnID != "" {
		t.Fatalf("terminal state was not cleared: %#v", state)
	}
}

func expectPromptRPCRequest(t *testing.T, ctx context.Context, requests <-chan promptRPCRequest, method string) promptRPCRequest {
	t.Helper()
	select {
	case request := <-requests:
		if request.Method != method {
			t.Fatalf("app-server method = %q, want %q", request.Method, method)
		}
		return request
	case <-ctx.Done():
		t.Fatal(context.Cause(ctx))
		return promptRPCRequest{}
	}
}
