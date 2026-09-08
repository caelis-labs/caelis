package acpagentbridge_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	runtimeacp "github.com/caelis-labs/caelis/internal/acpagentbridge"
)

func TestRuntimeAgentPromptErrorDoesNotDetachLiveControlRun(t *testing.T) {
	turn := &testControlTurn{events: make(chan eventstream.Envelope)}
	agent, sessionID := newPromptRouterAgentForScopeTest(t, turn)
	result := make(chan error, 1)
	go func() {
		_, err := agent.Prompt(t.Context(), runtimeacp.PromptInput{
			SessionID: sessionID, Prompt: []json.RawMessage{json.RawMessage(`{"type":"text","text":"/review"}`)},
		}, &recordingPromptCallbacks{})
		result <- err
	}()
	failure := errors.New("Control projection failed while Run is active")
	turn.events <- eventstream.Envelope{Kind: eventstream.KindError, Err: failure}
	// The managed producer is independent from the ACP request observer. A
	// further successful tool result belongs to its original Run, and must not
	// arrive after the prompt has already reported failure to its client.
	select {
	case turn.events <- eventstream.Envelope{Kind: eventstream.KindNotice, Notice: "original Run wrote file"}:
	case err := <-result:
		t.Fatalf("prompt completed before execution: %v", err)
	}
	select {
	case err := <-result:
		t.Fatalf("prompt completed before terminal: %v", err)
	default:
	}
	turn.events <- eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Now())
	if err := <-result; !errors.Is(err, failure) {
		t.Fatalf("Prompt() = %v", err)
	}
	if !turn.closed {
		t.Fatal("terminal observation was not released")
	}
}

type failingSettlementPermissionCallbacks struct {
	recordingPromptCallbacks
	failure error
}

func (c *failingSettlementPermissionCallbacks) RequestPermission(context.Context, acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	return acpsdk.RequestPermissionResponse{}, c.failure
}

func TestRuntimeAgentFailedForwardingCannotStrandPermission(t *testing.T) {
	for _, permissionCallbackFailed := range []bool{false, true} {
		name := "permission_after_projection_failure"
		if permissionCallbackFailed {
			name = "permission_callback_failure"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cancelled := make(chan struct{})
			turn := &testControlTurn{events: make(chan eventstream.Envelope), onCancel: func() { close(cancelled) }}
			defer close(turn.events)
			agent, sessionID := newPromptRouterAgentForScopeTest(t, turn)
			failure := errors.New("ACP interaction unavailable")
			result := make(chan error, 1)
			go func() {
				_, err := agent.Prompt(ctx, runtimeacp.PromptInput{
					SessionID: sessionID, Prompt: []json.RawMessage{json.RawMessage(`{"type":"text","text":"/review"}`)},
				}, &failingSettlementPermissionCallbacks{failure: failure})
				result <- err
			}()
			if !permissionCallbackFailed {
				select {
				case turn.events <- eventstream.Envelope{Kind: eventstream.KindError, Err: failure}:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			select {
			case turn.events <- scopedPermissionEnvelope("child"):
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			// The producer cannot finish the pending approval without either a
			// decision or cancellation. Do not use caller cancellation to rescue
			// a lost permission interaction.
			select {
			case <-cancelled:
			case <-ctx.Done():
				t.Fatal("lost permission did not cancel the producer")
			}
			select {
			case err := <-result:
				t.Fatalf("cancel request completed prompt before terminal: %v", err)
			default:
			}
			select {
			case turn.events <- eventstream.TurnCancelled("handle-1", "run-1", "turn-1", "cancelled", time.Now()):
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case err := <-result:
				if !errors.Is(err, failure) {
					t.Fatalf("projection failure was lost: %v", err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}

func TestRuntimeAgentPromptEOFWithoutTerminalIsNotSuccess(t *testing.T) {
	events := make(chan eventstream.Envelope)
	close(events)
	agent, sessionID := newPromptRouterAgentForScopeTest(t, &testControlTurn{events: events})
	_, err := agent.Prompt(t.Context(), runtimeacp.PromptInput{
		SessionID: sessionID, Prompt: []json.RawMessage{json.RawMessage(`{"type":"text","text":"/review"}`)},
	}, &recordingPromptCallbacks{})
	if err == nil {
		t.Fatal("lost Control observation returned end_turn")
	}
}
