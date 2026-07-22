package tuiapp

import "testing"

func TestApplyToolEventUpdateUsesPatchMergeSemantics(t *testing.T) {
	t.Parallel()

	events, changed, _ := applyToolEventUpdate(nil, toolEventUpdate{
		CallID: "call-1",
		Name:   "Shell",
		Args:   "Shell",
	}, map[string]int{})
	if !changed || len(events) != 1 {
		t.Fatalf("initial update events = %#v changed=%v, want one event", events, changed)
	}

	events, changed, _ = applyToolEventUpdate(events, toolEventUpdate{
		CallID: "call-1",
		Name:   "execute",
		Args:   "pwd",
		Meta:   ToolUpdateMeta{ToolKind: "execute"},
	}, map[string]int{})
	if !changed || len(events) != 1 {
		t.Fatalf("patch update events = %#v changed=%v, want one event", events, changed)
	}
	if event := events[0]; event.Name != "execute" || event.ToolKind != "execute" || event.Args != "pwd" {
		t.Fatalf("patch update event = %#v, want present fields to replace prior values", event)
	}

	events, changed, _ = applyToolEventUpdate(events, toolEventUpdate{
		CallID: "call-1",
		Output: "ok\n",
		Final:  true,
	}, map[string]int{})
	if !changed || len(events) != 1 {
		t.Fatalf("final update events = %#v changed=%v, want one event", events, changed)
	}
	if event := events[0]; !event.Done || event.Name != "execute" || event.ToolKind != "execute" || event.Args != "pwd" || event.Output != "ok\n" {
		t.Fatalf("final update event = %#v, want omitted fields preserved", event)
	}
}

func TestApplyToolEventUpdatePreservesRepeatedExactTerminalDeltas(t *testing.T) {
	t.Parallel()

	meta := ToolUpdateMeta{ToolKind: "execute", Terminal: true, OutputTerminal: true}
	events, _, _ := applyToolEventUpdate(nil, toolEventUpdate{
		CallID: "command-1", Name: "RUN_COMMAND", Output: "tick\n", Meta: meta,
	}, map[string]int{})
	events, _, _ = applyToolEventUpdate(events, toolEventUpdate{
		CallID: "command-1", Name: "RUN_COMMAND", Output: "tick\n", Meta: meta,
	}, map[string]int{})

	if len(events) != 1 || events[0].Output != "tick\ntick\n" {
		t.Fatalf("terminal events = %#v, want both exact repeated deltas", events)
	}
}

func TestCompletedRunCommandDuplicateEmptyFinalPreservesStreamedOutput(t *testing.T) {
	t.Parallel()

	index := map[string]int{}
	meta := ToolUpdateMeta{ToolKind: "execute", Terminal: true, OutputTerminal: true}
	events, _, _ := applyToolEventUpdate(nil, toolEventUpdate{
		CallID: "command-1", Name: "RUN_COMMAND", Output: "done\n", Meta: meta,
	}, index)
	final := toolEventUpdate{
		CallID: "command-1", Name: "RUN_COMMAND", Final: true,
		Meta: ToolUpdateMeta{ToolKind: "execute", Terminal: true},
	}
	events, _, _ = applyToolEventUpdate(events, final, index)
	events, _, _ = applyToolEventUpdate(events, final, index)

	if len(events) != 1 || !events[0].Done || events[0].Output != "done\n" {
		t.Fatalf("terminal events = %#v, want repeated empty finals to preserve streamed output", events)
	}
}

