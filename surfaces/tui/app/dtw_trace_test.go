package tuiapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

const desktopTraceSource = "if (false) await app.quit({});\nreturn 42;"

func desktopTraceFact(id, server, method, pluginID string, input map[string]any) *session.EventTool {
	name := mcp.ServerToolNamePrefix(server) + method
	binding := mcp.BindingForDefinition(tool.Definition{Name: name, Metadata: map[string]any{
		mcp.MetadataKind: mcp.KindTool, mcp.MetadataPluginID: pluginID, mcp.MetadataServer: server,
		mcp.MetadataTool: method, mcp.MetadataSourceFingerprint: "fixture",
	}})
	return &session.EventTool{ID: id, Name: name, Status: "pending", Binding: &binding, Input: input}
}

func desktopTraceResult(fact *session.EventTool, status, data string) *session.Event {
	result := *fact
	result.Status = status
	result.Result = &tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: []byte(data)}}}
	return &session.Event{Type: session.EventTypeToolResult, Tool: &result}
}

func desktopTraceEvents() []*session.Event {
	makeExec := func(id, server, pluginID string) *session.EventTool {
		return desktopTraceFact(id, server, "exec", pluginID, map[string]any{"app": "fixture.app", "source": desktopTraceSource})
	}
	const receipt = `{
		"app":"fixture.app","execution_id":"exe_fixture_1","status":"completed",
		"result":{"operation":"scene.quit","private":"SECRET_OUTPUT","value":9223372036854775807},
		"effects":{"dispatch":{"dispatched":2,"unknown":1},"interrupted":1,
		"acknowledgements":[{"call":1,"operation":"scene.click","confirmed":"element_focused"}]}}
	`
	native := makeExec("dtw-native", "dtw", "desktop-world")
	nested := makeExec("dtw-nested", "desktop-world", "desktop-world")
	nested.Parent = &session.EventParentTool{Kind: "code_mode", CallID: "dtw-script", Name: "js_exec"}
	script := &session.EventTool{ID: nested.Parent.CallID, Name: nested.Parent.Name, Status: "pending", Input: map[string]any{"code": `return await desktop_world__exec({app: "fixture.app", source: "OUTER_SOURCE"});`}}
	scriptDone := *script
	scriptDone.Status = "completed"
	foreign := makeExec("dtw-foreign", "dtw", "foreign")
	foreign.Title = "dtw__exec · Apps"
	foreign.Input["source"] = "return foreignFixture();"
	return []*session.Event{
		{Type: session.EventTypeToolCall, Tool: native},
		desktopTraceResult(native, "completed", receipt),
		{Type: session.EventTypeToolCall, Tool: script},
		{Type: session.EventTypeToolCall, Tool: nested},
		desktopTraceResult(nested, "completed", receipt),
		{Type: session.EventTypeToolResult, Tool: &scriptDone, Meta: map[string]any{"code_mode_script_completion": "completed"}},
		desktopTraceResult(foreign, "completed", receipt),
	}
}

func TestDesktopWorldTraceRendersForNativeAndScriptOnlyFromBoundSource(t *testing.T) {
	const summary = "Exec · 1 unknown dispatch · 1 interrupted · click · fixture.app · 2 dispatched · result returned"
	for _, replay := range []bool{false, true} {
		m, block := groupedCanonicalModel(t, desktopTraceEvents(), "main", replay)
		ctx := m.blockRenderContext(110)
		text := joinRenderedPlain(block.Render(ctx))
		if strings.Count(text, summary) != 2 || !strings.Contains(text, "Ran script") || !strings.Contains(text, "  └ "+summary) || !strings.Contains(text, "dtw__exec return foreignFixture();") {
			t.Fatalf("expected compact native and nested summaries (replay=%t):\n%s", replay, text)
		}
		for _, hidden := range []string{"scene.", "element_focused", "app.quit", "SECRET_OUTPUT", "OUTER_SOURCE"} {
			if strings.Contains(text, hidden) {
				t.Fatalf("unexpected %q in collapsed trace (replay=%t):\n%s", hidden, replay, text)
			}
		}
		for _, width := range []int{22, 24, 36} {
			narrow := joinRenderedPlain(block.Render(m.blockRenderContext(width)))
			if strings.Count(narrow, "1 unknown") != 2 {
				t.Fatalf("dispatch uncertainty lost at width %d (replay=%t):\n%s", width, replay, narrow)
			}
		}
		clickScriptToken(t, m, block, ctx, acpToolPanelClickToken("dtw-native"))
		details := joinRenderedPlain(block.Render(ctx))
		if !strings.Contains(details, "if (false) await app.quit") || !strings.Contains(details, "SECRET_OUTPUT") || !strings.Contains(details, "9223372036854775807") {
			t.Fatalf("native disclosure lost source or original result:\n%s", details)
		}
		clickScriptToken(t, m, block, ctx, acpToolPanelClickToken("dtw-native"))
		if got := joinRenderedPlain(block.Render(ctx)); got != text {
			t.Fatalf("reclosing native disclosure did not restore the compact view:\n%s", got)
		}
		clickScriptToken(t, m, block, ctx, acpToolPanelClickToken("dtw-script"))
		if got := joinRenderedPlain(block.Render(ctx)); !strings.Contains(got, "OUTER_SOURCE") {
			t.Fatalf("outer script no longer has its source disclosure:\n%s", got)
		}
		clickScriptToken(t, m, block, ctx, acpToolPanelClickToken("dtw-script"))
		clickScriptToken(t, m, block, ctx, acpToolPanelClickToken("dtw-nested"))
		if got := joinRenderedPlain(block.Render(ctx)); !strings.Contains(got, "if (false) await app.quit") || strings.Contains(got, "SECRET_OUTPUT") {
			t.Fatalf("nested invocation disclosure changed scope:\n%s", got)
		}
	}
}

