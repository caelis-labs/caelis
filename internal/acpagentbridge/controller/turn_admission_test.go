package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/subagent"
	"github.com/caelis-labs/caelis/internal/acptest/jsonrpc"
)

func TestControllerRunRejectsOverlappingTurns(t *testing.T) {
	t.Parallel()

	run := &controllerRun{parentSessionID: "session-a"}
	first := newTurnHandle(nil)
	if err := run.beginTurn(controller.TurnRequest{TurnID: "turn-1"}, first); err != nil {
		t.Fatal(err)
	}
	second := newTurnHandle(nil)
	if err := run.beginTurn(controller.TurnRequest{TurnID: "turn-2"}, second); err == nil || !strings.Contains(err.Error(), "turn in progress") {
		t.Fatalf("overlapping beginTurn() error = %v, want turn-in-progress rejection", err)
	}

	run.mu.Lock()
	turnID, active := run.turnID, run.handle
	run.mu.Unlock()
	if turnID != "turn-1" || active != first {
		t.Fatalf("active turn after rejected overlap = %q/%p, want turn-1/%p", turnID, active, first)
	}

	if _, _ = run.finishTurn(second); run.handle != first {
		t.Fatal("stale turn owner cleared the active turn")
	}
	run.finishTurn(first)
	first.finish()
	second.finish()

	third := newTurnHandle(nil)
	if err := run.beginTurn(controller.TurnRequest{TurnID: "turn-3"}, third); err != nil {
		t.Fatalf("beginTurn() after completion error = %v", err)
	}
	run.finishTurn(third)
	third.finish()
}

func TestControllerRunConcurrentAdmissionAllowsOneTurn(t *testing.T) {
	t.Parallel()

	const contenders = 16
	type admissionResult struct {
		turnID string
		handle *turnHandle
		err    error
	}

	run := &controllerRun{parentSessionID: "session-a"}
	start := make(chan struct{})
	results := make(chan admissionResult, contenders)
	var wg sync.WaitGroup
	for i := range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			turnID := fmt.Sprintf("turn-%d", i)
			handle := newTurnHandle(nil)
			<-start
			err := run.beginTurn(controller.TurnRequest{TurnID: turnID}, handle)
			results <- admissionResult{turnID: turnID, handle: handle, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var winner admissionResult
	successes := 0
	for result := range results {
		if result.err == nil {
			successes++
			winner = result
			continue
		}
		result.handle.finish()
	}
	if successes != 1 {
		t.Fatalf("successful concurrent admissions = %d, want 1", successes)
	}
	run.mu.Lock()
	turnID, active := run.turnID, run.handle
	run.mu.Unlock()
	if turnID != winner.turnID || active != winner.handle {
		t.Fatalf("winning turn state = %q/%p, want %q/%p", turnID, active, winner.turnID, winner.handle)
	}
	run.finishTurn(winner.handle)
	winner.handle.finish()
}

func TestManagerRunTurnBusyPreservesPendingContext(t *testing.T) {
	t.Parallel()

	run := &controllerRun{
		parentSessionID: "session-a",
		context:         agent.ContextTransfer{Summary: "handoff context"},
		contextPending:  true,
	}
	first := newTurnHandle(nil)
	if err := run.beginTurn(controller.TurnRequest{TurnID: "turn-1"}, first); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{controllers: map[string]*controllerRun{"session-a": run}}
	if _, err := manager.RunTurn(context.Background(), controller.TurnRequest{
		SessionRef: session.SessionRef{SessionID: "session-a"},
		TurnID:     "turn-2",
		Input:      "do not consume context",
	}); err == nil || !strings.Contains(err.Error(), "turn in progress") {
		t.Fatalf("RunTurn() overlap error = %v, want turn-in-progress rejection", err)
	}

	run.mu.Lock()
	pending := run.contextPending
	summary := run.context.Summary
	turnID, active := run.turnID, run.handle
	run.mu.Unlock()
	if !pending || summary != "handoff context" {
		t.Fatalf("pending context after rejected turn = %v/%q, want true/handoff context", pending, summary)
	}
	if turnID != "turn-1" || active != first {
		t.Fatalf("active turn after rejected manager call = %q/%p, want turn-1/%p", turnID, active, first)
	}
	run.finishTurn(first)
	first.finish()
}

func TestManagerDeactivateCancelsInFlightControllerTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	registry, err := subagent.NewRegistry([]subagent.AgentConfig{{
		Name:    "helper",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestManagerACPBlockingTurnHelperProcess", "--"},
		Env: map[string]string{
			"CAELIS_ACP_HELPER": "controller-blocking-turn",
		},
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	manager, err := NewManager(Config{Registry: registry})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	parentSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "parent", WorkspaceKey: "ws",
		},
		CWD: t.TempDir(),
	}
	t.Cleanup(func() {
		_ = manager.Deactivate(context.Background(), parentSession.SessionRef)
	})
	if _, err := manager.Activate(ctx, controller.HandoffRequest{
		Session: parentSession,
		Agent:   "helper",
		Source:  "test",
	}); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	manager.mu.RLock()
	run := manager.controllers[parentSession.SessionID]
	manager.mu.RUnlock()
	if run == nil {
		t.Fatal("active controller run is missing")
	}

	turn, err := manager.RunTurn(ctx, controller.TurnRequest{
		SessionRef: parentSession.SessionRef,
		Session:    parentSession,
		TurnID:     "turn-blocking",
		Input:      "wait for shutdown",
		Stream:     true,
	})
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	started := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		var terminalErr error
		for event, eventErr := range turn.Handle.Events() {
			if eventErr != nil {
				terminalErr = eventErr
				continue
			}
			if event != nil {
				select {
				case started <- struct{}{}:
				default:
				}
			}
		}
		done <- terminalErr
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatalf("controller turn did not become active: %v", ctx.Err())
	}

	if err := manager.Deactivate(ctx, parentSession.SessionRef); err != nil {
		t.Fatalf("Deactivate() error = %v", err)
	}
	if got := turn.Handle.Cancel().Status; got != controller.CancelStatusAlreadyCancelled {
		t.Fatalf("Cancel() after Deactivate status = %q, want %q", got, controller.CancelStatusAlreadyCancelled)
	}
	rejected := newTurnHandle(nil)
	if err := run.beginTurn(controller.TurnRequest{TurnID: "turn-after-shutdown"}, rejected); !errors.Is(err, controller.ErrNotActive) {
		t.Fatalf("beginTurn() after Deactivate error = %v, want ErrNotActive", err)
	}
	rejected.finish()
	select {
	case terminalErr := <-done:
		if terminalErr == nil {
			t.Fatal("in-flight turn ended without a cancellation or connection error")
		}
	case <-ctx.Done():
		t.Fatalf("in-flight turn did not finish after Deactivate: %v", ctx.Err())
	}
}

