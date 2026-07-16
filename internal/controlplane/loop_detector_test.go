package controlplane

import (
	"strconv"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestLoopDetectorIgnoresDifferentToolArgs(t *testing.T) {
	t.Parallel()
	d := newGenerationLoopDetector(20, 3, 8)
	for i := 0; i < 6; i++ {
		path := "file-" + string(rune('a'+i)) + ".txt"
		if _, ok := d.observe(watchdogToolCall("c-"+strconv.Itoa(i), "READ", map[string]any{"path": path})); ok {
			t.Fatalf("different READ paths must not trip at step %d", i)
		}
	}
}

func TestLoopDetectorIgnoresSameToolWhenContentDiffers(t *testing.T) {
	t.Parallel()
	d := newGenerationLoopDetector(20, 3, 8)
	for i := 0; i < 6; i++ {
		d.observe(&session.Event{Type: session.EventTypeAssistant, Text: "thinking step " + strings.Repeat("x", i+1)})
		if _, ok := d.observe(watchdogToolCall("c-"+strconv.Itoa(i), "READ", map[string]any{"path": "same.txt"})); ok {
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
		hit, ok = d.observe(watchdogToolCall("c-"+strconv.Itoa(i), "READ", map[string]any{"path": "same.txt"}))
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
		hit, ok = d.observe(watchdogToolCall("c-"+strconv.Itoa(i), "READ", map[string]any{"path": "same.txt"}))
	}
	if !ok || hit.Reason != WatchdogReasonToolLoop {
		t.Fatalf("hit = %+v ok=%v, want pure tool loop", hit, ok)
	}
}

func TestLoopDetectorIgnoresRepeatedTaskWaitAndResetsToolEvidence(t *testing.T) {
	t.Parallel()

	d := newGenerationLoopDetector(20, 3, 8)
	for i := 0; i < 2; i++ {
		if _, ok := d.observe(watchdogToolCall("read-before-"+strconv.Itoa(i), "READ", map[string]any{"path": "same.txt"})); ok {
			t.Fatalf("READ triggered before threshold at step %d", i)
		}
	}
	for i := 0; i < 6; i++ {
		if _, ok := d.observe(watchdogToolCall("wait-"+strconv.Itoa(i), "TASK", map[string]any{
			"action": "wait", "task_id": "8d7a77b2b254",
		})); ok {
			t.Fatalf("repeated Task wait triggered a loop at step %d", i)
		}
	}
	if _, ok := d.observe(watchdogToolCall("read-after", "READ", map[string]any{"path": "same.txt"})); ok {
		t.Fatal("tool-loop evidence crossed a Task wait boundary")
	}
}

func TestLoopDetectorIgnoresProtocolOnlyTaskWait(t *testing.T) {
	t.Parallel()

	d := newGenerationLoopDetector(20, 2, 8)
	for i := 0; i < 4; i++ {
		event := &session.Event{Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodSessionUpdate,
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeToolCall),
				ToolCallID:    "wait-" + strconv.Itoa(i),
				Title:         "TASK wait 8d7a77b2b254",
				RawInput:      map[string]any{"action": "wait", "task_id": "8d7a77b2b254"},
			},
		}}
		if _, ok := d.observe(event); ok {
			t.Fatalf("protocol-only Task wait triggered a loop at step %d", i)
		}
	}
}

func TestLoopDetectorStillCountsRepeatedTaskWrite(t *testing.T) {
	t.Parallel()

	d := newGenerationLoopDetector(20, 3, 8)
	var hit loopHit
	var ok bool
	for i := 0; i < 3; i++ {
		hit, ok = d.observe(watchdogToolCall("write-"+strconv.Itoa(i), "TASK", map[string]any{
			"action": "write", "task_id": "command-task", "input": "continue",
		}))
	}
	if !ok || hit.Reason != WatchdogReasonToolLoop {
		t.Fatalf("hit = %+v ok=%v, want repeated Task write tool loop", hit, ok)
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
		hit, ok = d.observe(watchdogToolCall("c-"+strconv.Itoa(i), "READ", map[string]any{"path": "x"}))
	}
	if !ok || hit.Reason != WatchdogReasonToolLoop {
		t.Fatalf("hit = %+v ok=%v, want tool loop with thought content", hit, ok)
	}
}

