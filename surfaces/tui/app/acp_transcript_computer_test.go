package tuiapp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/projection"
	"github.com/charmbracelet/x/ansi"
)

func TestComputerUseGroupKeepsNestedDisclosureAndFailures(t *testing.T) {
	block := NewMainACPTurnBlock("computer-use")
	block.Status = "completed"
	block.UpdateToolWithMeta("apps", "desktop_world__apps", "Chrome", `{"apps":[]}`, true, false, ToolUpdateMeta{})
	block.UpdateToolWithMeta("exec", "desktop_world__exec", "fixture.app", strings.Repeat("output ", 100)+"RESULT_TAIL", true, false, ToolUpdateMeta{FullArgs: `{"app":"fixture.app","source":"return 'SCRIPT_TAIL';"}`})
	block.UpdateToolWithMeta("failed", "desktop_world__exec", "fixture.app", "native failure", true, true, ToolUpdateMeta{})
	ctx := NewModel(Config{NoColor: true, NoAnimation: true}).blockRenderContext(100)
	closed := joinRenderedPlain(block.Render(ctx))
	if !strings.Contains(closed, "Using Computer · 1 failed") || !strings.Contains(closed, "desktop_world__exec") || strings.Contains(closed, "RESULT_TAIL") {
		t.Fatalf("computer summary lost identity/status or exposed output:\n%s", closed)
	}
	if !block.toggleExplorationExpanded("apps") {
		t.Fatal("group disclosure is unavailable")
	}
	opened := block.Render(ctx)
	if block.toolPanelExpanded("exec") || strings.Contains(joinRenderedPlain(opened), "RESULT_TAIL") {
		t.Fatal("opening the group expanded its individual outputs")
	}
	clickable := false
	for _, row := range opened {
		clickable = clickable || row.ClickToken == acpToolPanelClickToken("exec")
	}
	if !clickable || !block.toggleToolPanelClick("exec") || !block.toggleToolPanelClick("exec") {
		t.Fatal("individual call lost its independent disclosure")
	}
	full := joinRenderedPlain(block.Render(ctx))
	if !strings.Contains(full, "SCRIPT_TAIL") || !strings.Contains(full, "RESULT_TAIL") || !strings.Contains(full, "desktop_world__exec") {
		t.Fatalf("expanded call lost arguments or result:\n%s", full)
	}
}

func TestComputerUseContainersKeepBoundariesAndLiveCallIdentity(t *testing.T) {
	call := func(id, name string, done bool) SubagentEvent {
		return SubagentEvent{Kind: SEToolCall, CallID: id, Name: name, Done: done}
	}
	events := []SubagentEvent{call("a", "desktop_world__apps", true), call("b", "desktop_world__exec", true), call("pending", "desktop_world__exec", false)}
	state := &explorationProjectionState{}
	state.reconcile(events, "working")
	if len(state.Containers) != 1 || !state.Containers[0].Computer || len(state.Containers[0].CallIDs) != 2 {
		t.Fatalf("pending call consumed completed prefix: %+v", state.Containers)
	}
	events[2].Done = true
	state.reconcile(events, "working")
	if len(state.Containers) != 1 || state.Containers[0].StableID != "a" || len(state.Containers[0].CallIDs) != 3 {
		t.Fatal("late completion lost the stable group identity")
	}
	for _, boundary := range []SubagentEvent{
		{Kind: SEAssistant, Text: "visible boundary"},
		call("other", "service__exec", true),
		call("wrapper", "mcp_call", true),
		{Kind: SEToolCall, CallID: "reviewed", Name: "desktop_world__exec", Done: true, ApprovalStatus: "approved"},
		{Kind: SESemanticBoundary},
	} {
		separated := []SubagentEvent{events[0], boundary, events[1]}
		state.reconcile(separated, "completed")
		if len(state.Containers) != 0 {
			t.Fatalf("group crossed a visible, approval or source boundary: %+v", boundary)
		}
	}
	if isExplorationToolEvent(events[1]) || isComputerUseToolEvent(call("x", "other__exec", true)) {
		t.Fatal("computer identity leaked into exploration or arbitrary MCP tools")
	}
	readHint := events[0]
	readHint.ToolKind = "read"
	if isExplorationToolEvent(readHint) {
		t.Fatal("a computer read hint changed its exact tool identity to exploration")
	}
}

