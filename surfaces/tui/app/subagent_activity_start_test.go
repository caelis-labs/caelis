package tuiapp

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/control/uipreferences"
)

func TestSubagentDirectoryStartsPaneSpinnerBeforeChildOutput(t *testing.T) {
	for _, layout := range []uipreferences.Layout{uipreferences.Overlay, uipreferences.Right, uipreferences.Down} {
		t.Run(string(layout), func(t *testing.T) {
			m := NewModel(Config{NoColor: true})
			m.Update(tea.WindowSizeMsg{Width: 160, Height: 48})
			m.currentSessionID = "session-1"
			m.subagentDirectoryGeneration = 1
			startedAt := time.Now().Add(-time.Minute)
			view := addSubagentRosterTestView(m, "spawn-rhea", "rhea", "rhea[reviewer]: audit", "completed", startedAt, startedAt.Add(time.Second))
			view.block.SessionID = "turn-1"
			view.turnID = "turn-1"
			view.turnBlocks[view.turnID] = view.block
			view.historyResolved = true
			applyDirectory := func(revision uint64, descriptor taskstream.TaskDescriptor) tea.Cmd {
				t.Helper()
				_, cmd := m.Update(subagentDirectorySnapshotMsg{
					sessionID: m.currentSessionID, generation: 1,
					snapshot: taskstream.DirectorySnapshot{Revision: revision, Tasks: []taskstream.TaskDescriptor{descriptor}},
				})
				return cmd
			}
			completed := transcriptTaskDescriptor("turn-1", task.StateCompleted, false, startedAt.Add(time.Second))
			completed.ActivityID = "activity-1"
			applyDirectory(1, completed)
			m.openSubagentOutputOverlayView(view.callID, view)
			m.setSubagentLayout(layout)
			if m.workspaceLayout().split != (layout != uipreferences.Overlay) {
				t.Fatalf("layout %s did not use its intended pane geometry", layout)
			}
			frames := []string{m.View().Content}
			if m.spinnerTickScheduled || m.animationIndicatorActive() {
				t.Fatal("completed participant scheduled a spinner")
			}

			running := transcriptTaskDescriptor("turn-2", task.StateRunning, true, time.Now())
			running.ActivityID = "activity-2"
			cmd := applyDirectory(2, running)
			if cmd == nil || !m.spinnerTickScheduled || !m.animationIndicatorActive() {
				t.Fatal("new child activity did not schedule its spinner before content")
			}
			if m.turnRunning() || len(view.block.Events) != 0 || view.block.Status != "completed" {
				t.Fatal("directory activity mutated parent execution or child transcript")
			}
			frames = append(frames, m.View().Content)
			if !strings.Contains(ansi.Strip(frames[len(frames)-1]), "Waiting for response") {
				t.Fatalf("child pane omitted its pre-output activity hint:\n%s", ansi.Strip(frames[len(frames)-1]))
			}
			_, nextTick := m.Update(m.spinner.Tick())
			if nextTick == nil || !m.spinnerTickScheduled {
				t.Fatal("child spinner did not continue without assistant output")
			}

			m.taskStreamWanted["task-1"] = true
			m.taskStreamTokens["task-1"] = 7
			m.taskStreamIDsByCallID[view.callID] = "task-1"
			m.taskStreamCallIDsByID["task-1"] = view.callID
			input := subagentMailboxParentInput(t, "turn-2", time.Now(), "check the follow-up")
			input.ActivityID = running.ActivityID
			input.ParentTool.ToolCallID = view.callID
			m.Update(taskStreamBatchMsg{
				sessionID: m.currentSessionID, taskID: "task-1", token: 7,
				events: []eventstream.Envelope{input},
			})
			view.prepareVisibleRender()
			frames = append(frames, m.View().Content)
			plain := ansi.Strip(frames[len(frames)-1])
			if !strings.Contains(plain, "parent: check the follow-up") || !strings.Contains(plain, "Waiting for response") {
				t.Fatalf("accepted input arrived without its running hint:\n%s", plain)
			}
			if len(view.block.Events) != 1 || view.block.Events[0].Kind != SEAgentCommunication {
				t.Fatal("fixture emitted assistant output before checking the spinner")
			}
			updates := renderFullscreenFramesForTest(t, m.width, m.height, frames...)
			assertPhysicalFullscreenFrame(t, m.width, m.height, frames[len(frames)-1], updates)
			t.Logf("Child follow-up before first output (%s):\n%s", layout, plain)

			terminal := subagentMailboxLifecycle(t, "turn-2", time.Now(), eventstream.LifecycleStateCompleted)
			terminal.ParentTool.ToolCallID = view.callID
			m.Update(taskStreamBatchMsg{
				sessionID: m.currentSessionID, taskID: "task-1", token: 7,
				events: []eventstream.Envelope{terminal},
			})
			completed = running
			completed.Running, completed.State = false, task.StateCompleted
			applyDirectory(3, completed)
			_, nextTick = m.Update(m.spinner.Tick())
			if nextTick != nil || m.spinnerTickScheduled || m.animationIndicatorActive() {
				t.Fatal("completed follow-up retained the spinner")
			}
		})
	}
}
