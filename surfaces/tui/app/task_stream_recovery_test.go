package tuiapp

import (
	"testing"
	"testing/synctest"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
)

func TestTaskStreamQuietCommandRemainsSubscribed(t *testing.T) {
	for _, cursor := range []string{"", "already-applied-cursor"} {
		t.Run(cursor, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sub := newTUIProtocolTaskSubscription()
				service := &subagentRosterTestTaskStreamService{subscription: sub}
				messages := make(chan tea.Msg, 8)
				sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }}
				defer sender.Close()
				m := NewModel(Config{NoColor: true, NoAnimation: true, TaskStreams: bindTaskStreamTestClient(t, service), ProgramSender: sender})
				m.taskStreamCallIDsByID["task"] = "command-call"
				m.startTaskStreamForwarder("session", "task", 1, cursor, false)
				receiveTUITaskStreamMessage[taskStreamOpenedMsg](t, messages)
				synctest.Wait()
				time.Sleep(2 * taskStreamRecoveryBudget)
				synctest.Wait()
				select {
				case msg := <-messages:
					t.Fatalf("quiet command emitted an unexpected message: %#v", msg)
				default:
				}
				// Completion after a long quiet interval must still reach the panel.
				status := eventstream.ToolStatusCompleted
				sub.events <- eventstream.Envelope{
					Kind: eventstream.KindSessionUpdate, SessionID: "session", TurnID: "turn", Scope: eventstream.ScopeMain,
					Update: eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "command-call", Status: &status},
				}
				batch := receiveTUITaskStreamMessage[taskStreamBatchMsg](t, messages)
				if len(batch.events) != 1 || batch.cursor == "" || batch.events[0].Update.(eventstream.ToolCallUpdate).Status == nil || *batch.events[0].Update.(eventstream.ToolCallUpdate).Status != status {
					t.Fatalf("late command completion was lost: %#v", batch)
				}
			})
		})
	}
}

func TestTaskStreamIncompleteChildSnapshotStillTimesOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sub := newTUIProtocolTaskSubscription()
		service := &subagentRosterTestTaskStreamService{subscription: sub}
		messages := make(chan tea.Msg, 8)
		sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }}
		defer sender.Close()
		m := NewModel(Config{NoColor: true, NoAnimation: true, TaskStreams: bindTaskStreamTestClient(t, service), ProgramSender: sender})
		m.taskStreamCallIDsByID["task"] = "spawn-call"
		m.startTaskStreamForwarder("session", "task", 1, "", true)
		receiveTUITaskStreamMessage[taskStreamOpenedMsg](t, messages)
		sub.deliveries <- taskstream.Delivery{Kind: taskstream.DeliveryReplaceBegin, Source: taskstream.SourceReplacement, SnapshotID: "snapshot"}
		receiveTUITaskStreamMessage[taskStreamBatchMsg](t, messages)
		synctest.Wait()
		time.Sleep(taskStreamRecoveryBudget)
		for {
			switch msg := (<-messages).(type) {
			case taskStreamClosedMsg:
				if errorcode.CodeOf(msg.err) != errorcode.Timeout || msg.cursor != "" {
					t.Fatalf("incomplete child snapshot escaped recovery deadline: %#v", msg)
				}
				return
			case taskStreamBatchMsg:
				if msg.cursor != "" || msg.phase == taskstream.DeliveryReplaceEnd {
					t.Fatalf("incomplete child snapshot was committed: %#v", msg)
				}
			default:
				t.Fatalf("unexpected recovery message: %#v", msg)
			}
		}
	})
}

func TestChildOutputReconnectsAfterRecoveryBudgetWithoutChangingExecution(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sub := newTUIProtocolTaskSubscription()
		service := &subagentRosterTestTaskStreamService{subscription: sub}
		messages := make(chan tea.Msg, 16)
		sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }}
		defer sender.Close()
		m := NewModel(Config{NoColor: true, NoAnimation: true, TaskStreams: bindTaskStreamTestClient(t, service), ProgramSender: sender})
		m.currentSessionID = "session"
		v := m.ensureSubagentOutputView("call")
		v.block.Status = "running"
		v.taskHandle = "helper"
		doc := v.document
		m.subagentOutputOverlay = &subagentOutputOverlayState{callID: "call"}
		m.taskStreamCallIDsByID["task"] = "call"
		m.taskStreamIDsByCallID["call"] = "task"
		m.taskStreamHandlesByID["task"] = "helper"
		m.taskStreamWanted["task"] = true
		m.taskStreamTokens["task"] = 7
		m.taskStreamCursors["task"] = "committed"
		m.taskStreamRecovery = map[string]time.Time{"call": time.Now().Add(-time.Second)}
		_, cmd := m.handleTaskStreamClosed(taskStreamClosedMsg{sessionID: "session", taskID: "task", token: 7, err: errorcode.New(errorcode.Unavailable, "connection lost")})
		if cmd == nil || !v.streamRecovering || v.block.Status != "running" || v.document != doc {
			t.Fatal("observation failure escaped its panel")
		}
		started := time.Now()
		retry := cmd().(taskStreamSubscribeRetryMsg)
		if time.Since(started) != taskStreamRecoveryCooldown {
			t.Fatal("reconnect did not back off")
		}
		m.handleTaskStreamSubscribeRetry(retry)
		opened := receiveTUITaskStreamMessage[taskStreamOpenedMsg](t, messages)
		m.handleTaskStreamOpened(opened)
		if m.taskStreamCursors["task"] != "committed" {
			t.Fatal("resume lost committed cursor")
		}
		m.handleTaskStreamBatch(taskStreamBatchMsg{sessionID: "session", taskID: "task", token: opened.token, phase: taskstream.DeliveryAppendPage, cursor: "next"})
		if v.streamRecovering || v.block.Status != "running" || v.document != doc {
			t.Fatal("recovery failed to retain execution and transcript")
		}
		m.closeTaskStreamSubscriptions()
		m.handleTaskStreamSubscribeRetry(retry)
		if m.taskStreamWanted["task"] {
			t.Fatal("stale cooldown restarted hidden subscription")
		}
	})
}
