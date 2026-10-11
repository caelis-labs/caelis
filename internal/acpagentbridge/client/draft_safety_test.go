package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpmeta"
	"github.com/caelis-labs/caelis/internal/acpdraft"
)

func TestDraftOutboundPreparedAuthRejectionAllowsExactRetry(t *testing.T) {
	agent := &draftTestAgent{}
	agent.prompt = func(context.Context, v2.PromptRequest) (v2.PromptResponse, error) {
		return v2.PromptResponse{}, acp.NewAuthRequired(nil)
	}
	client, ctx, _ := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	call, err := client.PreparePromptParts("session", []json.RawMessage{json.RawMessage(`{"type":"text","text":"input"}`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := call.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := call.Wait(ctx); err == nil || PromptOutcomeUnknown(err) {
		t.Fatalf("expected proven auth rejection: %v", err)
	}
	if err := client.Authenticate(ctx, "login"); err != nil {
		t.Fatal(err)
	}
	retry, err := client.PreparePromptParts("session", []json.RawMessage{json.RawMessage(`{"type":"text","text":"input"}`)}, nil)
	if err != nil {
		t.Fatalf("prepared-call auth recovery blocked its retry: %v", err)
	}
	retry.Abandon()
}

func TestDraftOutboundAcceptedFencesRemainUnknownAfterTimeout(t *testing.T) {
	for _, test := range []struct {
		name        string
		steer, echo bool
	}{
		{name: "steerMissingEcho", steer: true},
		{name: "promptMissingEcho"},
		{name: "promptMissingSettlement", echo: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			agent := &draftTestAgent{}
			agent.prompt = func(ctx context.Context, req v2.PromptRequest) (v2.PromptResponse, error) {
				if !test.steer {
					if err := agent.state(ctx, "session", "activity", "running", ""); err != nil {
						return v2.PromptResponse{}, err
					}
				}
				if test.echo {
					if err := agent.echo(ctx, "session", "accepted-input"); err != nil {
						return v2.PromptResponse{}, err
					}
				}
				return v2.PromptResponse{MessageId: "accepted-input", Meta: acpdraft.ActivityMeta("activity")}, nil
			}
			client, ctx, _ := newOutboundDraftFixture(t, agent, Config{})
			if _, err := client.Initialize(ctx); err != nil {
				t.Fatal(err)
			}
			if test.steer {
				if err := agent.state(ctx, "session", "activity", "running", ""); err != nil {
					t.Fatal(err)
				}
				if _, err := client.ListSessions(ctx, acp.ListSessionsRequest{}); err != nil {
					t.Fatal(err)
				}
			}
			short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
			defer cancel()
			var err error
			if test.steer {
				_, err = client.SteerPartsWithAbort(short, "session", []json.RawMessage{json.RawMessage(`{"type":"text","text":"input"}`)}, nil, nil)
			} else {
				_, err = client.PromptParts(short, "session", []json.RawMessage{json.RawMessage(`{"type":"text","text":"input"}`)}, nil)
			}
			if !errors.Is(err, context.DeadlineExceeded) || !PromptOutcomeUnknown(err) || !DispatchMayHaveCommitted(err) {
				t.Fatalf("accepted input misclassified as rejection: %v", err)
			}
			client.draft.mu.Lock()
			active := client.draft.sessionLocked("session").active
			client.draft.mu.Unlock()
			if !active {
				t.Fatal("accepted timeout falsely cleared active work")
			}
		})
	}
}

func TestDraftOutboundSettledOlderActivityCannotCompleteNewPrompt(t *testing.T) {
	agent := &draftTestAgent{}
	agent.prompt = func(ctx context.Context, req v2.PromptRequest) (v2.PromptResponse, error) {
		for _, step := range [][3]string{{"activity-A", "running", ""}, {"activity-A", "idle", "end_turn"}, {"activity-C", "running", ""}} {
			if err := agent.state(ctx, string(req.SessionId), step[0], step[1], step[2]); err != nil {
				return v2.PromptResponse{}, err
			}
		}
		if err := agent.echo(ctx, string(req.SessionId), "actual-input-C"); err != nil {
			return v2.PromptResponse{}, err
		}
		return v2.PromptResponse{MessageId: "actual-input-C", Meta: acpdraft.ActivityMeta("activity-C")}, nil
	}
	client, ctx, _ := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	for _, activity := range []string{"activity-A", "activity-B"} {
		if err := agent.state(ctx, "session", activity, "running", ""); err != nil {
			t.Fatal(err)
		}
		if err := agent.state(ctx, "session", activity, "idle", "end_turn"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.ListSessions(ctx, acp.ListSessionsRequest{}); err != nil {
		t.Fatal(err)
	}
	_, err := client.PromptParts(ctx, "session", []json.RawMessage{json.RawMessage(`{"type":"text","text":"C input"}`)}, nil)
	if err == nil || !PromptOutcomeUnknown(err) || !DispatchMayHaveCommitted(err) {
		t.Fatalf("late Activity falsely settled new work: %v", err)
	}
}

func TestDraftOutboundReceiptBindsActualActivityAfterForeignWork(t *testing.T) {
	for _, settledBeforeReceipt := range []bool{false, true} {
		t.Run(fmt.Sprintf("settledBeforeReceipt=%t", settledBeforeReceipt), func(t *testing.T) {
			agent := &draftTestAgent{}
			agent.prompt = func(ctx context.Context, req v2.PromptRequest) (v2.PromptResponse, error) {
				// This different caller was admitted and completed after Prepare
				// but before this request's atomic idle-only admission.
				for _, step := range [][3]string{{"foreign-X", "running", ""}, {"foreign-X", "idle", "end_turn"}, {"actual-C", "running", ""}} {
					if err := agent.state(ctx, "session", step[0], step[1], step[2]); err != nil {
						return v2.PromptResponse{}, err
					}
				}
				if err := agent.echo(ctx, "session", "actual-input-C"); err != nil {
					return v2.PromptResponse{}, err
				}
				if settledBeforeReceipt {
					if err := agent.state(ctx, "session", "actual-C", "idle", "end_turn"); err != nil {
						return v2.PromptResponse{}, err
					}
				}
				return v2.PromptResponse{MessageId: "actual-input-C", Meta: acpdraft.ActivityMeta("actual-C")}, nil
			}
			client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
			if _, err := client.Initialize(ctx); err != nil {
				t.Fatal(err)
			}
			call, err := client.PreparePromptParts("session", []json.RawMessage{json.RawMessage(`{"type":"text","text":"C"}`)}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := call.Dispatch(ctx); err != nil {
				t.Fatal(err)
			}
			results := make(chan error, 1)
			go func() {
				response, err := call.Wait(ctx)
				if err == nil && response.StopReason != "end_turn" {
					err = fmt.Errorf("stop=%s", response.StopReason)
				}
				results <- err
			}()
			_ = requireDraftUpdate(t, ctx, updates)
			if !settledBeforeReceipt {
				assertDraftPending(t, results)
				client.draft.mu.Lock()
				active := client.draft.sessionLocked("session").active
				client.draft.mu.Unlock()
				if !active {
					t.Fatal("foreign settlement cleared actual work")
				}
				if err := agent.state(ctx, "session", "actual-C", "idle", "end_turn"); err != nil {
					t.Fatal(err)
				}
			}
			if err := <-results; err != nil {
				t.Fatalf("actual receipt settlement: %v", err)
			}
		})
	}
}

func TestDraftOutboundReceiptCannotOmitOrReuseActivityIdentity(t *testing.T) {
	for _, activity := range []string{"", "old-activity"} {
		t.Run("receipt="+activity, func(t *testing.T) {
			agent := &draftTestAgent{}
			agent.prompt = func(ctx context.Context, req v2.PromptRequest) (v2.PromptResponse, error) {
				if err := agent.state(ctx, "session", "new-activity", "running", ""); err != nil {
					return v2.PromptResponse{}, err
				}
				if err := agent.echo(ctx, "session", "new-input"); err != nil {
					return v2.PromptResponse{}, err
				}
				return v2.PromptResponse{MessageId: "new-input", Meta: acpdraft.ActivityMeta(activity)}, nil
			}
			client, ctx, _ := newOutboundDraftFixture(t, agent, Config{})
			if _, err := client.Initialize(ctx); err != nil {
				t.Fatal(err)
			}
			if err := agent.state(ctx, "session", "old-activity", "running", ""); err != nil {
				t.Fatal(err)
			}
			if err := agent.state(ctx, "session", "old-activity", "idle", "end_turn"); err != nil {
				t.Fatal(err)
			}
			if _, err := client.ListSessions(ctx, acp.ListSessionsRequest{}); err != nil {
				t.Fatal(err)
			}
			_, err := client.PromptParts(ctx, "session", []json.RawMessage{json.RawMessage(`{"type":"text","text":"new"}`)}, nil)
			if err == nil || !PromptOutcomeUnknown(err) || !DispatchMayHaveCommitted(err) {
				t.Fatalf("invalid receipt fabricated completion or rejection: %v", err)
			}
			select {
			case <-client.conn.Done():
			default:
				t.Fatal("invalid receipt did not fail closed")
			}
		})
	}
}

func TestDraftOutboundTerminalBeforeReferenceFlushesOnceAndReleasesBytes(t *testing.T) {
	agent := &draftTestAgent{}
	client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{
		map[string]any{"sessionUpdate": "terminal_update", "terminalId": "terminal", "cwd": "/tmp"},
		map[string]any{"sessionUpdate": "terminal_output_chunk", "terminalId": "terminal", "data": "YWJj"},
		map[string]any{"sessionUpdate": "terminal_update", "terminalId": "terminal", "exitStatus": map[string]any{"exitCode": 0}},
	} {
		if err := agent.update(ctx, "session", value); err != nil {
			t.Fatal(err)
		}
		_ = requireDraftUpdate(t, ctx, updates)
	}
	if err := agent.update(ctx, "session", map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "tool", "status": "completed", "content": []any{map[string]any{"type": "terminal", "terminalId": "terminal"}}}); err != nil {
		t.Fatal(err)
	}
	_ = requireDraftUpdate(t, ctx, updates)
	flushed := requireDraftUpdate(t, ctx, updates).Update.(ToolCallUpdate)
	output, ok := acpmeta.ReadTerminalOutput(flushed.Meta)
	exit, exitOK := acpmeta.ReadTerminalExit(flushed.Meta)
	info := flushed.Meta[acpmeta.TerminalInfoKey].(map[string]any)
	if !ok || output.Data != "abc" || !output.Replacement || !exitOK || exit.ExitCode == nil || *exit.ExitCode != 0 || info["cwd"] != "/tmp" {
		t.Fatalf("lost terminal-first native replay: %+v", flushed)
	}
	client.draft.mu.Lock()
	retained := len(client.draft.sessionLocked("session").terminals["terminal"].output)
	client.draft.mu.Unlock()
	if retained != 0 {
		t.Fatal("already projected terminal bytes retained in private cache")
	}
	if err := agent.update(ctx, "session", map[string]any{"sessionUpdate": "terminal_output_chunk", "terminalId": "terminal", "data": "ZA=="}); err != nil {
		t.Fatal(err)
	}
	delta := requireDraftUpdate(t, ctx, updates).Update.(ToolCallUpdate)
	output, ok = acpmeta.ReadTerminalOutput(delta.Meta)
	if !ok || output.Data != "d" || output.Replacement {
		t.Fatalf("flushed bytes replayed as a delta: %+v", delta)
	}
}

func TestDraftOutboundChunksMaterializeOnlyAtIdentityAndEventBoundaries(t *testing.T) {
	agent := &draftTestAgent{}
	client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	chunk := func(id, text string) []ContentChunk {
		t.Helper()
		if err := agent.update(ctx, "session", map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": id, "content": map[string]any{"type": "text", "text": text}}); err != nil {
			t.Fatal(err)
		}
		if _, err := client.ListSessions(ctx, acp.ListSessionsRequest{}); err != nil {
			t.Fatal(err)
		}
		var result []ContentChunk
		for {
			select {
			case env := <-updates:
				result = append(result, env.Update.(ContentChunk))
			default:
				return result
			}
		}
	}
	first := chunk("one", "a")
	if len(first) != 1 || !first[0].Replacement {
		t.Fatalf("initial identity not registered: %+v", first)
	}
	continuous := chunk("one", "b")
	if len(continuous) != 1 || continuous[0].Replacement {
		t.Fatalf("contiguous stream became cumulative: %+v", continuous)
	}
	if err := agent.update(ctx, "session", map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "tool", "title": "tool"}); err != nil {
		t.Fatal(err)
	}
	_ = requireDraftUpdate(t, ctx, updates)
	continued := chunk("one", "c")
	if len(continued) != 3 || !continued[0].Replacement || continued[1].Replacement || continued[2].Replacement {
		t.Fatalf("same identity after tool lost snapshot: %+v", continued)
	}
	_ = chunk("two", "latest")
	repaired := chunk("one", "d")
	if len(repaired) != 4 || !repaired[0].Replacement {
		t.Fatalf("older identity revisit split into new row: %+v", repaired)
	}
}

func TestDraftOutboundIdentityBudgetClosesInsteadOfEvicting(t *testing.T) {
	agent := &draftTestAgent{}
	client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			select {
			case <-updates:
			case <-ctx.Done():
				return
			case <-client.conn.Done():
				return
			}
		}
	}()
	for index := 0; index <= draftMaxSessionRecords; index++ {
		err := agent.update(ctx, "session", map[string]any{"sessionUpdate": "agent_message", "messageId": fmt.Sprintf("message-%d", index), "content": []any{}})
		if err != nil {
			break
		}
	}
	select {
	case <-client.conn.Done():
	case <-ctx.Done():
		t.Fatal("identity exhaustion did not fail closed")
	}
	client.draft.mu.Lock()
	session := client.draft.sessionLocked("session")
	count := len(session.messages)
	_, firstRetained := session.messages["agent_message\x00message-0"]
	client.draft.mu.Unlock()
	if count != draftMaxSessionRecords || !firstRetained {
		t.Fatalf("old identities silently evicted: count=%d first=%t", count, firstRetained)
	}
}

func TestDraftOutboundLocalSessionAdmissionCannotBypassIdentityBudget(t *testing.T) {
	agent := &draftTestAgent{}
	client, ctx, _ := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	for index := range draftMaxSessions {
		call, err := client.PreparePromptParts(fmt.Sprintf("session-%d", index), []json.RawMessage{json.RawMessage(`{"type":"text","text":"input"}`)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		call.Abandon()
	}
	call, err := client.PreparePromptParts("one-too-many", []json.RawMessage{json.RawMessage(`{"type":"text","text":"input"}`)}, nil)
	if err == nil || call != nil || DispatchMayHaveCommitted(err) {
		t.Fatalf("local resource rejection dispatched work: call=%v err=%v", call, err)
	}
	client.draft.mu.Lock()
	count := len(client.draft.sessions)
	client.draft.mu.Unlock()
	if count != draftMaxSessions {
		t.Fatalf("Session identity cache grew beyond limit: %d", count)
	}
	select {
	case <-agent.requests:
		t.Fatal("resource rejection reached peer admission")
	default:
	}
}

func TestDraftOutboundActivityIdentityUsesRetainedBudget(t *testing.T) {
	d := newDraftClient(&Client{})
	d.mu.Lock()
	session, err := d.ensureSessionLocked("session")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.reserveCacheLocked(session, draftMaxSessionCacheBytes-1, session.cacheBytes); err != nil {
		t.Fatal(err)
	}
	d.mu.Unlock()
	fields := map[string]json.RawMessage{"state": json.RawMessage(`"running"`), "_meta": json.RawMessage(`{"caelis/activity_id":"actual-id"}`)}
	if err := d.observeState("session", fields); err == nil {
		t.Fatal("current Activity identity bypassed retained byte budget")
	}
}