func TestComputerUseKeepsAssistantMessageOutsideGroup(t *testing.T) {
	block := NewMainACPTurnBlock("message-boundary")
	block.AppendStreamEvent(SEReasoning, "preceding thought", narrativeTestSource())
	block.AppendStreamEvent(SEAssistant, "VISIBLE_MESSAGE", narrativeTestSource())
	block.UpdateToolWithMeta("a", "desktop_world__apps", "query", "result", true, false, ToolUpdateMeta{})
	block.AppendStreamEvent(SEReasoning, "group thought", narrativeTestSource())
	block.UpdateToolWithMeta("b", "desktop_world__exec", "app", "result", true, false, ToolUpdateMeta{})
	block.Status = "completed"
	ctx := NewModel(Config{NoColor: true, NoAnimation: true}).blockRenderContext(100)
	text := joinRenderedPlain(block.Render(ctx))
	if !strings.Contains(text, "VISIBLE_MESSAGE") || !strings.Contains(text, "Using Computer") || strings.Contains(text, "group thought") {
		t.Fatalf("group hid a message or exposed its folded reasoning:\n%s", text)
	}
	block.toggleExplorationExpanded("a")
	if !strings.Contains(joinRenderedPlain(block.Render(ctx)), "group thought") {
		t.Fatal("expansion lost the original reasoning")
	}
}

func TestComputerUseDisclosureSurvivesLateFinalUpdate(t *testing.T) {
	main := NewMainACPTurnBlock("main")
	participant := NewParticipantTurnBlock("participant", "worker")
	for _, update := range []struct {
		apply    func(string, string, string, string, bool, bool, ToolUpdateMeta)
		expand   func(string, bool)
		expanded func(string) bool
	}{
		{main.UpdateToolWithMeta, main.setToolPanelExpanded, main.toolPanelExpanded},
		{participant.UpdateToolWithMeta, participant.setToolPanelExpanded, participant.toolPanelExpanded},
	} {
		update.apply("exec", "desktop_world__exec", "app", "first result", true, false, ToolUpdateMeta{})
		if update.expanded("exec") {
			t.Fatal("computer call did not default to collapsed")
		}
		update.expand("exec", true)
		update.apply("exec", "desktop_world__exec", "app", "canonical result", true, false, ToolUpdateMeta{})
		if !update.expanded("exec") {
			t.Fatal("late final update discarded the user's disclosure choice")
		}
	}
}

func TestComputerUseDisclosureSurvivesTurnCompletion(t *testing.T) {
	type computerTurn interface {
		UpdateToolWithMeta(string, string, string, string, bool, bool, ToolUpdateMeta)
		SetStatus(string, string, string, time.Time)
		Render(BlockRenderContext) []RenderedRow
		toggleToolPanelClick(string) bool
		toolPanelExpanded(string) bool
		toggleExplorationExpanded(string) bool
	}
	for _, surface := range []struct {
		name string
		new  func() computerTurn
	}{
		{"main", func() computerTurn { return NewMainACPTurnBlock("main") }},
		{"participant", func() computerTurn { return NewParticipantTurnBlock("participant", "worker") }},
	} {
		for _, grouped := range []bool{false, true} {
			name := "single"
			if grouped {
				name = "grouped"
			}
			t.Run(surface.name+"/"+name, func(t *testing.T) {
				block := surface.new()
				ctx := NewModel(Config{NoColor: true, NoAnimation: true}).blockRenderContext(80)
				if grouped {
					block.UpdateToolWithMeta("apps", "desktop_world__apps", "query", "apps result", true, false, ToolUpdateMeta{})
				}
				block.UpdateToolWithMeta("exec", "desktop_world__exec", "fixture.app", strings.Repeat("detail ", 100)+"RESULT_TAIL", true, false, ToolUpdateMeta{FullArgs: `{"app":"fixture.app","source":"return 'SCRIPT_TAIL';"}`})
				// An unsettled call has no final-update default yet. Turn completion
				// must still collapse it when the user has made no choice.
				block.UpdateToolWithMeta("untouched", "desktop_world__exec", "fixture.app", "partial result", false, false, ToolUpdateMeta{})
				if !block.toolPanelExpanded("untouched") {
					t.Fatal("unsettled call already has a collapse default")
				}
				if grouped {
					if !strings.Contains(joinRenderedPlain(block.Render(ctx)), "Using Computer") || !block.toggleExplorationExpanded("apps") {
						t.Fatal("completed calls did not form an expandable group")
					}
				}
				for range 2 {
					if !block.toggleToolPanelClick("exec") {
						t.Fatal("individual call lost its disclosure")
					}
				}
				before := joinRenderedPlain(block.Render(ctx))
				if !block.toolPanelExpanded("exec") || !strings.Contains(before, "SCRIPT_TAIL") || !strings.Contains(before, "RESULT_TAIL") {
					t.Fatalf("precondition: original arguments and result are not visible:\n%s", before)
				}
				block.SetStatus("completed", "", "", time.Unix(200, 0))
				after := joinRenderedPlain(block.Render(ctx))
				if !block.toolPanelExpanded("exec") || !strings.Contains(after, "SCRIPT_TAIL") || !strings.Contains(after, "RESULT_TAIL") || !strings.Contains(after, "desktop_world__exec") {
					t.Fatalf("Turn completion lost the user's disclosure or original call:\n%s", after)
				}
				if block.toolPanelExpanded("untouched") {
					t.Fatal("Turn completion did not apply the unchosen default")
				}
				if !block.toggleToolPanelClick("exec") || block.toolPanelExpanded("exec") || strings.Contains(joinRenderedPlain(block.Render(ctx)), "RESULT_TAIL") {
					t.Fatal("expanded call cannot be collapsed after Turn completion")
				}
			})
		}
	}
}