func TestSpawnFinalOutputDoesNotTruncateLiveChildNarrative(t *testing.T) {
	t.Parallel()

	events, changed, _ := applyToolEventUpdate(nil, toolEventUpdate{
		CallID: "spawn-1",
		Name:   "SPAWN",
		Output: "当前目录下共有 12 个文件",
		Meta:   ToolUpdateMeta{ToolKind: "execute", MessageID: "child-message-1", OutputNarrative: true},
	}, map[string]int{})
	if !changed || len(events) != 1 {
		t.Fatalf("live update events = %#v changed=%v, want one event", events, changed)
	}

	events, changed, _ = applyToolEventUpdate(events, toolEventUpdate{
		CallID: "spawn-1",
		Name:   "SPAWN",
		Output: "当前",
		Final:  true,
		Meta:   ToolUpdateMeta{ToolKind: "execute"},
	}, map[string]int{})
	if !changed || len(events) != 1 {
		t.Fatalf("final update events = %#v changed=%v, want one merged event", events, changed)
	}
	if event := events[0]; !event.Done || event.Output != "当前目录下共有 12 个文件" || event.OutputMessageID != "child-message-1" {
		t.Fatalf("final spawn event = %#v, want complete live child narrative preserved", event)
	}
}

func TestCompletedSpawnDuplicateFinalDoesNotTruncateChildNarrative(t *testing.T) {
	t.Parallel()

	events, _, _ := applyToolEventUpdate(nil, toolEventUpdate{
		CallID: "spawn-1",
		Name:   "SPAWN",
		Output: "当前目录下共有 12 个文件。",
		Meta:   ToolUpdateMeta{ToolKind: "execute", MessageID: "child-message-1", OutputNarrative: true},
	}, map[string]int{})
	events, changed, _ := applyToolEventUpdate(events, toolEventUpdate{
		CallID: "spawn-1",
		Name:   "SPAWN",
		Output: "。",
		Final:  true,
		Meta:   ToolUpdateMeta{ToolKind: "execute"},
	}, map[string]int{})
	if !changed || len(events) != 1 || !events[0].Done {
		t.Fatalf("first final events = %#v changed=%v, want one completed Spawn", events, changed)
	}

	events, changed, _ = applyToolEventUpdate(events, toolEventUpdate{
		CallID: "spawn-1",
		Name:   "SPAWN",
		Output: "。",
		Final:  true,
		Meta:   ToolUpdateMeta{ToolKind: "execute"},
	}, map[string]int{})
	if !changed || len(events) != 1 {
		t.Fatalf("duplicate final events = %#v changed=%v, want one completed Spawn", events, changed)
	}
	if event := events[0]; !event.Done || event.Output != "当前目录下共有 12 个文件。" || event.OutputMessageID != "child-message-1" {
		t.Fatalf("duplicate final Spawn = %#v, want complete child narrative preserved", event)
	}
}

func TestCompletedTaskDuplicateFinalDoesNotTruncateChildNarrative(t *testing.T) {
	t.Parallel()

	events, _, _ := applyToolEventUpdate(nil, toolEventUpdate{
		CallID: "task-1",
		Name:   "TASK",
		Output: "当前目录下共有 12 个文件。",
		Final:  true,
		Meta:   ToolUpdateMeta{MessageID: "child-message-1", OutputNarrative: true},
	}, map[string]int{})
	events, changed, _ := applyToolEventUpdate(events, toolEventUpdate{
		CallID: "task-1",
		Name:   "TASK",
		Output: "。",
		Final:  true,
	}, map[string]int{})

	if !changed || len(events) != 1 {
		t.Fatalf("duplicate final events = %#v changed=%v, want one completed Task", events, changed)
	}
	if event := events[0]; !event.Done || event.Output != "当前目录下共有 12 个文件。" || event.OutputMessageID != "child-message-1" {
		t.Fatalf("duplicate final Task = %#v, want complete child narrative preserved", event)
	}
}

