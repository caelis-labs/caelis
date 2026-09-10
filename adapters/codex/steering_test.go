package codex

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"
)

func TestSteeringWithoutActiveTurnRequiresPrompt(t *testing.T) {
	a := &agent{sessions: map[string]*sessionState{
		"thread-1": {threadID: "thread-1"},
	}}
	response, err := a.HandleExtensionMethod(context.Background(), sessionSteeringMethod, json.RawMessage(`{
		"sessionId":"thread-1","prompt":[{"type":"text","text":"continue"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"outcome":"promptRequired","reason":"noRunningTurn"}` {
		t.Fatalf("steering response = %s", encoded)
	}
}

func TestActiveSteeringUsesExpectedTurnAndValidatesAcknowledgement(t *testing.T) {
	for _, reply := range []string{"turn-1", "turn-2", ""} {
		t.Run("ack-"+reply, func(t *testing.T) {
			appInput, appOutput := io.Pipe()
			adapterInput, adapterOutput := io.Pipe()
			defer appInput.Close()
			defer appOutput.Close()
			defer adapterInput.Close()
			defer adapterOutput.Close()
			fake := newPromptRPCFake(adapterInput, appOutput)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			backend, err := NewBackend(ctx, appInput, adapterOutput)
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()
			a := &agent{backend: backend, sessions: map[string]*sessionState{
				"thread-1": {threadID: "thread-1", activeTurnID: "turn-1"},
			}}
			done := make(chan error, 1)
			var response any
			go func() {
				var err error
				response, err = a.HandleExtensionMethod(ctx, sessionSteeringMethod, json.RawMessage(`{"sessionId":"thread-1","prompt":[{"type":"text","text":"first report"},{"type":"text","text":"second report"}]}`))
				done <- err
			}()
			request := expectPromptRPCRequest(t, ctx, fake.requests, "turn/steer")
			if stringValue(request.Params["threadId"]) != "thread-1" || stringValue(request.Params["expectedTurnId"]) != "turn-1" {
				t.Fatalf("steering target = %s", request.Params)
			}
			if _, wrong := request.Params["turnId"]; wrong {
				t.Fatal("request used response-only turnId")
			}
			var inputs []map[string]string
			if err := json.Unmarshal(request.Params["input"], &inputs); err != nil {
				t.Fatal(err)
			}
			if len(inputs) != 2 || inputs[0]["text"] != "first report" || inputs[1]["text"] != "second report" {
				t.Fatalf("input = %#v", inputs)
			}
			fake.respond(request, map[string]any{"turnId": reply})
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			want := "injected"
			if reply != "turn-1" {
				want = "unknown"
			}
			if response.(map[string]any)["outcome"] != want {
				t.Fatalf("response = %#v, want %s", response, want)
			}
			select {
			case extra := <-fake.requests:
				t.Fatalf("steering started or retried another request: %s", extra.Method)
			default:
			}
		})
	}
}
