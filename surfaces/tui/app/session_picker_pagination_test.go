package tuiapp

import (
	"context"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/charmbracelet/x/ansi"
)

func TestSessionWorkbenchRestartsStalePaginationAfterRefresh(t *testing.T) {
	model := openSessionPickerForPaginationTest(t, []sessionPickerPageStep{
		{ids: []string{"one", "two"}, next: "second"},
		{cursor: "second", ids: []string{"three"}, next: "stale"},
		{ids: []string{"one", "two"}, next: "refreshed-head"},
		{cursor: "stale", err: errorcode.New(errorcode.Conflict, "Session workbench priority membership changed")},
		{err: errorcode.New(errorcode.Unavailable, "Session workbench unavailable")},
		{ids: []string{"one", "four"}, next: "fresh"},
		{cursor: "fresh", ids: []string{"three", "five"}},
	})
	_, load := model.Update(keyPress("end"))
	if load == nil {
		t.Fatal("end did not request the second page")
	}
	model.Update(load())

	state := model.sessionPicker
	_, refresh := model.Update(sessionPickerRefreshMsg{request: state.request, refresh: state.refresh})
	if refresh == nil {
		t.Fatal("periodic refresh did not request the first page")
	}
	model.Update(refresh())
	if state.nextCursor != "stale" {
		t.Fatalf("periodic refresh replaced the advanced cursor with %q", state.nextCursor)
	}

	_, load = model.Update(keyPress("end"))
	if load == nil || state.selectionID() != "three" {
		t.Fatal("end did not select the last mounted Session and request more")
	}
	_, restart := model.Update(load())
	if restart == nil || state.nextCursor != "" || state.loadingCursor != "" || !state.loading {
		t.Fatal("typed conflict did not discard the stale cursor and restart at the first page")
	}
	model.Update(restart())
	if state.selectionID() != "three" || state.nextCursor != "" || state.failedCursor != "" {
		t.Fatal("failed restart restored a stale cursor or lost the selection")
	}
	if frame := ansi.Strip(model.View().Content); !strings.Contains(frame, "Refresh failed") {
		t.Fatalf("failed restart did not show a retry hint:\n%s", frame)
	}
	_, retry := model.Update(keyPress("r"))
	if retry == nil {
		t.Fatal("restart failure did not allow a first-page retry")
	}
	model.Update(retry())
	if state.selectionID() != "three" || state.nextCursor != "fresh" {
		t.Fatalf("restart left selection %q with cursor %q", state.selectionID(), state.nextCursor)
	}
	frame := ansi.Strip(model.View().Content)
	if strings.Contains(frame, "Refresh failed") || !strings.Contains(frame, "Session four") {
		t.Fatalf("unexpected frame after restart:\n%s", frame)
	}

	_, load = model.Update(keyPress("end"))
	if load == nil {
		t.Fatal("end did not continue the restarted traversal")
	}
	model.Update(load())
	var ids []string
	for _, row := range state.rows {
		ids = append(ids, row.SessionID)
	}
	if state.nextCursor != "" || !slices.Equal(ids, []string{"one", "two", "three", "four", "five"}) {
		t.Fatalf("restarted traversal ended with rows %v and cursor %q", ids, state.nextCursor)
	}
}

func TestSessionWorkbenchRefreshReopensExhaustedTraversalOnPriorityChange(t *testing.T) {
	original := "priority:" + strings.Repeat("a", 64) + ":first"
	movedHead := "recent:" + strings.Repeat("a", 64) + ":next"
	changed := "priority:" + strings.Repeat("b", 64) + ":first"
	model := openSessionPickerForPaginationTest(t, []sessionPickerPageStep{
		{ids: []string{"one", "two"}, next: original},
		{cursor: original, ids: []string{"three"}},
		{ids: []string{"two", "one"}, next: movedHead},
		{ids: []string{"two", "one"}, next: changed},
		{cursor: changed, ids: []string{"three", "four"}},
	})
	_, load := model.Update(keyPress("end"))
	if load == nil {
		t.Fatal("end did not request the second page")
	}
	model.Update(load())
	if _, load := model.Update(keyPress("end")); load != nil {
		t.Fatal("completed traversal requested another page")
	}

	state := model.sessionPicker
	refresh := func() {
		t.Helper()
		_, cmd := model.Update(sessionPickerRefreshMsg{request: state.request, refresh: state.refresh})
		if cmd == nil {
			t.Fatal("periodic refresh did not request the first page")
		}
		model.Update(cmd())
	}
	refresh()
	if state.nextCursor != "" {
		t.Fatal("a changed keyset position with the same membership reopened pagination")
	}
	refresh()
	if state.selectionID() != "three" || state.nextCursor != changed {
		t.Fatalf("refreshed selection %q with cursor %q, want selection three and a new traversal", state.selectionID(), state.nextCursor)
	}
	_, load = model.Update(keyPress("end"))
	if load == nil {
		t.Fatal("end did not load the page beyond the refreshed head")
	}
	model.Update(load())
	if state.nextCursor != "" || len(state.rows) != 4 || state.rows[3].SessionID != "four" {
		t.Fatalf("fresh traversal did not reach the previously unseen Session: %+v", state.rows)
	}
}

type sessionPickerPageStep struct {
	cursor string
	ids    []string
	next   string
	err    error
}

func openSessionPickerForPaginationTest(t *testing.T, steps []sessionPickerPageStep) *Model {
	t.Helper()
	model := NewModel(Config{NoAnimation: true, NoColor: true, SessionWorkbench: func(_ context.Context, req appserver.SessionWorkbenchRequest) (appserver.SessionWorkbenchPage, error) {
		if len(steps) == 0 || req.Cursor != steps[0].cursor {
			t.Fatalf("unexpected request for cursor %q, remaining steps: %+v", req.Cursor, steps)
		}
		step := steps[0]
		steps = steps[1:]
		page := appserver.SessionWorkbenchPage{NextCursor: step.next}
		for _, id := range step.ids {
			page.Sessions = append(page.Sessions, appserver.SessionWorkbenchRow{SessionID: id, Title: "Session " + id})
		}
		return page, step.err
	}})
	t.Cleanup(func() {
		if !t.Failed() && len(steps) != 0 {
			t.Errorf("remaining workbench requests: %+v", steps)
		}
	})
	model.Update(tea.WindowSizeMsg{Width: 100, Height: 16})
	openSessionPickerForTest(t, model)
	return model
}
