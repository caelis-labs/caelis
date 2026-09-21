package tuiapp

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
)

func TestDirectoryDiscoversChildrenWithoutControllerToolDisplay(t *testing.T) {
	for _, parentCall := range []string{"collaboration-host-call", ""} {
		t.Run(parentCall, func(t *testing.T) {
			m := newSubagentRosterTestModel()
			m.currentSessionID = "session-1"
			descriptor := taskstream.TaskDescriptor{
				SessionID: "session-1", TaskID: "child-1", Handle: "orbit-worker", AgentHandle: "orbit",
				Kind: task.KindSubagent, State: task.StateRunning, Running: true,
				ParticipantID: "participant-1", ParentTool: taskstream.ParentTool{ToolCallID: parentCall, ToolName: "StartThread"},
			}
			applySubagentDirectorySnapshotForTest(m, 1, []taskstream.TaskDescriptor{descriptor})
			key := subagentDirectoryViewKey(descriptor)
			view := m.subagentOutputViews[key]
			if view == nil || view.participantID != "participant-1" || m.subagentRosterCount() != 1 {
				t.Fatalf("directory did not discover child: %#v", m.subagentOutputViews)
			}
			if len(view.block.Events) != 0 || len(m.taskStreamWanted) != 0 {
				t.Fatal("metadata discovery eagerly loaded child content")
			}
			if !m.openSubagentWorkspace() {
				t.Fatal("directory child did not open a workspace")
			}
			m.subagentOutputOverlay.editor.SetValue("retained draft")
			m.openPaneMenu("agents")
			if rendered := ansi.Strip(m.renderPaneMenu()); !strings.Contains(rendered, "orbit-worker[orbit]") {
				t.Fatalf("directory identity not rendered: %s", rendered)
			}
			m.subagentOutputOverlay.menu = ""
			before := m.View().Content
			descriptor.Running, descriptor.State = false, task.StateCompleted
			applySubagentDirectorySnapshotForTest(m, 2, []taskstream.TaskDescriptor{descriptor})
			if m.subagentOutputViews[key] != view || m.subagentOutputOverlay.editor.Value() != "retained draft" || m.subagentRosterRunningCount() != 0 {
				t.Fatal("directory update replaced pane, draft or terminal state")
			}
			after := m.View().Content
			updates := renderFullscreenFramesForTest(t, m.width, m.height, before, after)
			assertPhysicalFullscreenFrame(t, m.width, m.height, after, updates)
			m.taskStreamHandlesByID[descriptor.TaskID] = descriptor.Handle
			envelope := eventstream.Envelope{
				Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeSubagent,
				ScopeID: "child-1", TurnID: "child-turn", ParticipantID: "participant-1",
				Update: eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, Content: eventstream.TextContent{Type: "text", Text: "child answer"}},
			}
			if parentCall != "" {
				envelope.ParentTool = &eventstream.ParentToolRelation{ToolCallID: parentCall, ToolName: "StartThread"}
			}
			m.handleACPEventEnvelope(envelope)
			plain := strings.Join(renderedPlainRows(m.subagentOutputRows(view, 96, 20)), "\n")
			if !strings.Contains(plain, "child answer") {
				t.Fatalf("directory child content was not routed: %s", plain)
			}
		})
	}
}

func TestDirectoryChildSurvivesParentHistoryReplacement(t *testing.T) {
	m := newSubagentRosterTestModel()
	m.currentSessionID = "session-1"
	descriptor := taskstream.TaskDescriptor{
		SessionID: "session-1", TaskID: "child-1", Handle: "orbit-worker", Kind: task.KindSubagent,
		State: task.StateCompleted, ParticipantID: "participant-1",
	}
	applySubagentDirectorySnapshotForTest(m, 1, []taskstream.TaskDescriptor{descriptor})
	view := m.subagentOutputViews["task:child-1"]
	if !m.openSubagentWorkspace() {
		t.Fatal("directory child not selectable")
	}
	pane := m.subagentOutputOverlay
	pane.editor.SetValue("draft")
	m.Update(sessionHistoryReplacementMsg{state: appserver.SessionState{SessionID: "session-1"}})
	m.Update(TranscriptEventsMsg{ReconnectReplay: true, Events: longHistoryTranscript(2)})
	m.Update(sessionHistoryReadyMsg{})
	if m.subagentOutputViews["task:child-1"] != view || m.subagentOutputOverlay != pane || pane.editor.Value() != "draft" {
		t.Fatal("parent history replacement lost independently discovered child")
	}
	// Switching Sessions must not transfer either identity or draft.
	m.Update(sessionViewStartMsg{generation: 2, state: appserver.SessionState{SessionID: "session-2"}})
	m.Update(sessionHistoryReadyMsg{})
	if m.subagentRosterCount() != 0 || m.subagentOutputOverlay != nil {
		t.Fatal("Session switch retained another Session's child")
	}
}

