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
		Args:   "pwd",
		Meta:   ToolUpdateMeta{ToolKind: "execute"},
	}, map[string]int{})
	if !changed || len(events) != 1 {
		t.Fatalf("patch update events = %#v changed=%v, want one event", events, changed)
	}
	if event := events[0]; event.Name != "Shell" || event.ToolKind != "execute" || event.Args != "pwd" {
		t.Fatalf("patch update event = %#v, want kind/args patched without replacing exact name", event)
	}

	events, changed, _ = applyToolEventUpdate(events, toolEventUpdate{
		CallID: "call-1",
		Output: "ok\n",
		Final:  true,
	}, map[string]int{})
	if !changed || len(events) != 1 {
		t.Fatalf("final update events = %#v changed=%v, want one event", events, changed)
	}
	if event := events[0]; !event.Done || event.Name != "Shell" || event.ToolKind != "execute" || event.Args != "pwd" || event.Output != "ok" {
		t.Fatalf("final update event = %#v, want omitted fields preserved", event)
	}
}

func TestFinalToolEventPreservesExistingPreviewFullPair(t *testing.T) {
	t.Parallel()

	index := map[string]int{}
	events, _, _ := applyToolEventUpdate(nil, toolEventUpdate{
		CallID: "call-1",
		Name:   "CUSTOM",
		Args:   "folded preview",
		Meta:   ToolUpdateMeta{FullArgs: "complete invocation"},
	}, index)
	events, _, _ = applyToolEventUpdate(events, toolEventUpdate{
		CallID: "call-1",
		Name:   "CUSTOM",
		Args:   "lossy final title",
		Final:  true,
	}, index)
	if event := events[0]; event.Args != "folded preview" || event.FullArgs != "complete invocation" {
		t.Fatalf("final event = %#v, want existing preview/full pair preserved", event)
	}

	replacementIndex := map[string]int{}
	replacement, _, _ := applyToolEventUpdate(nil, toolEventUpdate{
		CallID: "call-2",
		Name:   "CUSTOM",
		Args:   "folded preview",
		Meta:   ToolUpdateMeta{FullArgs: "complete invocation"},
	}, replacementIndex)
	replacement, _, _ = applyToolEventUpdate(replacement, toolEventUpdate{
		CallID: "call-2",
		Name:   "CUSTOM",
		Args:   "replacement preview",
		Final:  true,
		Meta:   ToolUpdateMeta{FullArgs: "replacement invocation"},
	}, replacementIndex)
	if event := replacement[0]; event.Args != "replacement preview" || event.FullArgs != "replacement invocation" {
		t.Fatalf("replacement final event = %#v, want complete incoming pair to replace existing pair", event)
	}
}

func TestApplyToolEventUpdatePreservesRepeatedExactTerminalDeltas(t *testing.T) {
	t.Parallel()

	meta := ToolUpdateMeta{ToolKind: "execute", Terminal: true, OutputTerminal: true}
	events, _, _ := applyToolEventUpdate(nil, toolEventUpdate{
		CallID: "command-1", Name: "RunCommand", Output: "tick\n", Meta: meta,
	}, map[string]int{})
	events, _, _ = applyToolEventUpdate(events, toolEventUpdate{
		CallID: "command-1", Name: "RunCommand", Output: "tick\n", Meta: meta,
	}, map[string]int{})

	if len(events) != 1 || events[0].Output != "tick\ntick\n" {
		t.Fatalf("terminal events = %#v, want both exact repeated deltas", events)
	}
}

