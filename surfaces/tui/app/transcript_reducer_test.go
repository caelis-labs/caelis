package tuiapp

import "testing"

func TestMainCommandTaskOwnerBlockRequiresUniqueRenderedOwner(t *testing.T) {
	t.Parallel()

	newOwner := func(turnKey string, callID string, taskHandle string) *MainACPTurnBlock {
		block := NewMainACPTurnBlock(turnKey)
		block.Events = []SubagentEvent{{
			Kind:       SEToolCall,
			CallID:     callID,
			Name:       "RUN_COMMAND",
			ToolKind:   "execute",
			TaskHandle: taskHandle,
			Terminal:   true,
		}}
		return block
	}
	newModel := func(blocks ...*MainACPTurnBlock) *Model {
		model := NewModel(Config{NoColor: true, NoAnimation: true})
		for _, block := range blocks {
			model.doc.Append(block)
		}
		return model
	}

	t.Run("unique handle without parent call", func(t *testing.T) {
		owner := newOwner("turn-1", "command-1", "command-task")
		model := newModel(owner)

		got, ok := model.mainCommandTaskOwnerBlock("command-task", "")
		if !ok || got != owner {
			t.Fatalf("mainCommandTaskOwnerBlock() = %#v, %v; want unique owner %#v", got, ok, owner)
		}
	})

	t.Run("duplicate handle across blocks", func(t *testing.T) {
		first := newOwner("turn-1", "command-1", "command-task")
		second := newOwner("turn-2", "command-2", "command-task")
		model := newModel(first, second)

		if got, ok := model.mainCommandTaskOwnerBlock("command-task", ""); ok || got != nil {
			t.Fatalf("mainCommandTaskOwnerBlock() = %#v, %v; want ambiguous handle to fail closed", got, ok)
		}
		mutation := transcriptToolMutation{
			name:   "TASK",
			output: "must not be routed\n",
			meta: ToolUpdateMeta{
				TaskAction:     "read",
				TaskTargetKind: "command",
				TaskHandle:     "command-task",
			},
		}
		if owner := model.absorbCommandTaskObservation(TranscriptEvent{}, &mutation); owner != nil ||
			first.Events[0].Output != "" || second.Events[0].Output != "" {
			t.Fatalf("ambiguous observation owner = %#v, first = %#v, second = %#v; want no panel mutation", owner, first.Events[0], second.Events[0])
		}

		got, ok := model.mainCommandTaskOwnerBlock("command-task", "command-2")
		if !ok || got != second {
			t.Fatalf("mainCommandTaskOwnerBlock() = %#v, %v; want parent call to select %#v", got, ok, second)
		}
	})

	t.Run("duplicate owner within block", func(t *testing.T) {
		owner := newOwner("turn-1", "command-1", "command-task")
		owner.Events = append(owner.Events, SubagentEvent{
			Kind:       SEToolCall,
			CallID:     "command-2",
			Name:       "RUN_COMMAND",
			ToolKind:   "execute",
			TaskHandle: "command-task",
			Terminal:   true,
		})
		model := newModel(owner)

		if got, ok := model.mainCommandTaskOwnerBlock("command-task", ""); ok || got != nil {
			t.Fatalf("mainCommandTaskOwnerBlock() = %#v, %v; want duplicate rendered owners to fail closed", got, ok)
		}
	})

	t.Run("reused call id requires matching handle", func(t *testing.T) {
		first := newOwner("turn-1", "command-reused", "task-1")
		second := newOwner("turn-2", "command-reused", "task-2")
		model := newModel(first, second)

		got, ok := model.mainCommandTaskOwnerBlock("task-2", "command-reused")
		if !ok || got != second {
			t.Fatalf("mainCommandTaskOwnerBlock() = %#v, %v; want handle-correlated owner %#v", got, ok, second)
		}
	})
}