func TestDirectoryChildHistoryWithoutCreatingToolCall(t *testing.T) {
	m := newSubagentRosterTestModel()
	m.currentSessionID = "session-1"
	descriptor := taskstream.TaskDescriptor{SessionID: "session-1", TaskID: "child-1", Handle: "orbit-worker", Kind: task.KindSubagent, State: task.StateCompleted}
	applySubagentDirectorySnapshotForTest(m, 1, []taskstream.TaskDescriptor{descriptor})
	key := subagentDirectoryViewKey(descriptor)
	view := m.subagentOutputViews[key]
	m.taskStreamCallIDsByID["child-1"] = key
	m.taskStreamHandlesByID["child-1"] = descriptor.Handle
	old := view.document
	m.handleChildHistoryPage(taskStreamBatchMsg{taskID: "child-1", phase: taskstream.DeliveryReplaceBegin})
	m.handleChildHistoryPage(taskStreamBatchMsg{taskID: "child-1", phase: taskstream.DeliveryReplacePage, events: []eventstream.Envelope{{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeSubagent, ScopeID: "child-1", TurnID: "child-turn",
		Update: eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, Content: eventstream.TextContent{Type: "text", Text: "recovered child answer"}},
	}}})
	if view.document != old {
		t.Fatal("partial replay replaced the mounted child")
	}
	m.handleChildHistoryPage(taskStreamBatchMsg{taskID: "child-1", phase: taskstream.DeliveryReplaceEnd, cursor: "recovered"})
	plain := strings.Join(renderedPlainRows(m.subagentOutputRows(view, 96, 20)), "\n")
	if !strings.Contains(plain, "recovered child answer") || m.taskStreamCursors["child-1"] != "recovered" {
		t.Fatalf("unanchored child replay was lost: %s", plain)
	}
}

type discoveryDirectoryClient struct {
	taskstream.Client
	requests chan taskstream.DirectoryWatchRequest
}

func (c *discoveryDirectoryClient) WatchDirectory(ctx context.Context, req taskstream.DirectoryWatchRequest) (taskstream.DirectoryWatchResult, error) {
	c.requests <- req
	<-ctx.Done()
	return taskstream.DirectoryWatchResult{}, ctx.Err()
}

func TestDirectoryWatchStartsBeforeFirstChild(t *testing.T) {
	client := &discoveryDirectoryClient{requests: make(chan taskstream.DirectoryWatchRequest, 1)}
	sender := &ProgramSender{Send: func(tea.Msg) {}}
	defer sender.Close()
	m := NewModel(Config{TaskStreams: client, ProgramSender: sender})
	defer m.resetSubagentDirectoryWatch()
	m.handleACPEventEnvelope(eventstream.Envelope{SessionID: "session-1", Scope: eventstream.ScopeMain})
	select {
	case req := <-client.requests:
		if req.SessionID != "session-1" || m.subagentRosterCount() != 0 {
			t.Fatalf("unexpected discovery request: %#v", req)
		}
	case <-time.After(time.Second):
		t.Fatal("empty roster prevented directory observation")
	}
}

