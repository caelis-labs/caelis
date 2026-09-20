package tuiapp

import (
	"fmt"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestReplayedSpawnPromptMouseTargetsAndPhysicalFrames(t *testing.T) {
	for _, width := range []int{35, 80, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m := newWelcomeTestModel(t, width, 40, Config{NoColor: true, NoAnimation: true})
			m.Update(sessionViewStartMsg{generation: 1, state: appserver.SessionState{SessionID: "session-1"}})
			prompt := strings.Repeat("检查 payload ", 12) + "\nspawn-deep-marker\n" + strings.Repeat("结果 tail ", 12)
			envelope := eventstream.Envelope{
				Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
				Update: eventstream.ToolCall{
					SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "spawn-1", Title: "StartThread", Kind: eventstream.ToolKindExecute, Status: eventstream.ToolStatusCompleted,
					RawInput: map[string]any{"agent": "breeze", "prompt": prompt}, RawOutput: map[string]any{"handle": "ziva", "state": "completed"}, Meta: acpToolNameMeta("StartThread"),
				},
			}
			m.Update(TranscriptEventsMsg{Events: ProjectACPEventToTranscriptEvents(envelope), ReconnectReplay: true})
			m.Update(sessionHistoryReadyMsg{})
			frames := []string{m.View().Content}
			header := func() int {
				t.Helper()
				for n, line := range m.viewportPlainLines {
					if strings.Contains(line, "• Spawned ") {
						return n
					}
				}
				t.Fatal("replayed Spawn row missing")
				return -1
			}
			line := header()
			bounds := m.viewportClickBounds[line]
			if !bounds.valid() || bounds.start != displayColumns("• Spawned ") {
				t.Fatalf("Spawn label bounds=%+v", bounds)
			}
			clickViewportColumn(t, m, line, bounds.start)
			if m.subagentOutputOverlay == nil || m.subagentOutputOverlay.callID != "spawn-1" {
				t.Fatal("Spawn label mouse click did not open the child")
			}
			m.closeSubagentOutputOverlay()
			m.syncViewportContent()
			line = header()
			clickViewportColumn(t, m, line, m.viewportClickBounds[line].end+1)
			if m.subagentOutputOverlay != nil || !strings.Contains(strings.Join(m.viewportPlainLines, "\n"), "spawn-deep-marker") {
				t.Fatal("replayed Spawn body did not expand the full prompt")
			}
			frames = append(frames, m.View().Content)
			continuation := header() + 1
			if m.viewportClickTokens[continuation] != agentMessageFoldClickToken("spawn:spawn-1") || m.viewportClickBounds[continuation].valid() {
				t.Fatal("Spawn continuation has a navigation target")
			}
			clickViewportColumn(t, m, continuation, 4)
			if m.subagentOutputOverlay != nil || strings.Contains(strings.Join(m.viewportPlainLines, "\n"), "spawn-deep-marker") {
				t.Fatal("Spawn continuation did not collapse the prompt")
			}
			frames = append(frames, m.View().Content)
			updates := renderFullscreenFramesForTest(t, m.width, m.height, frames...)
			for n, frame := range frames {
				assertPhysicalFullscreenFrame(t, m.width, m.height, frame, updates[:n+1])
			}
		})
	}
}