func TestApplyToolEventUpdateSwitchesCollectionAndTerminalOutputModes(t *testing.T) {
	t.Parallel()

	index := map[string]int{}
	events, _, _ := applyToolEventUpdate(nil, toolEventUpdate{
		CallID: "execute-1", Output: "collection phase", Meta: ToolUpdateMeta{
			ToolKind: "execute", OutputCollection: true,
		},
	}, index)
	events, _, _ = applyToolEventUpdate(events, toolEventUpdate{
		CallID: "execute-1", Output: "terminal bytes\n", Meta: ToolUpdateMeta{
			ToolKind: "execute", Terminal: true, OutputTerminal: true,
		},
	}, index)
	if len(events) != 1 || events[0].Output != "terminal bytes\n" || !events[0].OutputTerminal || events[0].OutputCollection {
		t.Fatalf("terminal transition = %#v, want terminal bytes to replace the prior collection mode", events)
	}

	events, _, _ = applyToolEventUpdate(events, toolEventUpdate{
		CallID: "execute-1", Meta: ToolUpdateMeta{
			ToolKind: "execute", OutputCollection: true,
		},
	}, index)
	if len(events) != 1 || events[0].Output != "" || events[0].OutputTerminal || !events[0].OutputCollection || !events[0].Terminal {
		t.Fatalf("collection transition = %#v, want explicit empty collection to clear terminal bytes without losing terminal presentation", events)
	}
}

func TestRunCommandTerminalDeltaReplacesExplicitContentCollection(t *testing.T) {
	t.Parallel()

	index := map[string]int{}
	events, _, _ := applyToolEventUpdate(nil, toolEventUpdate{
		CallID: "command-collection-1", Name: "RunCommand", Output: "preview",
		Meta: ToolUpdateMeta{ToolKind: "execute", Terminal: true, OutputCollection: true},
	}, index)
	events, _, _ = applyToolEventUpdate(events, toolEventUpdate{
		CallID: "command-collection-1", Name: "RunCommand", Output: "actual\n",
		Meta: ToolUpdateMeta{ToolKind: "execute", Terminal: true, OutputTerminal: true},
	}, index)

	if len(events) != 1 || events[0].Output != "actual\n" || !events[0].OutputTerminal || events[0].OutputCollection {
		t.Fatalf("RunCommand transition = %#v, want terminal bytes to replace explicit collection", events)
	}
}

func TestSpawnNarrativeReplacesExplicitContentCollection(t *testing.T) {
	t.Parallel()

	index := map[string]int{}
	events, _, _ := applyToolEventUpdate(nil, toolEventUpdate{
		CallID: "spawn-collection-1", Name: "Spawn", Output: "starting",
		Meta: ToolUpdateMeta{ToolKind: "execute", OutputCollection: true},
	}, index)
	events, _, _ = applyToolEventUpdate(events, toolEventUpdate{
		CallID: "spawn-collection-1", Name: "Spawn", Output: "answer",
		Meta: ToolUpdateMeta{ToolKind: "execute", MessageID: "child-1", OutputNarrative: true},
	}, index)

	if len(events) != 1 || events[0].Output != "answer" || !events[0].OutputNarrative || events[0].OutputCollection {
		t.Fatalf("Spawn transition = %#v, want child narrative to replace explicit collection", events)
	}
}

func TestExactNameToolsReplaceExplicitContentCollections(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"RunCommand", "Spawn"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			index := map[string]int{}
			events, _, _ := applyToolEventUpdate(nil, toolEventUpdate{
				CallID: name + "-collection", Name: name, Output: "phase 1",
				Meta: ToolUpdateMeta{ToolKind: "execute", OutputCollection: true},
			}, index)
			events, _, _ = applyToolEventUpdate(events, toolEventUpdate{
				CallID: name + "-collection", Name: name, Output: "phase 2",
				Meta: ToolUpdateMeta{ToolKind: "execute", OutputCollection: true},
			}, index)
			if len(events) != 1 || events[0].Output != "phase 2" || !events[0].OutputCollection || events[0].OutputTerminal || events[0].OutputNarrative {
				t.Fatalf("%s events = %#v, want the latest explicit collection snapshot", name, events)
			}
		})
	}
}

func TestRunCommandFinalCollectionReplacesEarlierCollectionDespiteTerminalPresentation(t *testing.T) {
	t.Parallel()

	index := map[string]int{}
	events, _, _ := applyToolEventUpdate(nil, toolEventUpdate{
		CallID: "command-final-collection", Name: "RunCommand", Output: "phase 1",
		Meta: ToolUpdateMeta{ToolKind: "execute", Terminal: true, OutputCollection: true},
	}, index)
	events, _, _ = applyToolEventUpdate(events, toolEventUpdate{
		CallID: "command-final-collection", Name: "RunCommand", Output: "phase 2", Final: true,
		Meta: ToolUpdateMeta{ToolKind: "execute", Terminal: true, OutputCollection: true},
	}, index)

	if len(events) != 1 || !events[0].Done || events[0].Output != "phase 2" || !events[0].OutputCollection || events[0].OutputTerminal {
		t.Fatalf("RunCommand final collection = %#v, want the final snapshot to replace the earlier collection", events)
	}
}