func TestManagerDetachCancelsInFlightParticipantPrompt(t *testing.T) {
	t.Parallel()

	key := participantKey("parent", "participant-1")
	run := &participantRun{
		id:              "participant-1",
		parentSessionID: "parent",
		binding: session.ParticipantBinding{
			ID:                   "participant-1",
			DelegationID:         "delegation-1",
			AttachmentGeneration: "generation-1",
		},
	}
	promptCtx, cancel := context.WithCancel(context.Background())
	handle := newTurnHandle(cancel)
	if err := run.beginPrompt(controller.ParticipantPromptRequest{
		TurnID: "turn-1", ParticipantID: run.id,
	}, handle); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{participants: map[participantRunKey]*participantRun{key: run}}

	if err := manager.Detach(context.Background(), controller.DetachRequest{
		SessionRef:           session.SessionRef{SessionID: "parent"},
		ParticipantID:        "participant-1",
		DelegationID:         "delegation-1",
		AttachmentGeneration: "generation-1",
	}); err != nil {
		t.Fatalf("Detach() error = %v", err)
	}
	select {
	case <-promptCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("Detach did not cancel the in-flight participant prompt")
	}
	manager.mu.RLock()
	attached := manager.participants[key]
	manager.mu.RUnlock()
	if attached != nil {
		t.Fatal("Detach left participant in the manager registry")
	}
	rejected := newTurnHandle(nil)
	if err := run.beginPrompt(controller.ParticipantPromptRequest{
		TurnID: "turn-after-detach", ParticipantID: run.id,
	}, rejected); !errors.Is(err, controller.ErrNotActive) {
		t.Fatalf("beginPrompt() after Detach error = %v, want ErrNotActive", err)
	}
	run.finishPrompt(handle)
	handle.finish()
	rejected.finish()
}

func TestManagerACPBlockingTurnHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_ACP_HELPER") != "controller-blocking-turn" {
		return
	}
	conn := jsonrpc.New(os.Stdin, os.Stdout)
	err := conn.Serve(context.Background(), func(_ context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
		switch msg.Method {
		case client.MethodInitialize:
			return client.InitializeResponse{ProtocolVersion: 1}, nil
		case client.MethodSessionNew:
			return client.NewSessionResponse{SessionID: "remote-blocking-turn"}, nil
		case client.MethodSessionPrompt:
			var req client.PromptRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			if req.SessionID != "remote-blocking-turn" || len(req.Prompt) == 0 {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected session/prompt request"}
			}
			update := jsonrpc.MustMarshalRaw(client.ContentChunk{
				SessionUpdate: client.UpdateAgentMessage,
				Content: jsonrpc.MustMarshalRaw(client.TextContent{
					Type: "text",
					Text: "turn is active",
				}),
			})
			if err := conn.Notify(client.MethodSessionUpdate, client.SessionNotification{
				SessionID: req.SessionID,
				Update:    update,
			}); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32000, Message: err.Error()}
			}
			select {}
		default:
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
		}
	}, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "helper Serve() error = %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}