func TestLoopDetectorUsesProtocolOnlyToolCall(t *testing.T) {
	t.Parallel()
	d := newGenerationLoopDetector(50, 2, 4)
	protocolCall := func(id string) *session.Event {
		return &session.Event{Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodSessionUpdate,
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeToolCall), ToolCallID: id,
				Title: "READ file", RawInput: map[string]any{"path": "same.txt"},
			},
		}}
	}
	if _, ok := d.observe(protocolCall("one")); ok {
		t.Fatal("first protocol-only tool call triggered loop")
	}
	hit, ok := d.observe(protocolCall("two"))
	if !ok || hit.Reason != WatchdogReasonToolLoop {
		t.Fatalf("hit = %+v ok=%v, want protocol-only tool loop", hit, ok)
	}
}

func TestLoopDetectorTextTailDoesNotCrossToolBoundary(t *testing.T) {
	t.Parallel()
	d := newGenerationLoopDetector(2, 50, 4)
	cycle := "repeated reasoning segment"
	if _, ok := d.observe(&session.Event{Type: session.EventTypeAssistant, Text: cycle}); ok {
		t.Fatal("first text segment triggered loop")
	}
	d.observe(watchdogToolCall("call", "READ", map[string]any{"path": "a"}))
	if _, ok := d.observe(&session.Event{Type: session.EventTypeAssistant, Text: cycle}); ok {
		t.Fatal("text loop crossed a tool boundary")
	}
}

func TestLoopDetectorCountsOneACPToolCallIDOnce(t *testing.T) {
	t.Parallel()
	d := newGenerationLoopDetector(50, 2, 4)
	call := func(id string, args map[string]any) *session.Event {
		return &session.Event{Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodSessionUpdate,
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeToolCall), ToolCallID: id,
				Title: "READ file", RawInput: args,
			},
		}}
	}
	if _, ok := d.observe(call("same-call", map[string]any{"path": "x"})); ok {
		t.Fatal("first ACP tool update triggered loop")
	}
	if _, ok := d.observe(call("same-call", map[string]any{"path": "x"})); ok {
		t.Fatal("second update for the same ACP ToolCallID counted as a new step")
	}
	if hit, ok := d.observe(call("next-call", map[string]any{"path": "x"})); !ok || hit.Streak != 2 {
		t.Fatalf("next distinct call hit = %+v ok=%v, want second tool step", hit, ok)
	}
}

func TestLoopDetectorCountsACPToolCallAfterArgsBecomeComparable(t *testing.T) {
	t.Parallel()
	d := newGenerationLoopDetector(50, 2, 4)
	call := func(id string, args map[string]any) *session.Event {
		return &session.Event{Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodSessionUpdate,
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeToolCall), ToolCallID: id,
				Title: "READ file", RawInput: args,
			},
		}}
	}
	if _, ok := d.observe(call("same-call", nil)); ok {
		t.Fatal("argument-free pending update triggered loop")
	}
	if _, ok := d.observe(call("same-call", map[string]any{"path": "x"})); ok {
		t.Fatal("first comparable update triggered loop")
	}
	if hit, ok := d.observe(call("next-call", map[string]any{"path": "x"})); !ok || hit.Streak != 2 {
		t.Fatalf("next distinct call hit = %+v ok=%v, want second comparable step", hit, ok)
	}
}

func TestLoopDetectorDoesNotRecountNonAdjacentACPToolCallID(t *testing.T) {
	t.Parallel()
	d := newGenerationLoopDetector(50, 3, 4)
	for _, id := range []string{"call-a", "call-b", "call-a"} {
		if _, ok := d.observe(watchdogToolCall(id, "READ", map[string]any{"path": "x"})); ok {
			t.Fatalf("tool call %q triggered before three distinct calls", id)
		}
	}
	if hit, ok := d.observe(watchdogToolCall("call-c", "READ", map[string]any{"path": "x"})); !ok || hit.Streak != 3 {
		t.Fatalf("third distinct call hit = %+v ok=%v", hit, ok)
	}
}

func TestLoopDetectorUncomparableToolIsEvidenceBarrier(t *testing.T) {
	t.Parallel()
	d := newGenerationLoopDetector(50, 2, 4)
	if _, ok := d.observe(watchdogToolCall("one", "READ", map[string]any{"path": "x"})); ok {
		t.Fatal("first comparable tool triggered loop")
	}
	if _, ok := d.observe(watchdogToolCall("barrier", "READ", nil)); ok {
		t.Fatal("empty tool args triggered loop")
	}
	if _, ok := d.observe(watchdogToolCall("two", "READ", map[string]any{"path": "x"})); ok {
		t.Fatal("tool loop evidence crossed an uncomparable tool boundary")
	}
}