func TestSubagentFailureFinalReplacesLiveChildNarrative(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		toolName  string
		status    string
		err       bool
		final     string
		expectErr bool
	}{
		{name: "spawn failed", toolName: "SPAWN", status: "failed", err: true, final: "subagent failed: boom", expectErr: true},
		{name: "task cancelled", toolName: "TASK", status: "cancelled", final: "subagent cancelled"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			events, _, _ := applyToolEventUpdate(nil, toolEventUpdate{
				CallID: "call-1",
				Name:   test.toolName,
				Output: "正在检查…",
				Meta:   ToolUpdateMeta{OutputNarrative: true},
			}, map[string]int{})
			events, changed, _ := applyToolEventUpdate(events, toolEventUpdate{
				CallID: "call-1",
				Name:   test.toolName,
				Output: test.final,
				Final:  true,
				Err:    test.err,
				Meta:   ToolUpdateMeta{ToolStatus: test.status},
			}, map[string]int{})

			if !changed || len(events) != 1 {
				t.Fatalf("failure final events = %#v changed=%v, want one completed %s", events, changed, test.toolName)
			}
			if event := events[0]; !event.Done || event.Err != test.expectErr || event.Output != test.final {
				t.Fatalf("failure final %s = %#v, want authoritative output %q", test.toolName, event, test.final)
			}
		})
	}
}

func TestLinkedSubagentFailureFinalReplacesLiveChildNarrative(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		ownerName   string
		ownerAction string
		status      string
		err         bool
		final       string
		expectErr   bool
	}{
		{name: "linked spawn failed", ownerName: "SPAWN", status: "failed", err: true, final: "subagent failed: boom", expectErr: true},
		{name: "linked task write cancelled", ownerName: "TASK", ownerAction: "write", status: "cancelled", final: "subagent cancelled"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			events, _, _ := applyToolEventUpdate(nil, toolEventUpdate{
				CallID: "owner-call",
				Name:   test.ownerName,
				Output: "正在检查…",
				Meta: ToolUpdateMeta{
					TaskHandle:      "task-1",
					TaskAction:      test.ownerAction,
					OutputNarrative: true,
				},
			}, map[string]int{})
			events, changed, _ := applyToolEventUpdate(events, toolEventUpdate{
				CallID: "observer-call",
				Name:   "SPAWN",
				Output: test.final,
				Final:  true,
				Err:    test.err,
				Meta: ToolUpdateMeta{
					TaskHandle: "task-1",
					ToolStatus: test.status,
				},
			}, map[string]int{})

			if !changed || len(events) != 1 {
				t.Fatalf("linked failure events = %#v changed=%v, want one completed %s", events, changed, test.ownerName)
			}
			if event := events[0]; !event.Done || event.Err != test.expectErr || event.Output != test.final {
				t.Fatalf("linked failure %s = %#v, want authoritative output %q", test.ownerName, event, test.final)
			}
		})
	}
}

func TestLinkedCompletedSpawnFinalRespectsChildNarrativeProvenance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		existing          string
		outputNarrative   bool
		observerFinal     string
		expected          string
		checkLateSnapshot bool
	}{
		{
			name:              "child narrative rejects truncated observer final",
			existing:          "当前目录下共有 12 个文件。",
			outputNarrative:   true,
			observerFinal:     "。",
			expected:          "当前目录下共有 12 个文件。",
			checkLateSnapshot: true,
		},
		{
			name:          "parent snapshot yields to authoritative observer final",
			existing:      "still running",
			observerFinal: "final answer",
			expected:      "final answer",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			events, _, _ := applyToolEventUpdate(nil, toolEventUpdate{
				CallID: "spawn-owner",
				Name:   "SPAWN",
				Output: test.existing,
				Final:  true,
				Meta: ToolUpdateMeta{
					TaskHandle:      "task-1",
					OutputNarrative: test.outputNarrative,
				},
			}, map[string]int{})
			events, changed, _ := applyToolEventUpdate(events, toolEventUpdate{
				CallID: "spawn-observer",
				Name:   "SPAWN",
				Output: test.observerFinal,
				Final:  true,
				Meta:   ToolUpdateMeta{TaskHandle: "task-1"},
			}, map[string]int{})

			if !changed || len(events) != 1 {
				t.Fatalf("linked final events = %#v changed=%v, want one completed Spawn", events, changed)
			}
			if event := events[0]; !event.Done || event.Output != test.expected {
				t.Fatalf("linked final Spawn = %#v, want completed output %q", event, test.expected)
			}
			if !test.checkLateSnapshot {
				return
			}

			events, changed, _ = applyToolEventUpdate(events, toolEventUpdate{
				CallID: "spawn-late-observer",
				Name:   "SPAWN",
				Output: "。",
				Meta:   ToolUpdateMeta{TaskHandle: "task-1"},
			}, map[string]int{})
			if !changed || len(events) != 1 {
				t.Fatalf("late linked snapshot events = %#v changed=%v, want one completed Spawn", events, changed)
			}
			if event := events[0]; !event.Done || event.Output != test.expected {
				t.Fatalf("late linked snapshot Spawn = %#v, want completed output %q preserved", event, test.expected)
			}
		})
	}
}

