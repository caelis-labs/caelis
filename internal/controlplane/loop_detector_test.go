package controlplane

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestLoopDetectorIgnoresDifferentToolArgs(t *testing.T) {
	t.Parallel()
	d := newGenerationLoopDetector(20, 3, 8)
	for i := 0; i < 6; i++ {
		path := "file-" + string(rune('a'+i)) + ".txt"
		if _, ok := d.observe(watchdogToolCall("c", "READ", map[string]any{"path": path})); ok {
			t.Fatalf("different READ paths must not trip at step %d", i)
		}
	}
}

func TestLoopDetectorIgnoresSameToolWhenContentDiffers(t *testing.T) {
	t.Parallel()
	d := newGenerationLoopDetector(20, 3, 8)
	for i := 0; i < 6; i++ {
		d.observe(&session.Event{Type: session.EventTypeAssistant, Text: "thinking step " + strings.Repeat("x", i+1)})
		if _, ok := d.observe(watchdogToolCall("c", "READ", map[string]any{"path": "same.txt"})); ok {
			t.Fatalf("same tool with different segment content must not trip at step %d", i)
		}
	}
}

func TestLoopDetectorTripsOnIdenticalContentAndTool(t *testing.T) {
	t.Parallel()
	d := newGenerationLoopDetector(20, 3, 8)
	var hit loopHit
	var ok bool
	for i := 0; i < 3; i++ {
		d.observe(&session.Event{Type: session.EventTypeAssistant, Text: "I will read the file again"})
		hit, ok = d.observe(watchdogToolCall("c", "READ", map[string]any{"path": "same.txt"}))
	}
	if !ok || hit.Reason != WatchdogReasonToolLoop || hit.Streak != 3 {
		t.Fatalf("hit = %+v ok=%v, want tool_loop streak 3", hit, ok)
	}
}

func TestLoopDetectorTripsOnPureIdenticalToolWithEmptyContent(t *testing.T) {
	t.Parallel()
	d := newGenerationLoopDetector(20, 3, 8)
	var hit loopHit
	var ok bool
	for i := 0; i < 3; i++ {
		hit, ok = d.observe(watchdogToolCall("c", "READ", map[string]any{"path": "same.txt"}))
	}
	if !ok || hit.Reason != WatchdogReasonToolLoop {
		t.Fatalf("hit = %+v ok=%v, want pure tool loop", hit, ok)
	}
}

func TestLoopDetectorFailsOpenOnEmptyToolArgs(t *testing.T) {
	t.Parallel()
	d := newGenerationLoopDetector(20, 3, 8)
	for i := 0; i < 10; i++ {
		if _, ok := d.observe(watchdogToolCall("c", "READ", nil)); ok {
			t.Fatal("empty tool args must fail open")
		}
		if _, ok := d.observe(watchdogToolCall("c", "READ", map[string]any{})); ok {
			t.Fatal("empty tool args map must fail open")
		}
	}
}

func TestLoopDetectorTextTailCycleWithoutForcedSpaces(t *testing.T) {
	t.Parallel()
	// Simulate token stream deltas with no artificial separators.
	d := newGenerationLoopDetector(4, 6, 8)
	cycle := "The model is stuck repeating this exact phrase forever."
	// Feed as small deltas that concatenate into cycle*4.
	stream := strings.Repeat(cycle, 4)
	var hit loopHit
	var ok bool
	const chunk = 7
	for i := 0; i < len(stream); i += chunk {
		end := i + chunk
		if end > len(stream) {
			end = len(stream)
		}
		hit, ok = d.observe(&session.Event{Type: session.EventTypeAssistant, Text: stream[i:end]})
		if ok {
			break
		}
	}
	if !ok || hit.Reason != WatchdogReasonTextLoop {
		t.Fatalf("hit = %+v ok=%v, want text_loop on concatenated stream deltas", hit, ok)
	}
}

func TestLoopDetectorRuneSafeTailCap(t *testing.T) {
	t.Parallel()
	d := newGenerationLoopDetector(4, 6, 8)
	// Non-periodic non-ASCII pad so the pad itself is not a cycle; then a clean cycle.
	pad := make([]rune, defaultMaxTailRunes)
	for i := range pad {
		pad[i] = rune(0x4e00 + (i % 512))
	}
	d.observe(&session.Event{Type: session.EventTypeAssistant, Text: string(pad)})
	// Cycle must be >= defaultMinCycleRunes (24).
	cycle := "重复输出这段中文用于检测循环是否会被错误截断掉。"
	var hit loopHit
	var ok bool
	for i := 0; i < 6; i++ {
		hit, ok = d.observe(&session.Event{Type: session.EventTypeAssistant, Text: cycle})
		if ok {
			break
		}
	}
	if !ok || hit.Reason != WatchdogReasonTextLoop {
		t.Fatalf("hit = %+v ok=%v, want text_loop after rune-capped non-ASCII tail", hit, ok)
	}
}

func TestLoopDetectorIncludesThoughtInContentFingerprint(t *testing.T) {
	t.Parallel()
	d := newGenerationLoopDetector(20, 3, 8)
	thought := &session.Event{
		Type: session.EventTypeAssistant,
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodSessionUpdate,
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeAgentThought),
				Content:       session.ProtocolTextContent("same thought"),
			},
		},
		Text: "same thought",
	}
	var hit loopHit
	var ok bool
	for i := 0; i < 3; i++ {
		d.observe(thought)
		hit, ok = d.observe(watchdogToolCall("c", "READ", map[string]any{"path": "x"}))
	}
	if !ok || hit.Reason != WatchdogReasonToolLoop {
		t.Fatalf("hit = %+v ok=%v, want tool loop with thought content", hit, ok)
	}
}