func TestDesktopWorldTraceDisclosesSourceWhileRunningAndErrorAfterSettlement(t *testing.T) {
	fact := desktopTraceFact("exec-error", "dtw", "exec", "desktop-world", map[string]any{
		"app": "fixture.app", "source": "return await app.look();",
	})
	start := &session.Event{Type: session.EventTypeToolCall, Tool: fact}
	failed := desktopTraceResult(fact, "failed", `{"error":{"code":"fixture_error"},"view":{"title":"Retained view"}}`)
	failed.Tool.Error = "Desktop World fixture detail"
	m, block := groupedCanonicalModel(t, []*session.Event{start}, "main", false)
	ctx := m.blockRenderContext(110)
	if got := joinRenderedPlain(block.Render(ctx)); !strings.Contains(got, "Exec · fixture.app") || strings.Contains(got, "app.look") {
		t.Fatalf("unexpected running header:\n%s", got)
	}
	clickScriptToken(t, m, block, ctx, acpToolPanelClickToken(fact.ID))
	if got := joinRenderedPlain(block.Render(ctx)); !strings.Contains(got, "return await app.look();") {
		t.Fatalf("running call did not disclose source:\n%s", got)
	}
	for _, replay := range []bool{false, true} {
		m, block := groupedCanonicalModel(t, []*session.Event{start, failed}, "main", replay)
		ctx := m.blockRenderContext(110)
		if got := joinRenderedPlain(block.Render(ctx)); !strings.Contains(got, "Exec · failed · fixture.app") || strings.Contains(got, "fixture_error") || strings.Contains(got, failed.Tool.Error) {
			t.Fatalf("unexpected failed header (replay=%t):\n%s", replay, got)
		}
		clickScriptToken(t, m, block, ctx, acpToolPanelClickToken(fact.ID))
		if got := joinRenderedPlain(block.Render(ctx)); !strings.Contains(got, "fixture_error") || !strings.Contains(got, failed.Tool.Error) {
			t.Fatalf("failed call did not disclose error details (replay=%t):\n%s", replay, got)
		}
	}
}

func TestDesktopWorldTraceAppsCountFiltersAndCoverage(t *testing.T) {
	for _, tc := range []struct {
		name, data, summary string
		input               map[string]any
	}{
		{"limited empty", `{"apps":[],"next":null,"coverage":{"complete":false,"installed":true}}`, `Apps · 0 in scope · limited coverage · stopped · "missing" · limit 1`,
			map[string]any{"state": "stopped", "query": "missing", "limit": 1}},
		{"running only", `{"apps":[],"next":null,"coverage":{"complete":false,"installed":false}}`, `Apps · 0 in scope · running-only coverage · stopped`,
			map[string]any{"state": "stopped"}},
		{"page", `{"apps":[{"app":"one"},{"app":"two"}],"next":4,"coverage":{"complete":true,"installed":true}}`, `Apps · 2 on page · more available · running · "a" · offset 2 · limit 2`,
			map[string]any{"state": "running", "query": "a", "offset": 2, "limit": 2}},
		{"no reported coverage", `{"apps":[{"app":"one"}]}`, "Apps · 1 match", nil},
		{"absent collection", `{}`, "Apps", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fact := desktopTraceFact("apps", "dtw", "apps", "desktop-world", tc.input)
			events := []*session.Event{{Type: session.EventTypeToolCall, Tool: fact}, desktopTraceResult(fact, "completed", tc.data)}
			for _, replay := range []bool{false, true} {
				m, block := groupedCanonicalModel(t, events, "main", replay)
				if got := strings.TrimSpace(joinRenderedPlain(block.Render(m.blockRenderContext(110)))); got != "• "+tc.summary {
					t.Fatalf("summary (replay=%t): got %q, want %q", replay, got, tc.summary)
				}
			}
		})
	}
}