func TestLinkedCompletedTaskWriteFinalDoesNotTruncateChildNarrative(t *testing.T) {
	t.Parallel()

	events, _, _ := applyToolEventUpdate(nil, toolEventUpdate{
		CallID: "task-write-owner",
		Name:   "TASK",
		Output: "完整的子代理输出。",
		Final:  true,
		Meta: ToolUpdateMeta{
			TaskHandle:      "task-1",
			TaskAction:      "write",
			OutputNarrative: true,
		},
	}, map[string]int{})
	events, changed, _ := applyToolEventUpdate(events, toolEventUpdate{
		CallID: "spawn-observer",
		Name:   "SPAWN",
		Output: "。",
		Final:  true,
		Meta:   ToolUpdateMeta{TaskHandle: "task-1"},
	}, map[string]int{})

	if !changed || len(events) != 1 {
		t.Fatalf("linked Task write events = %#v changed=%v, want one completed Task", events, changed)
	}
	if event := events[0]; !event.Done || event.Output != "完整的子代理输出。" || !event.OutputNarrative {
		t.Fatalf("linked Task write = %#v, want complete child narrative preserved", event)
	}
}

func TestSpawnFinalOutputConvergesWithoutLiveNarrativeOrWhenMoreComplete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		live     string
		final    string
		expected string
	}{
		{name: "no live narrative", final: "final child result", expected: "final child result"},
		{name: "parent running snapshot", live: "still running", final: "final answer", expected: "final answer"},
		{name: "cumulative final", live: "partial child", final: "partial child result", expected: "partial child result"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var events []SubagentEvent
			if test.live != "" {
				events, _, _ = applyToolEventUpdate(nil, toolEventUpdate{
					CallID: "spawn-1",
					Name:   "SPAWN",
					Output: test.live,
					Meta:   ToolUpdateMeta{ToolKind: "execute", MessageID: "child-message-1"},
				}, map[string]int{})
			} else {
				events, _, _ = applyToolEventUpdate(nil, toolEventUpdate{
					CallID: "spawn-1",
					Name:   "SPAWN",
					Meta:   ToolUpdateMeta{ToolKind: "execute"},
				}, map[string]int{})
			}

			events, changed, _ := applyToolEventUpdate(events, toolEventUpdate{
				CallID: "spawn-1",
				Name:   "SPAWN",
				Output: test.final,
				Final:  true,
				Meta:   ToolUpdateMeta{ToolKind: "execute"},
			}, map[string]int{})
			if !changed || len(events) != 1 {
				t.Fatalf("final update events = %#v changed=%v, want one merged event", events, changed)
			}
			if event := events[0]; !event.Done || event.Output != test.expected {
				t.Fatalf("final spawn event = %#v, want output %q", event, test.expected)
			}
		})
	}
}