// Replays the already-recorded native Agent task without invoking a model, MCP
// service or OS action. Its frames are presentation evidence, not another task.
func TestComputerUseNativeTaskReplay(t *testing.T) {
	input, artifacts := os.Getenv("CAELIS_TUI_DTW_REPLAY"), os.Getenv("CAELIS_TUI_DTW_ARTIFACTS")
	if input == "" || artifacts == "" {
		t.Skip("requires frozen native Session evidence and a task artifact directory")
	}
	var loaded session.LoadedSession
	raw, err := os.ReadFile(input)
	if err != nil || json.Unmarshal(raw, &loaded) != nil {
		t.Fatal("native Session evidence unavailable")
	}
	model := NewModel(Config{NoColor: false, NoAnimation: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 110, Height: 35})
	model = updated.(*Model)
	for _, event := range loaded.Events {
		base := projection.EnvelopeBaseFromSessionEvent(loaded.Session.SessionRef, event, projection.SessionEventTransport{TurnID: "frozen-desktop-task"})
		for _, envelope := range projection.ProjectSessionEventEnvelope(base, event) {
			model = applyACPEnvelopeForTest(t, model, envelope)
		}
	}
	for _, docBlock := range model.doc.Blocks() {
		if block, ok := docBlock.(*MainACPTurnBlock); ok {
			block.Status = "completed"
		}
	}
	if err := os.MkdirAll(artifacts, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name string) string {
		t.Helper()
		model.syncViewportContent()
		if err := os.WriteFile(filepath.Join(artifacts, name+".rows.txt"), []byte(strings.Join(model.viewportPlainLines, "\n")), 0o600); err != nil {
			t.Fatal(err)
		}
		frame := model.View().Content
		if err := os.WriteFile(filepath.Join(artifacts, name+".ansi"), []byte(frame), 0o600); err != nil {
			t.Fatal(err)
		}
		plain := ansi.Strip(frame)
		if err := os.WriteFile(filepath.Join(artifacts, name+".txt"), []byte(plain), 0o600); err != nil {
			t.Fatal(err)
		}
		return plain
	}
	write("collapsed")
	var block *MainACPTurnBlock
	var group *explorationContainerState
	for _, docBlock := range model.doc.Blocks() {
		candidate, ok := docBlock.(*MainACPTurnBlock)
		if !ok {
			continue
		}
		for i := range candidate.explorationProjection.Containers {
			if candidate.explorationProjection.Containers[i].Computer {
				block, group = candidate, &candidate.explorationProjection.Containers[i]
				break
			}
		}
	}
	if group == nil {
		for _, docBlock := range model.doc.Blocks() {
			if block, ok := docBlock.(*MainACPTurnBlock); ok {
				for _, event := range block.Events {
					t.Logf("replay block=%s kind=%d name=%s done=%v approval=%s text_bytes=%d", block.id, event.Kind, event.Name, event.Done, event.ApprovalStatus, len(event.Text))
				}
			}
		}
		t.Fatal("real DTW calls did not form a computer-use group")
	}
	block.toggleExplorationExpanded(group.StableID)
	write("group-open")
	callID := group.CallIDs[len(group.CallIDs)-1]
	block.toggleToolPanelClick(callID)
	block.toggleToolPanelClick(callID)
	write("call-open")
	t.Logf("replayed %d canonical native events; group, per-call disclosure and original parameters retained", len(loaded.Events))
}