func TestSpawnCollectionClearsPriorNarrativeProvenanceBeforeFinalSnapshot(t *testing.T) {
	t.Parallel()

	index := map[string]int{}
	events, _, _ := applyToolEventUpdate(nil, toolEventUpdate{
		CallID: "spawn-mode-1", Name: "Spawn", Output: "answer",
		Meta: ToolUpdateMeta{ToolKind: "execute", MessageID: "child-1", OutputNarrative: true},
	}, index)
	events[0].OutputNarrativeBoundary = true
	events, _, _ = applyToolEventUpdate(events, toolEventUpdate{
		CallID: "spawn-mode-1", Name: "Spawn", Output: "status",
		Meta: ToolUpdateMeta{ToolKind: "execute", OutputCollection: true},
	}, index)
	if events[0].Output != "status" || !events[0].OutputCollection || events[0].OutputNarrative || events[0].OutputNarrativeBoundary {
		t.Fatalf("Spawn running collection = %#v, want collection mode without stale narrative provenance", events[0])
	}
	events, _, _ = applyToolEventUpdate(events, toolEventUpdate{
		CallID: "spawn-mode-1", Name: "Spawn", Output: "final", Final: true,
		Meta: ToolUpdateMeta{ToolKind: "execute", OutputCollection: true},
	}, index)
	if len(events) != 1 || !events[0].Done || events[0].Output != "final" || !events[0].OutputCollection || events[0].OutputNarrative {
		t.Fatalf("Spawn final collection = %#v, want final snapshot to replace running collection", events)
	}
}