func TestParentOnlyRunningSubagentSnapshotYieldsToAuthoritativeFinal(t *testing.T) {
	t.Parallel()

	for _, toolName := range []string{"SPAWN", "TASK"} {
		t.Run(toolName, func(t *testing.T) {
			t.Parallel()
			events, _, _ := applyToolEventUpdate(nil, toolEventUpdate{
				CallID: "call-1",
				Name:   toolName,
				Output: "still running",
			}, map[string]int{})
			events, changed, _ := applyToolEventUpdate(events, toolEventUpdate{
				CallID: "call-1",
				Name:   toolName,
				Output: "final answer",
				Final:  true,
			}, map[string]int{})

			if !changed || len(events) != 1 {
				t.Fatalf("final events = %#v changed=%v, want one completed %s", events, changed, toolName)
			}
			if event := events[0]; !event.Done || event.Output != "final answer" || event.OutputNarrative {
				t.Fatalf("final %s = %#v, want authoritative parent result without narrative provenance", toolName, event)
			}
		})
	}
}

func TestSpawnLiveNarrativeKeepsEqualTextFromDifferentMessages(t *testing.T) {
	t.Parallel()

	events, _, _ := applyToolEventUpdate(nil, toolEventUpdate{
		CallID: "spawn-1",
		Name:   "SPAWN",
		Output: "same child text",
		Meta:   ToolUpdateMeta{ToolKind: "execute", MessageID: "child-message-1"},
	}, map[string]int{})
	events, changed, _ := applyToolEventUpdate(events, toolEventUpdate{
		CallID: "spawn-1",
		Name:   "SPAWN",
		Output: "same child text",
		Meta:   ToolUpdateMeta{ToolKind: "execute", MessageID: "child-message-2"},
	}, map[string]int{})

	if !changed || len(events) != 1 {
		t.Fatalf("live updates = %#v changed=%v, want one Spawn panel", events, changed)
	}
	if event := events[0]; event.Output != "same child text\n\nsame child text" || event.OutputMessageID != "child-message-2" || event.OutputMessage != "same child text" {
		t.Fatalf("spawn event = %#v, want both child messages separated", event)
	}
}

func TestSpawnLiveNarrativeKeepsEqualDeltasFromSameMessage(t *testing.T) {
	t.Parallel()

	events, _, _ := applyToolEventUpdate(nil, toolEventUpdate{
		CallID: "spawn-1",
		Name:   "SPAWN",
		Output: "ha",
		Meta:   ToolUpdateMeta{ToolKind: "execute", MessageID: "child-message-1", OutputNarrative: true},
	}, map[string]int{})
	events, changed, _ := applyToolEventUpdate(events, toolEventUpdate{
		CallID: "spawn-1",
		Name:   "SPAWN",
		Output: "ha",
		Meta:   ToolUpdateMeta{ToolKind: "execute", MessageID: "child-message-1", OutputNarrative: true},
	}, map[string]int{})

	if !changed || len(events) != 1 {
		t.Fatalf("live updates = %#v changed=%v, want one Spawn panel", events, changed)
	}
	if event := events[0]; event.Output != "haha" || event.OutputMessage != "haha" || event.OutputMessageID != "child-message-1" {
		t.Fatalf("spawn event = %#v, want both identical ACP deltas preserved", event)
	}
}

func TestLinkedSpawnLiveNarrativeKeepsEqualDeltas(t *testing.T) {
	t.Parallel()

	event := SubagentEvent{Output: "ha", OutputNarrative: true}
	mergeLinkedSubagentOutput(&event, "ha", "message-1", false, true, false)
	if event.Output != "haha" {
		t.Fatalf("linked Spawn output = %q, want both identical ACP deltas preserved", event.Output)
	}
}

func TestLinkedSpawnLiveNarrativeSeparatesDistinctMessages(t *testing.T) {
	t.Parallel()

	event := SubagentEvent{
		Output:          "任务 3 完成。\n---",
		OutputMessageID: "message-1",
		OutputMessage:   "任务 3 完成。\n---",
		OutputNarrative: true,
	}
	mergeLinkedSubagentOutput(&event, "### 任务 4", "message-2", false, true, false)
	if event.Output != "任务 3 完成。\n---\n\n### 任务 4" || event.OutputMessageID != "message-2" || event.OutputMessage != "### 任务 4" {
		t.Fatalf("linked Spawn output = %#v, want distinct child messages separated", event)
	}
}