func TestBackgroundParticipantFocusWaitsForDirectory(t *testing.T) {
	for _, role := range []string{"orbit", "reviewer"} {
		t.Run(role, func(t *testing.T) {
			for _, directoryFirst := range []bool{false, true} {
				t.Run(map[bool]string{false: "receipt-first", true: "directory-first"}[directoryFirst], func(t *testing.T) {
					m := newSubagentRosterTestModel()
					m.currentSessionID = "session-1"
					descriptor := taskstream.TaskDescriptor{SessionID: "session-1", TaskID: "direct-task", Handle: "lina", AgentHandle: role, Kind: task.KindSubagent, State: task.StateRunning, Running: true, ParticipantID: "direct-agent"}
					if directoryFirst {
						applySubagentDirectorySnapshotForTest(m, 1, []taskstream.TaskDescriptor{descriptor})
					}
					m.focusParticipantTask(participantTaskFocusMsg{sessionID: "session-1", taskID: "direct-task"})
					if !directoryFirst {
						if m.subagentOutputOverlay != nil {
							t.Fatal("focus opened a speculative child")
						}
						// Child output may beat the directory snapshot to the Surface.
						m.handleACPEventEnvelope(eventstream.Envelope{
							Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeSubagent,
							ScopeID: "direct-task", TurnID: "child-turn", ParticipantID: "direct-agent",
							Update: eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, Content: eventstream.TextContent{Type: "text", Text: "early child output"}},
						})
						if m.subagentOutputViews["task:direct-task"] == nil {
							t.Fatal("early child output was routed to the main transcript")
						}
						applySubagentDirectorySnapshotForTest(m, 1, []taskstream.TaskDescriptor{descriptor})
					}
					if m.subagentOutputOverlay == nil || m.subagentOutputOverlay.callID != "task:direct-task" || !m.workspace.childFocused || m.subagentFocusTaskID != "" {
						t.Fatal("committed background Task did not open its child pane")
					}
					m.subagentOutputOverlay.editor.SetValue("draft")
					m.focusParticipantTask(participantTaskFocusMsg{sessionID: "old-session", taskID: "other-task"})
					if m.subagentOutputOverlay.editor.Value() != "draft" || m.subagentFocusTaskID != "" {
						t.Fatal("stale focus crossed Session boundary")
					}
					frame := m.View().Content
					if !strings.Contains(ansi.Strip(frame), "lina["+role+"]") {
						t.Fatalf("participant identity missing from child pane: %s", ansi.Strip(frame))
					}
					updates := renderFullscreenFramesForTest(t, m.width, m.height, frame)
					assertPhysicalFullscreenFrame(t, m.width, m.height, frame, updates)
				})
			}
		})
	}
}

func TestColdResumeKeepsBackgroundChildOutOfMain(t *testing.T) {
	for _, previous := range []string{"", "other-session"} {
		t.Run("from-"+previous, func(t *testing.T) {
			m := newSubagentRosterTestModel()
			m.currentSessionID = previous
			m.Update(sessionViewStartMsg{generation: 1, state: appserver.SessionState{SessionID: "restored"}})
			envelope := eventstream.Envelope{
				Kind: eventstream.KindSessionUpdate, SessionID: "restored", Scope: eventstream.ScopeSubagent,
				ScopeID: "child-1", TurnID: "child-turn", ParticipantID: "child-participant", Final: true,
				Update: eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, Content: eventstream.TextContent{Type: "text", Text: "background child answer sentinel"}},
			}
			m.Update(TranscriptEventsMsg{ReconnectReplay: true, Events: ProjectACPEventToTranscriptEvents(envelope)})
			m.Update(sessionHistoryReadyMsg{})
			applySubagentDirectorySnapshotForTest(m, 1, []taskstream.TaskDescriptor{{
				SessionID: "restored", TaskID: "child-1", Handle: "orbit-worker", Kind: task.KindSubagent, State: task.StateCompleted,
			}})
			frame := ansi.Strip(m.View().Content)
			if strings.Contains(frame, "background child answer sentinel") {
				t.Fatalf("child history leaked into parent transcript on cold resume:\n%s", frame)
			}

			if !m.openSubagentWorkspace() {
				t.Fatal("replayed child did not appear in sidebar")
			}
			childFrame := m.View().Content
			if !strings.Contains(ansi.Strip(childFrame), "background child answer sentinel") {
				t.Fatalf("child pane lost its answer:\n%s", childFrame)
			}
			updates := renderFullscreenFramesForTest(t, m.width, m.height, childFrame)
			assertPhysicalFullscreenFrame(t, m.width, m.height, childFrame, updates)
		})
	}
}
