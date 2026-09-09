package acpagentbridge_test

import (
	"context"
	"net"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	runtimeacp "github.com/caelis-labs/caelis/internal/acpagentbridge"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

// The SDK owns dispatch and session/cancel context semantics. This adapter
// deliberately supplies no custom request cancellation or response machinery.
type settlementWireAgent struct {
	acpsdk.Agent
	product *runtimeacp.RuntimeAgent
}

func (a settlementWireAgent) Initialize(ctx context.Context, req acpsdk.InitializeRequest) (acpsdk.InitializeResponse, error) {
	return a.product.Initialize(ctx, req)
}
func (a settlementWireAgent) NewSession(ctx context.Context, req acpsdk.NewSessionRequest) (acpsdk.NewSessionResponse, error) {
	return a.product.NewSession(ctx, req)
}
func (a settlementWireAgent) Cancel(ctx context.Context, req acpsdk.CancelNotification) error {
	return a.product.Cancel(ctx, req)
}
func (a settlementWireAgent) Prompt(ctx context.Context, req acpsdk.PromptRequest) (acpsdk.PromptResponse, error) {
	input, err := runtimeacp.PromptInputFromACP(req)
	if err != nil {
		return acpsdk.PromptResponse{}, err
	}
	return a.product.Prompt(ctx, input, &recordingPromptCallbacks{})
}

func TestSDKSessionCancelResponseWaitsForControlTerminal(t *testing.T) {
	testSDKCancellationSettlement(t, false)
}

func TestSDKSessionCompletionWinsCancellationRace(t *testing.T) {
	testSDKCancellationSettlement(t, true)
}

func testSDKCancellationSettlement(t *testing.T, completedBeforeCancellation bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cancelled := make(chan struct{})
	turn := &testControlTurn{events: make(chan eventstream.Envelope), onCancel: func() { close(cancelled) }}
	product, _ := newPromptRouterAgentForScopeTest(t, turn)
	server, socket := net.Pipe()
	peer := acpsdk.NewAgentSideConnection(settlementWireAgent{product: product}, server, server)
	defer peer.Close()
	remote, err := client.NewStreamClient(socket, socket, client.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close(context.Background())
	if _, err := remote.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	opened, err := remote.NewSession(ctx, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	type completion struct {
		response client.PromptResponse
		err      error
	}
	completed := make(chan completion, 1)
	go func() {
		response, err := remote.Prompt(ctx, opened.SessionID, "/review", nil)
		completed <- completion{response, err}
	}()
	// Receiving an actual Control event proves the prompt was admitted before
	// the standard cancel notification is sent.
	select {
	case turn.events <- eventstream.Envelope{Kind: eventstream.KindNotice, Notice: "running"}:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := remote.Cancel(ctx, opened.SessionID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case result := <-completed:
		t.Fatalf("cancel notification completed prompt before Control: %#v", result)
	default:
	}
	terminal := eventstream.TurnCancelled("handle-1", "run-1", "turn-1", "cancelled", time.Now())
	wantStopReason := string(acpsdk.StopReasonCancelled)
	if completedBeforeCancellation {
		terminal = eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Now())
		wantStopReason = string(acpsdk.StopReasonEndTurn)
	}
	turn.events <- terminal
	select {
	case result := <-completed:
		if result.err != nil || result.response.StopReason != wantStopReason {
			t.Fatalf("cancel response = %#v", result)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
