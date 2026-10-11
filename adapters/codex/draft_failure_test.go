package codex

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	outbound "github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acpdraft"
)

func assertDraftNoIdle(t *testing.T, recorder *draftRecorder, activity string) {
	t.Helper()
	for _, update := range recorder.snapshot() {
		state, _, id := draftState(update)
		if state == "idle" && id == activity {
			t.Fatalf("projection failure fabricated native completion for %s: %+v", activity, update)
		}
	}
}

func failDraftProjection(fake *draftRPCFake, thread, turn string) {
	// An unsupported native content variant is a genuine projection failure,
	// independent of the signed exit-code sentinel repaired in draftTerminalUpdates.
	fake.notify("item/started", map[string]any{"threadId": thread, "turnId": turn, "item": map[string]any{"id": "unprojectable", "type": "userMessage", "content": []any{map[string]any{"type": "future-native-content"}}}})
}

func waitDraftConnectionLoss(t *testing.T, ctx context.Context, client *v2.ClientSideConnection) {
	t.Helper()
	select {
	case <-client.Done():
		if client.Err() == nil || !outbound.PromptOutcomeUnknown(client.Err()) {
			t.Fatalf("lost projection claimed a settled outcome: %v", client.Err())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("route failure left the ACP consumer waiting")
	case <-ctx.Done():
		t.Fatal(context.Cause(ctx))
	}
}

func replyDraftInterrupt(t *testing.T, fake *draftRPCFake, thread, turn string) {
	t.Helper()
	interrupt := fake.next(t, "turn/interrupt")
	if stringValue(interrupt.Params["threadId"]) != thread || stringValue(interrupt.Params["turnId"]) != turn {
		t.Fatalf("interrupt lost the actual native owner: %+v", interrupt)
	}
	fake.reply(interrupt, map[string]any{})
}

func TestDraftProjectionFailureBeforeAndAfterReceiptIsObservable(t *testing.T) {
	for _, stage := range []string{"before-native-ack", "before-user-echo", "after-receipt"} {
		t.Run(stage, func(t *testing.T) {
			fake, client, recorder, ctx, cwd := newDraftFixture(t)
			openDraftSession(t, fake, client, ctx, cwd, "thread-1")
			// A different ACP transport shares the native backend, never this failure.
			other, otherRecorder := attachDraftTestClient(t, ctx, fake.backend, cwd)
			openDraftSession(t, fake, other, ctx, cwd, "thread-2")
			type result struct {
				receipt v2.PromptResponse
				err     error
			}
			done := make(chan result, 1)
			go func() {
				r, err := client.Prompt(ctx, v2.PromptRequest{SessionId: "thread-1", Prompt: []v2.ContentBlock{v2.TextBlock("accepted work")}})
				done <- result{r, err}
			}()
			start := fake.next(t, "turn/start")
			fake.notify("turn/started", map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": "actual-turn-1", "status": "inProgress"}})
			recorder.wait(t, func(update v2.UpdateSessionNotification) bool {
				state, _, id := draftState(update)
				return state == "running" && id == "actual-turn-1"
			})
			if stage != "before-native-ack" {
				fake.reply(start, map[string]any{"turn": map[string]any{"id": "actual-turn-1"}})
			}
			if stage == "after-receipt" {
				echoDraftUser(fake, "thread-1", "actual-turn-1", "actual-user-1", "accepted work")
				r := <-done
				if r.err != nil || r.receipt.MessageId != v2.MessageId(stableID("thread-1", "actual-user-1", "user-message")) || acpdraft.ActivityID(r.receipt.Meta) != "actual-turn-1" {
					t.Fatalf("actual insertion receipt = %+v, %v", r.receipt, r.err)
				}
			}
			failDraftProjection(fake, "thread-1", "actual-turn-1")
			waitDraftConnectionLoss(t, ctx, client)
			if stage != "after-receipt" {
				select {
				case r := <-done:
					if r.err == nil || r.receipt.MessageId != "" || !outbound.PromptOutcomeUnknown(r.err) {
						t.Fatalf("unobserved insertion became accepted or retry-safe: %+v, %v", r.receipt, r.err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("failed projection left native acknowledgement RPC pending")
				}
			}
			replyDraftInterrupt(t, fake, "thread-1", "actual-turn-1")
			assertDraftNoIdle(t, recorder, "actual-turn-1")
			if stage == "before-native-ack" {
				// A late native acknowledgement cannot re-open the failed projection.
				fake.reply(start, map[string]any{"turn": map[string]any{"id": "actual-turn-1"}})
			}
			select {
			case <-fake.backend.Done():
				t.Fatalf("one transport failure closed the shared native backend: %v", fake.backend.Err())
			case <-other.Done():
				t.Fatalf("one transport failure closed another ACP connection: %v", other.Err())
			default:
			}
			acceptDraftPrompt(t, fake, other, ctx, "thread-2", "actual-turn-2", "actual-user-2", "independent")
			finishDraftTurn(fake, "thread-2", "actual-turn-2", "completed")
			otherRecorder.wait(t, func(update v2.UpdateSessionNotification) bool {
				state, stop, id := draftState(update)
				return state == "idle" && stop == "end_turn" && id == "actual-turn-2"
			})
		})
	}
}

func TestDraftProjectionFailureCancelsOriginalApproval(t *testing.T) {
	fake, client, recorder, ctx, cwd := newDraftFixture(t)
	openDraftSession(t, fake, client, ctx, cwd, "thread-1")
	acceptDraftPrompt(t, fake, client, ctx, "thread-1", "turn-1", "user-1", "requires approval")
	fake.send(map[string]any{"id": "original-approval", "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": "thread-1", "turnId": "turn-1", "itemId": "command-1", "command": "printf fixture", "cwd": cwd}})
	select {
	case request := <-recorder.permissions:
		if len(request.Options) != 4 || request.Options[1].OptionId != "allow_for_session" || request.Subject == nil || request.Subject.ToolCall == nil {
			t.Fatalf("native approval options/subject changed: %+v", request)
		}
	case <-ctx.Done():
		t.Fatal(context.Cause(ctx))
	}
	failDraftProjection(fake, "thread-1", "turn-1")
	waitDraftConnectionLoss(t, ctx, client)
	interrupted, resolved := false, false
	for !interrupted || !resolved {
		select {
		case message := <-fake.messages:
			switch {
			case message.Method == "turn/interrupt":
				if stringValue(message.Params["threadId"]) != "thread-1" || stringValue(message.Params["turnId"]) != "turn-1" {
					t.Fatalf("interrupt changed native owner: %+v", message)
				}
				interrupted = true
				fake.reply(message, map[string]any{})
			case stringValue(message.ID) == "original-approval":
				var outcome struct {
					Decision string `json:"decision"`
				}
				if json.Unmarshal(message.Result, &outcome) != nil || outcome.Decision != "cancel" {
					t.Fatalf("original approval was lost or granted: %+v", message)
				}
				resolved = true
			default:
				t.Fatalf("unexpected native retry or replacement: %+v", message)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("route failure lost original native cancellation: interrupt=%v approval=%v", interrupted, resolved)
		}
	}
	assertDraftNoIdle(t, recorder, "turn-1")
}

func TestDraftExplicitSessionCloseKeepsSiblingSession(t *testing.T) {
	fake, client, recorder, ctx, cwd := newDraftFixture(t)
	openDraftSession(t, fake, client, ctx, cwd, "thread-1")
	openDraftSession(t, fake, client, ctx, cwd, "thread-2")
	if _, err := client.CloseSession(ctx, v2.CloseSessionRequest{SessionId: "thread-1"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.Done():
		t.Fatalf("explicit Session close became a connection failure: %v", client.Err())
	default:
	}
	acceptDraftPrompt(t, fake, client, ctx, "thread-2", "turn-2", "user-2", "sibling remains open")
	finishDraftTurn(fake, "thread-2", "turn-2", "completed")
	recorder.wait(t, func(update v2.UpdateSessionNotification) bool {
		state, _, id := draftState(update)
		return state == "idle" && id == "turn-2"
	})
}

func TestDraftProjectionFailureOnSharedTransportLeavesSiblingOutcomeUnknown(t *testing.T) {
	for _, stage := range []string{"sibling-before-ack", "sibling-receipted"} {
		t.Run(stage, func(t *testing.T) {
			fake, client, recorder, ctx, cwd := newDraftFixture(t)
			openDraftSession(t, fake, client, ctx, cwd, "thread-1")
			openDraftSession(t, fake, client, ctx, cwd, "thread-2")
			acceptDraftPrompt(t, fake, client, ctx, "thread-1", "turn-1", "user-1", "first native activity")
			siblingRoute := fake.backend.route("thread-2")
			done := make(chan error, 1)
			go func() {
				_, err := client.Prompt(ctx, v2.PromptRequest{SessionId: "thread-2", Prompt: []v2.ContentBlock{v2.TextBlock("sibling native activity")}})
				done <- err
			}()
			siblingStart := fake.next(t, "turn/start")
			if stage == "sibling-receipted" {
				fake.reply(siblingStart, map[string]any{"turn": map[string]any{"id": "turn-2"}})
				echoDraftUser(fake, "thread-2", "turn-2", "user-2", "sibling native activity")
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
			failDraftProjection(fake, "thread-1", "turn-1")
			waitDraftConnectionLoss(t, ctx, client)
			if stage == "sibling-before-ack" {
				select {
				case err := <-done:
					if err == nil || !outbound.PromptOutcomeUnknown(err) {
						t.Fatalf("sibling unknown became retry-safe: %v", err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("shared transport did not release sibling consumer")
				}
			}
			want := map[string]string{"thread-1": "turn-1"}
			if stage == "sibling-receipted" {
				want["thread-2"] = "turn-2"
			}
			for len(want) > 0 {
				message := fake.next(t, "turn/interrupt")
				thread := stringValue(message.Params["threadId"])
				if expected := want[thread]; expected == "" || expected != stringValue(message.Params["turnId"]) {
					t.Fatalf("cleanup fabricated or repeated a native target: %+v", message)
				}
				delete(want, thread)
				fake.reply(message, map[string]any{})
			}
			deadline := time.Now().Add(2 * time.Second)
			for !siblingRoute.state.promptMu.TryLock() {
				if time.Now().After(deadline) {
					t.Fatal("closed transport retained the sibling native acknowledgement request")
				}
				time.Sleep(time.Millisecond)
			}
			siblingRoute.state.promptMu.Unlock()
			assertDraftNoIdle(t, recorder, "turn-1")
			assertDraftNoIdle(t, recorder, "turn-2")
		})
	}
}