func TestCompletedRunCommandDuplicateEmptyFinalPreservesStreamedOutput(t *testing.T) {
	t.Parallel()

	index := map[string]int{}
	meta := ToolUpdateMeta{ToolKind: "execute", Terminal: true, OutputTerminal: true}
	events, _, _ := applyToolEventUpdate(nil, toolEventUpdate{
		CallID: "command-1", Name: "RunCommand", Output: "done\n", Meta: meta,
	}, index)
	final := toolEventUpdate{
		CallID: "command-1", Name: "RunCommand", Final: true,
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
		Name:   "Spawn",
		Output: "当前目录下共有 12 个文件",
		Meta:   ToolUpdateMeta{ToolKind: "execute", MessageID: "child-message-1", OutputNarrative: true},
	}, map[string]int{})
	if !changed || len(events) != 1 {
		t.Fatalf("live update events = %#v changed=%v, want one event", events, changed)
	}

	events, changed, _ = applyToolEventUpdate(events, toolEventUpdate{
		CallID: "spawn-1",
		Name:   "Spawn",
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
		Name:   "Spawn",
		Output: "当前目录下共有 12 个文件。",
		Meta:   ToolUpdateMeta{ToolKind: "execute", MessageID: "child-message-1", OutputNarrative: true},
	}, map[string]int{})
	events, changed, _ := applyToolEventUpdate(events, toolEventUpdate{
		CallID: "spawn-1",
		Name:   "Spawn",
		Output: "。",
		Final:  true,
		Meta:   ToolUpdateMeta{ToolKind: "execute"},
	}, map[string]int{})
	if !changed || len(events) != 1 || !events[0].Done {
		t.Fatalf("first final events = %#v changed=%v, want one completed Spawn", events, changed)
	}

	events, changed, _ = applyToolEventUpdate(events, toolEventUpdate{
		CallID: "spawn-1",
		Name:   "Spawn",
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

func TestCompletedTaskWriteDuplicateFinalStaysCompact(t *testing.T) {
	t.Parallel()

	events, _, _ := applyToolEventUpdate(nil, toolEventUpdate{
		CallID: "task-1",
		Name:   "Task",
		Final:  true,
		Meta: ToolUpdateMeta{
			TaskAction: "write",
			TaskInput:  "hello",
			Terminal:   true,
		},
	}, map[string]int{})
	events, changed, _ := applyToolEventUpdate(events, toolEventUpdate{
		CallID: "task-1",
		Name:   "Task",
		Output: "。",
		Final:  true,
		Meta:   ToolUpdateMeta{TaskAction: "write", OutputNarrative: true},
	}, map[string]int{})

	if !changed || len(events) != 1 {
		t.Fatalf("duplicate final events = %#v changed=%v, want one completed Task write", events, changed)
	}
	if event := events[0]; !event.Done || event.Output != "" || event.Terminal {
		t.Fatalf("duplicate final Task write = %#v, want compact interaction without child output", event)
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
		{name: "spawn failed", toolName: "Spawn", status: "failed", err: true, final: "subagent failed: boom", expectErr: true},
		{name: "task cancelled", toolName: "Task", status: "cancelled", final: "subagent cancelled"},
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
		{name: "linked spawn failed", ownerName: "Spawn", status: "failed", err: true, final: "subagent failed: boom", expectErr: true},
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
				Name:   "Spawn",
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
				Name:   "Spawn",
				Output: test.existing,
				Final:  true,
				Meta: ToolUpdateMeta{
					TaskHandle:      "task-1",
					OutputNarrative: test.outputNarrative,
				},
			}, map[string]int{})
			events, changed, _ := applyToolEventUpdate(events, toolEventUpdate{
				CallID: "spawn-observer",
				Name:   "Spawn",
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
				Name:   "Spawn",
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

func TestSpawnFinalDoesNotAttachToTaskWriteInteraction(t *testing.T) {
	t.Parallel()

	events, _, _ := applyToolEventUpdate(nil, toolEventUpdate{
		CallID: "task-write-owner",
		Name:   "Task",
		Final:  true,
		Meta: ToolUpdateMeta{
			TaskHandle: "task-1",
			TaskAction: "write",
			TaskInput:  "continue",
			Terminal:   true,
		},
	}, map[string]int{})
	events, changed, _ := applyToolEventUpdate(events, toolEventUpdate{
		CallID: "spawn-observer",
		Name:   "Spawn",
		Output: "完整的子代理输出。",
		Final:  true,
		Meta:   ToolUpdateMeta{TaskHandle: "task-1"},
	}, map[string]int{})

	if !changed || len(events) != 2 {
		t.Fatalf("events = %#v changed=%v, want Task write to stay separate from Spawn", events, changed)
	}
	if write := events[0]; write.CallID != "task-write-owner" || write.Output != "" || write.Terminal {
		t.Fatalf("Task write = %#v, want a compact control row without child output or terminal ownership", write)
	}
	if spawn := events[1]; spawn.CallID != "spawn-observer" || spawn.Output != "完整的子代理输出。" {
		t.Fatalf("Spawn = %#v, want child output kept on the Spawn event", spawn)
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
					Name:   "Spawn",
					Output: test.live,
					Meta:   ToolUpdateMeta{ToolKind: "execute", MessageID: "child-message-1"},
				}, map[string]int{})
			} else {
				events, _, _ = applyToolEventUpdate(nil, toolEventUpdate{
					CallID: "spawn-1",
					Name:   "Spawn",
					Meta:   ToolUpdateMeta{ToolKind: "execute"},
				}, map[string]int{})
			}

			events, changed, _ := applyToolEventUpdate(events, toolEventUpdate{
				CallID: "spawn-1",
				Name:   "Spawn",
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

	for _, toolName := range []string{"Spawn", "Task"} {
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
		Name:   "Spawn",
		Output: "same child text",
		Meta:   ToolUpdateMeta{ToolKind: "execute", MessageID: "child-message-1"},
	}, map[string]int{})
	events, changed, _ := applyToolEventUpdate(events, toolEventUpdate{
		CallID: "spawn-1",
		Name:   "Spawn",
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
		Name:   "Spawn",
		Output: "ha",
		Meta:   ToolUpdateMeta{ToolKind: "execute", MessageID: "child-message-1", OutputNarrative: true},
	}, map[string]int{})
	events, changed, _ := applyToolEventUpdate(events, toolEventUpdate{
		CallID: "spawn-1",
		Name:   "Spawn",
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
