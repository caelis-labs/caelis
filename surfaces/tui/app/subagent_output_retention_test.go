package tuiapp

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/uipreferences"
	"github.com/charmbracelet/x/ansi"
)

func TestChildDisplayReleasesOldDetailAndBoundsLongTurn(t *testing.T) {
	v := &subagentOutputView{document: NewDocument(), turnBlocks: map[string]*ParticipantTurnBlock{}}
	for n := range 100 {
		id := fmt.Sprint(n)
		b := NewParticipantTurnBlock(id, "helper")
		b.Events = []SubagentEvent{{Kind: SEUserInput, Text: "user"}, {Kind: SEAgentCommunication, Text: "mail"}, {Kind: SEReasoning, Text: "private reasoning"}, {Kind: SEToolCall, Output: "tool detail"}, {Kind: SEAssistant, Text: "answer"}}
		v.document.Append(b)
		v.turnBlocks[id], v.block = b, b
		v.retainDisplayWindow()
	}
	if v.document.Len() > 64 || len(v.turnBlocks) > 64 {
		t.Fatal("unbounded child Turns")
	}
	blocks := v.document.Blocks()
	for i, raw := range blocks {
		b := raw.(*ParticipantTurnBlock)
		if i < len(blocks)-2 && len(b.Events) != 3 {
			t.Fatal("old Turn must retain user, mail and assistant only")
		}
	}
	v.block.Events = append(v.block.Events, SubagentEvent{Kind: SEAssistant, Text: strings.Repeat("long", 1<<20)})
	v.retainDisplayWindow()
	last := v.block.Events[len(v.block.Events)-1]
	if len(last.Text) > 128<<10 || !strings.Contains(last.Text, "Earlier display omitted") {
		t.Fatal("long Turn was not bounded")
	}
}

func TestTaskStreamRecoveryDeadlineSurvivesSuccessfulSnapshots(t *testing.T) {
	m := &Model{taskStreamCallIDsByID: map[string]string{"task": "call"}, taskStreamRetries: map[string]int{}}
	start := time.Unix(100, 0)
	if !m.taskStreamRetryWithinBudget("call", start) {
		t.Fatal("no recovery budget")
	}
	deadline := m.taskStreamRecovery["call"]
	for n := range 20 {
		m.noteTaskStreamFollowing("task", start.Add(time.Duration(n)*time.Second))
		// Repeated snapshots followed by immediate loss never prove stability.
		delete(m.taskStreamFollowing, "task")
	}
	if m.taskStreamRecovery["call"] != deadline || m.taskStreamRetryWithinBudget("call", deadline) {
		t.Fatal("retry episode reset or exceeded deadline")
	}
	m.noteTaskStreamFollowing("task", deadline)
	m.noteTaskStreamFollowing("task", deadline.Add(5*time.Second))
	if _, exists := m.taskStreamRecovery["call"]; exists {
		t.Fatal("stable following did not clear episode")
	}
}

func TestTaskStreamRetryExhaustionKeepsMountedDocument(t *testing.T) {
	m := newSubagentOutputPerformanceModel(t, 100, 30, 1)
	callID := m.subagentOutputOverlay.callID
	v := m.subagentOutputViews[callID]
	doc := v.document
	m.currentSessionID = "session"
	m.taskStreamTokens["task"] = 7
	m.taskStreamCallIDsByID["task"] = callID
	m.taskStreamIDsByCallID[callID] = "task"
	m.taskStreamWanted["task"] = true
	m.taskStreamRecovery = map[string]time.Time{callID: time.Now().Add(-time.Second)}
	_, _ = m.handleTaskStreamClosed(taskStreamClosedMsg{sessionID: "session", taskID: "task", token: 7, err: errorcode.New(errorcode.Unavailable, "cache unavailable")})
	if v.document != doc || m.taskStreamWanted["task"] {
		t.Fatal("exhausted retry replaced display or remained wanted")
	}
}

func TestChildDisplayPruningKeepsLiveFinalOwnerAndRenderedFrame(t *testing.T) {
	m, _ := newPaneTestModel(t)
	v := m.subagentOutputViews["spawn-breeze"]
	v.resetForReplacement()
	m.setSubagentLayout(uipreferences.Right)
	before := m.View().Content
	for n := range 3 {
		turn := fmt.Sprint(n)
		for _, kind := range []TranscriptNarrativeKind{TranscriptNarrativeUser, TranscriptNarrativeReasoning, TranscriptNarrativeAssistant} {
			v.observeChildEvent(TranscriptEvent{Kind: TranscriptEventNarrative, TurnID: turn, MessageID: turn + string(kind), NarrativeKind: kind, Text: string(kind) + "-" + turn, Observation: true})
		}
	}
	v.observeChildEvent(TranscriptEvent{Kind: TranscriptEventNarrative, TurnID: "2", MessageID: "large", NarrativeKind: TranscriptNarrativeAssistant, Text: strings.Repeat("中", 50000), Observation: true})
	v.observeChildEvent(TranscriptEvent{Kind: TranscriptEventNarrative, TurnID: "2", MessageID: "large", NarrativeKind: TranscriptNarrativeAssistant, Text: "final-current", Final: true, Observation: true})
	large := 0
	for _, event := range v.block.Events {
		if event.narrativeTarget.identity == "message:large" {
			large++
			if event.Text != "final-current" {
				t.Fatal("final did not repair its retained stream owner")
			}
		}
	}
	if large != 1 {
		t.Fatalf("final owner count=%d", large)
	}
	old := joinRenderedPlain(v.turnBlocks["0"].Render(m.blockRenderContext(70)))
	if strings.Contains(old, "reasoning-0") || !strings.Contains(old, "user-0") || !strings.Contains(old, "assistant-0") {
		t.Fatalf("trimmed Turn rendering: %s", old)
	}
	v.touch(true)
	after := m.View().Content
	if !strings.Contains(ansi.Strip(after), "final-current") || !strings.Contains(ansi.Strip(after), "Main transcript remains visible") {
		t.Fatalf("pruned pane frame: %s", ansi.Strip(after))
	}
	updates := renderFullscreenFramesForTest(t, m.width, m.height, before, after)
	assertPhysicalFullscreenFrame(t, m.width, m.height, after, updates)
}

func TestChildDisplayCacheBoundsUnopenedPanesAndRetainsDraft(t *testing.T) {
	m, _ := newPaneTestModel(t)
	selected := m.subagentOutputOverlay.callID
	pane := m.subagentOutputViews[selected].pane
	pane.editor.SetValue("keep my draft")
	for n := range 100 {
		id := fmt.Sprint("child-", n)
		m.observeSubagentOutputEvents([]TranscriptEvent{{Kind: TranscriptEventNarrative, Scope: ACPProjectionSubagent, AnchorToolCallID: id, TurnID: id, NarrativeKind: TranscriptNarrativeAssistant, Text: "child answer", Observation: true}})
	}
	retained := 0
	for _, view := range m.subagentOutputViews {
		if view.document != nil {
			retained++
		}
	}
	if retained > childDisplayViews || m.subagentOutputViews[selected].document == nil || pane.editor.Value() != "keep my draft" {
		t.Fatalf("retained=%d selected pane or draft lost", retained)
	}
}
