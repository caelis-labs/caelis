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
