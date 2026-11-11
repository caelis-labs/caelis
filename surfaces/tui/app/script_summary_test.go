package tuiapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestScriptSummaryIsPerInvocationAndLive(t *testing.T) {
	events := consecutiveScriptEvents()
	for _, n := range []int{1, 2, 4, len(events)} {
		m, b := groupedCanonicalModel(t, events[:n], "main", false)
		text := joinRenderedPlain(b.Render(m.blockRenderContext(100)))
		t.Logf("events=%d\n%s", n, text)
		if n == 1 && !strings.Contains(text, "Running script") {
			t.Fatal("zero-child running state absent")
		}
		if n >= 2 && !strings.Contains(text, "script1Read") {
			t.Fatal("started child missing from live folded summary")
		}
		if n == len(events) && strings.Count(text, "• Called tools") != 2 {
			t.Fatal("invocations merged")
		}
	}
}

func TestScriptSummaryKeepsNarrativeOutsideAndNoFailureLabels(t *testing.T) {
	events := consecutiveScriptEvents()
	for _, ev := range events {
		if ev.Tool != nil && ev.Tool.ID == "script1Read" && ev.Type == session.EventTypeToolResult {
			ev.Tool.Status = "failed"
			ev.Tool.Content = []session.EventToolContent{{Type: "content", Text: "path missing: bad.txt"}}
		}
	}
	m, b := groupedCanonicalModel(t, events, "main", true)
	ctx := m.blockRenderContext(100)
	before := joinRenderedPlain(b.Render(ctx))
	if strings.Contains(before, "failed") || strings.Contains(before, "Result") {
		t.Fatal(before)
	}
	if !strings.Contains(before, "BETWEEN_SCRIPT_STEPS") {
		t.Fatal("narrative hidden by folding")
	}
	token := acpStableExplorationClickToken("tools:script1")
	clickScriptToken(t, m, b, ctx, token)
	rows := b.Render(ctx)
	text := joinRenderedPlain(rows)
	t.Log(text)
	if strings.Count(text, "└") != 2 {
		t.Fatal("expected one public child prefix per invocation")
	}
	if !strings.Contains(text, "script1Read") || !strings.Contains(text, "script2Read") {
		t.Fatal(text)
	}
}

func TestScriptSummaryDoesNotInventAnEmptyFailureResult(t *testing.T) {
	for _, literal := range []bool{false, true} {
		events := consecutiveScriptEvents()[:6]
		events[2].Tool.Status = "failed"
		events[2].Tool.Content = []session.EventToolContent{{Type: "content", Text: "original child failure"}}
		events[5].Tool.Status = "failed"
		if literal {
			events[5].Tool.Content = []session.EventToolContent{{Type: "content", Text: "failed"}}
		}
		m, b := groupedCanonicalModel(t, events, "main", true)
		ctx := m.blockRenderContext(60)
		clickScriptToken(t, m, b, ctx, acpStableExplorationClickToken("tools:script1"))
		text := joinRenderedPlain(b.Render(ctx))
		hasStatusLine := false
		for _, line := range strings.Split(text, "\n") {
			hasStatusLine = hasStatusLine || strings.TrimSpace(line) == "failed"
		}
		if hasStatusLine != literal {
			t.Fatalf("literal=%v: unexpected synthetic status/result\n%s", literal, text)
		}
		root := b.(*MainACPTurnBlock).Events[0]
		if !root.Err {
			t.Fatal("original failure flag lost")
		}
		clickScriptToken(t, m, b, ctx, acpToolPanelClickToken("script1Read"))
		if !strings.Contains(joinRenderedPlain(b.Render(ctx)), "original child failure") {
			t.Fatal("actual child error lost")
		}
	}
}
