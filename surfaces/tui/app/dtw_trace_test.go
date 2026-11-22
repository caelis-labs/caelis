package tuiapp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/projection"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
)

const desktopTraceApp = "C:/Program Files/Example Distribution/Long Browser Install/Chrome.exe"
const desktopTraceSource = `const view = await app.look();
await app.click({selector:{role:"button",text:"New tab"}});
await app.look();
await app.keypress({keys:["META","L"]});`
const desktopTraceResultJSON = `{"status":"completed","execution_id":"fixture","effects":{"dispatch":{"dispatched":2}},"result":{"value":"RESULT_DETAIL"}}`

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
	message := model.Message{Role: model.RoleTool, Parts: []model.Part{{
		Kind: model.PartKindToolResult,
		ToolResult: &model.ToolResultPart{
			ToolUseID: result.ID, Name: result.Name, Content: tool.ModelParts(result.Result.Content),
		},
	}}}
	if err := json.Unmarshal([]byte(data), &result.Output); err != nil {
		panic(err)
	}
	return &session.Event{Type: session.EventTypeToolResult, Tool: &result, Message: &message}
}

func desktopTraceEvents() []*session.Event {
	start := func(fact *session.EventTool) *session.Event {
		return &session.Event{Type: session.EventTypeToolCall, Tool: fact}
	}
	native := desktopTraceFact("native", "dtw", "exec", "desktop-world", map[string]any{"app": desktopTraceApp, "source": desktopTraceSource})
	nested := desktopTraceFact("nested", "desktop-world", "exec", "desktop-world", map[string]any{"app": desktopTraceApp, "source": desktopTraceSource})
	nested.Parent = &session.EventParentTool{Kind: "code_mode", CallID: "script", Name: "js_exec"}
	script := &session.EventTool{ID: "script", Name: "js_exec", Status: "pending", Input: map[string]any{"code": `await desktop_world__exec({app:"Long app path",source:"OUTER_SOURCE"});`}}
	scriptDone := *script
	scriptDone.Status = "completed"
	apps := desktopTraceFact("apps", "dtw", "apps", "desktop-world", map[string]any{"query": "Chrome", "state": "running", "limit": 2})
	failed := desktopTraceFact("failed", "dtw", "exec", "desktop-world", map[string]any{
		"app": "com.example.Editor", "source": `if (ready) await app.click("Continue");`,
	})
	return []*session.Event{
		start(native), desktopTraceResult(native, "completed", desktopTraceResultJSON),
		start(apps), desktopTraceResult(apps, "completed", `{"apps":[],"coverage":{"complete":false}}`),
		start(failed), desktopTraceResult(failed, "failed", `{"error":"fixture permission denied"}`),
		start(script), start(nested), desktopTraceApproval(nested), desktopTraceResult(nested, "completed", desktopTraceResultJSON),
		{Type: session.EventTypeToolResult, Tool: &scriptDone, Meta: map[string]any{"code_mode_script_completion": "completed"}},
	}
}

func desktopTraceApproval(fact *session.EventTool) *session.Event {
	return &session.Event{Type: session.EventTypeLifecycle, Visibility: session.VisibilityJournal,
		Approval: &session.ApprovalRecord{Status: session.ApprovalRecordResolved, TurnID: "turn",
			ToolCallID: fact.ID, ToolName: fact.Name, Approved: true, ReviewText: "approved: fixture review"},
	}
}

func desktopTraceModel(t *testing.T, events []*session.Event, width int, replay bool) (*Model, Block) {
	t.Helper()
	m := newACPEventstreamRegressionModel(t, width, 48)
	m.currentSessionID = "desktop-fixture"
	var history []eventstream.Envelope
	for _, envelope := range desktopTraceEnvelopes(events, m.currentSessionID) {
		if !replay {
			m.Update(envelope)
			continue
		}
		wire, err := wirev1.MarshalEnvelope(envelope)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := wirev1.UnmarshalEnvelope(wire)
		if err != nil {
			t.Fatal(err)
		}
		history = append(history, decoded)
	}
	if replay {
		m.beginSessionHistory(sessionViewStartMsg{state: appserver.SessionState{SessionID: m.currentSessionID}})
		m.sessionHistory.model.handleTranscriptEventsMsg(TranscriptEventsMsg{Events: projectResumeReplayEvents(history), ReconnectReplay: true})
		m.commitSessionHistory()
	}
	b := requireMainACPTurnBlockForTest(t, m)
	return m, b
}

func desktopTraceEnvelopes(events []*session.Event, sessionID string) []eventstream.Envelope {
	var out []eventstream.Envelope
	for i, original := range events {
		ev := session.CloneEvent(original)
		ev.SessionID = sessionID
		ev.ID, ev.Seq = fmt.Sprintf("desktop-event-%d", i), uint64(i+1)
		if ev.Visibility == "" {
			ev.Visibility = session.VisibilityCanonical
			if ev.Tool != nil && ev.Tool.Parent != nil {
				ev.Visibility = session.VisibilityMirror
			}
		}
		base := projection.EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: sessionID}, ev, projection.SessionEventTransport{TurnID: "turn"})
		out = append(out, projection.ProjectSessionEventEnvelope(base, ev)...)
	}
	return out
}

func TestDesktopWorldTraceGoldenAndDisclosure(t *testing.T) {
	for _, width := range []int{24, 112} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			for _, replay := range []bool{false, true} {
				m, b := desktopTraceModel(t, desktopTraceEvents(), width, replay)
				ctx := m.blockRenderContext(width)
				rows := b.Render(ctx)
				for _, row := range rows {
					if cells := displayColumns(row.Plain); cells > minInt(width, 60) {
						t.Fatalf("row exceeds width: %d, %q", cells, row.Plain)
					}
				}
				collapsed := joinRenderedPlain(rows) + "\n"
				path := filepath.Join("testdata", fmt.Sprintf("desktop_world_%d.golden", width))
				if !replay && os.Getenv("UPDATE_GOLDEN") == "1" {
					if err := os.WriteFile(path, []byte(collapsed), 0600); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if collapsed != string(want) {
					t.Fatalf("replay=%v rendered rows differ from %s:\n%s", replay, path, collapsed)
				}
				for _, hidden := range []string{"unknown", "coverage", "RESULT_DETAIL", "OUTER_SOURCE", "await app", "approved", "Plan to", "Run a script", "more"} {
					if strings.Contains(collapsed, hidden) {
						t.Fatalf("collapsed trace exposes detail %q:\n%s", hidden, collapsed)
					}
				}
				clickScriptToken(t, m, b, ctx, acpStableExplorationClickToken("computer:native"))
				for _, callID := range []string{"native", "nested"} {
					ctx := m.blockRenderContext(44)
					clickScriptToken(t, m, b, ctx, acpToolPanelClickToken(callID))
					details := joinRenderedPlain(b.Render(ctx))
					squeezed := strings.Join(strings.Fields(details), "")
					path := strings.Join(strings.Fields(desktopTraceApp), "")
					if !strings.Contains(squeezed, path) || !strings.Contains(squeezed, "awaitapp.look()") {
						t.Fatalf("%s lost complete app or source in disclosure:\n%s", callID, details)
					}
					if !strings.Contains(details, "RESULT_DETAIL") {
						t.Fatalf("%s lost the structured result in disclosure:\n%s", callID, details)
					}
					if callID == "nested" && !strings.Contains(details, "[approved]") {
						t.Fatalf("missing nested approval record:\n%s", details)
					}
					clickScriptToken(t, m, b, ctx, acpToolPanelClickToken(callID))
				}
				clickScriptToken(t, m, b, ctx, acpToolPanelClickToken("failed"))
				failure := strings.Join(strings.Fields(joinRenderedPlain(b.Render(ctx))), "")
				if !strings.Contains(failure, "fixturepermissiondenied") {
					t.Fatalf("failed call lost error detail: %s", failure)
				}
			}
		})
	}
}

func TestDesktopWorldTraceGroupLimitRetainsRunningCall(t *testing.T) {
	for _, nested := range []bool{false, true} {
		t.Run(fmt.Sprintf("nested=%t", nested), func(t *testing.T) {
			var events []*session.Event
			if nested {
				events = append(events, &session.Event{Type: session.EventTypeToolCall, Tool: &session.EventTool{
					ID: "script", Name: "js_exec", Status: "pending", Input: map[string]any{"code": "OUTER_SOURCE"},
				}})
			}
			for i := 1; i <= 5; i++ {
				fact := desktopTraceFact(fmt.Sprint(i), "dtw", "exec", "desktop-world", map[string]any{
					"app": fmt.Sprintf("App%d", i), "source": `await app.click("Continue");`,
				})
				if nested {
					fact.Parent = &session.EventParentTool{Kind: "code_mode", CallID: "script", Name: "js_exec"}
				}
				events = append(events, &session.Event{Type: session.EventTypeToolCall, Tool: fact})
				if i < 5 {
					events = append(events, desktopTraceResult(fact, "completed", desktopTraceResultJSON))
				}
			}
			for _, replay := range []bool{false, true} {
				m, b := desktopTraceModel(t, events, 24, replay)
				ctx := m.blockRenderContext(24)
				got := joinRenderedPlain(b.Render(ctx))
				for _, want := range []string{"App4", "App5"} {
					if !strings.Contains(got, want) {
						t.Fatalf("replay=%v missing %s in running group:\n%s", replay, want, got)
					}
				}
				if strings.Contains(got, "App1") || strings.Contains(got, "App2") || strings.Contains(got, "App3") ||
					strings.Contains(got, "more") || strings.Count(got, "└") != 1 {
					t.Fatalf("running group should show only two recent calls:\n%s", got)
				}
				label, key := "Using computer", "computer:1"
				if nested {
					key = "computer:script"
				}
				if !strings.Contains(got, label) {
					t.Fatalf("missing active group label %q:\n%s", label, got)
				}
				clickScriptToken(t, m, b, ctx, acpToolPanelClickToken("5"))
				if got := joinRenderedPlain(b.Render(ctx)); !strings.Contains(got, "Arguments") {
					t.Fatalf("running invocation cannot disclose input:\n%s", got)
				}
				clickScriptToken(t, m, b, ctx, acpStableExplorationClickToken(key))
				expanded := joinRenderedPlain(b.Render(ctx))
				if !strings.Contains(expanded, "App1") || !strings.Contains(expanded, "App3") || strings.Count(expanded, "└") != 1 {
					t.Fatalf("group disclosure lost calls:\n%s", expanded)
				}
			}
		})
	}
}

func TestDesktopWorldTraceTerminalActions(t *testing.T) {
	const beforeKey = `await app.look({role:'text_field',text:'Search inventory'});
await app.click({selector:{role:'text_field',text:'Search inventory'}});
await app.look({role:'text_field',text:'Search inventory'});
`
	const key = `await app.keypress({keys:['META','A']});`
	const twoDispatches = `{"execution_id":"fixture","effects":{"dispatch":{"dispatched":2}}}`
	for _, test := range []struct {
		name, source, status, result string
		wide, narrow, forbidden      string
	}{
		{"straight-line input hides observations", beforeKey + key, "completed", twoDispatches,
			"Click Search inventory, press META+A in fixture.desktop", "Click", "Inspect"},
		{"straight-line input without dispatch counters", beforeKey + key, "completed", `{"execution_id":"fixture"}`,
			"Click Search inventory, press META+A in fixture.desktop", "Click", "Inspect"},
		{"B09 literal chord in straight-line source", "const chord = ['META','A'];\n" + beforeKey + "await app.keypress({keys:chord});", "completed", twoDispatches,
			"Click Search inventory, press META+A in fixture.desktop", "Click", "Inspect"},
		{"S02 guarded read", `const view = await app.look({role:'text_area',text:'Handoff note'});
if (view.elements.length !== 1) return null;
return await app.readText({target:view.elements[0].id});`, "completed", `{"execution_id":"fixture"}`,
			"Look for “Handoff note”", "Inspect", "press"},
		{"S06 read after dead branch", `const text = "app.keypress()";
if (false) await app.quit({});
await app.look({text:'order number'});
return {text};`, "completed", `{"execution_id":"fixture"}`,
			"Look for “order number”", "Inspect", "press"},
		{"interrupted", beforeKey + key, "failed", `{"execution_id":"fixture","error":{"code":"execution_interrupted"}}`,
			"execution_interrupted", "execution_interrupt", "press"},
		{"early return", beforeKey + "return 42;\n" + key, "completed", `{"execution_id":"fixture","result":42}`,
			"Click “Search inventory” in fixture.desktop", "Click", "press"},
		{"refused input caught by script", beforeKey + "try { " + key + " } catch (e) { return e.code; }", "completed",
			`{"execution_id":"fixture","effects":{"dispatch":{"dispatched":1,"not_dispatched":1}},"result":"input_not_ready"}`,
			"", "", "Using computer"},
		{"B04 argument evaluation order", `await app.keypress({keys: (await app.click({selector:{role:'text_field',text:'Search inventory'}}), ['META','A'])});`,
			"completed", twoDispatches, "", "", "Using computer"},
		{"B05 uninstantiated class", `class Clicker {
  effect = app.click({selector:{role:'text_field',text:'Search inventory'}});
}
class Unused {
  effect = app.keypress({keys:['META','A']});
}
await new Clicker().effect;
await new Clicker().effect;`, "completed", twoDispatches, "", "", "Using computer"},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, nested := range []bool{false, true} {
				fact := desktopTraceFact("desktop", "dtw", "exec", "desktop-world", map[string]any{"app": "fixture.desktop", "source": test.source})
				final := desktopTraceResult(fact, test.status, test.result)
				events := desktopTracePlacement(t, fact, final, nested, "")
				if nested && test.status == "failed" {
					final.Tool.Content = []session.EventToolContent{{Type: "content", Text: test.result}}
				}
				for _, width := range []int{24, 112} {
					want := test.wide
					if width == 24 {
						want = test.narrow
					}
					for _, replay := range []bool{false, true} {
						m, b := desktopTraceModel(t, events, width, replay)
						got := joinRenderedPlain(b.Render(m.blockRenderContext(width)))
						if !strings.Contains(got, want) || test.forbidden != "" && strings.Contains(strings.ToLower(got), strings.ToLower(test.forbidden)) {
							t.Fatalf("nested=%t replay=%t width=%d want %q without %q:\n%s", nested, replay, width, want, test.forbidden, got)
						}
					}
				}
			}
		})
	}
}

// Native Code Mode stores the structured result on the child, while its
// bare structured payload may be absent from the child's ACP update.
func desktopTracePlacement(t *testing.T, call *session.EventTool, final *session.Event, nested bool, parentOutput string) []*session.Event {
	t.Helper()
	start := &session.Event{Type: session.EventTypeToolCall, Tool: call}
	if !nested {
		return []*session.Event{start, final}
	}
	args, err := json.Marshal(call.Input)
	if err != nil {
		t.Fatal(err)
	}
	code := "return await " + call.Name + "(" + string(args) + ");"
	root := &session.EventTool{ID: "outer", Name: "js_exec", Status: "pending", Input: map[string]any{"code": code}}
	call.Parent = &session.EventParentTool{Kind: "code_mode", CallID: root.ID, Name: root.Name}
	final.Tool.Parent = call.Parent
	if parentOutput == "" {
		parentOutput = `{"script":"outer only"}`
	}
	final.Tool.Output = nil
	return []*session.Event{{Type: session.EventTypeToolCall, Tool: root}, start, final, desktopTraceResult(root, "completed", parentOutput)}
}

func TestDesktopWorldAppsResultBelongsToItsCall(t *testing.T) {
	const empty = `{"apps":[],"coverage":{"complete":false},"next":null}`
	const matching = `{"apps":[{"app":"fixture.desktop","name":"Fixture Desktop"}],"next":null}`
	for _, test := range []struct {
		name, child, parent string
		nested, childOutput bool
		wantEmpty           bool
	}{
		{"direct empty", empty, "", false, true, true},
		{"direct more pages", `{"apps":[],"next":"cursor"}`, "", false, true, false},
		{"nested child output", empty, matching, true, true, true},
		{"nested equal parent still not child output", empty, empty, true, false, false},
		{"B08 shadowed parent returns false empty", matching, empty, true, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			fact := desktopTraceFact("apps", "dtw", "apps", "desktop-world", map[string]any{"query": "Fixture"})
			final := desktopTraceResult(fact, "completed", test.child)
			ownOutput := final.Tool.Output
			events := desktopTracePlacement(t, fact, final, test.nested, test.parent)
			if test.childOutput {
				final.Tool.Output = ownOutput
			}
			for _, replay := range []bool{false, true} {
				m, b := desktopTraceModel(t, events, 112, replay)
				ctx := m.blockRenderContext(112)
				got := joinRenderedPlain(b.Render(ctx))
				if strings.Contains(got, "No apps in scope") != test.wantEmpty || !strings.Contains(got, "Fixture") || strings.Contains(got, "coverage") {
					t.Fatalf("replay=%t wrong scoped search summary:\n%s", replay, got)
				}
				clickScriptToken(t, m, b, ctx, acpToolPanelClickToken(fact.ID))
				got = joinRenderedPlain(b.Render(ctx))
				if !strings.Contains(got, "Arguments") || strings.Contains(got, "Result") != test.childOutput ||
					strings.Contains(got, "Fixture Desktop") {
					t.Fatalf("replay=%t wrong result ownership in disclosure:\n%s", replay, got)
				}
			}
		})
	}
}

func TestDesktopWorldTraceGroupsAdjacentDirectCalls(t *testing.T) {
	events := desktopTraceEvents()
	ordinary := &session.Event{Type: session.EventTypeToolCall, Tool: &session.EventTool{
		ID: "other", Name: "RunCommand", Status: "in_progress", Input: map[string]any{"command": "printf example"},
	}}
	events = append(events[:2:2], append([]*session.Event{ordinary}, events[2:]...)...)
	for _, replay := range []bool{false, true} {
		m, b := desktopTraceModel(t, events, 112, replay)
		text := joinRenderedPlain(b.Render(m.blockRenderContext(112)))
		if strings.Count(text, "Used computer") != 1 || strings.Count(text, "Using computer") != 1 || strings.Contains(text, "js_exec") {
			t.Fatalf("replay=%v unrelated invocation merged adjacent computer groups:\n%s", replay, text)
		}
	}
}

func TestDesktopWorldTraceUsesGenericDisplayForUnknownSource(t *testing.T) {
	input := map[string]any{"app": "Peer", "source": `await app.click("Pretend");`}
	foreign := desktopTraceFact("foreign", "dtw", "exec", "foreign", input)
	name := foreign.Name
	forged := &session.Event{Type: session.EventTypeToolCall, Visibility: session.VisibilityMirror,
		Protocol: &session.EventProtocol{Method: session.ProtocolMethodSessionUpdate, Update: &session.ProtocolUpdate{
			SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "peer", Name: &name, Title: name + " · Peer", Status: "pending",
			RawInput: input,
			Meta:     map[string]any{"tui.desktop_world": "exec", "caelis": map[string]any{"display": map[string]any{"desktop_world": "exec"}}},
		}},
	}
	transient := desktopTraceFact("unpositioned", "dtw", "exec", "desktop-world", input)
	for _, replay := range []bool{false, true} {
		m, b := desktopTraceModel(t, []*session.Event{
			{Type: session.EventTypeToolCall, Tool: foreign}, forged,
			{Type: session.EventTypeToolCall, Visibility: session.VisibilityUIOnly, Tool: transient},
		}, 90, replay)
		text := joinRenderedPlain(b.Render(m.blockRenderContext(90)))
		if !strings.Contains(text, "dtw__exec") || strings.Contains(text, "Using computer") || strings.Contains(text, "Click “Pretend” in Peer") {
			t.Fatalf("replay=%t unknown source selected native presentation:\n%s", replay, text)
		}
		for _, ev := range scriptBlockEvents(b) {
			if ev.desktopWorld != nil {
				t.Fatalf("unknown source supplied presentation: %s", ev.CallID)
			}
		}
	}
}

func TestDesktopWorldTraceReadyWithPhysicalSize(t *testing.T) {
	m, b := desktopTraceModel(t, desktopTraceEvents(), 24, false)
	m.Update(tea.WindowSizeMsg{Width: 24, Height: 30})
	for _, row := range b.Render(m.blockRenderContext(m.viewport.Width())) {
		if displayColumns(row.Plain) > m.viewport.Width() {
			t.Fatalf("row wider than TUI viewport: %q", row.Plain)
		}
	}
}

// These are selected scripts from s-s7jj2vjwb4hlq4gn7vyynbulwk. Window IDs and
// selector text are anonymized in the fixture. Only Obsidian 309 is read live.
func desktopWorldSessionScripts(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile("testdata/desktop_world_session_scripts.json")
	if err != nil {
		t.Fatal(err)
	}
	var scripts map[string]string
	if err := json.Unmarshal(raw, &scripts); err != nil {
		t.Fatal(err)
	}
	return scripts
}

func desktopTraceToolStart(tool *session.EventTool) *session.Event {
	return &session.Event{Type: session.EventTypeToolCall, Tool: tool}
}

func TestDesktopWorldSessionScriptsLiveReplay(t *testing.T) {
	scripts := desktopWorldSessionScripts(t)
	for _, test := range []struct {
		seq, action, compact string
	}{
		{"104", "Inspect the window", "Inspect"},
		{"113", "Inspect the menu", "Inspect"},
		{"128", "Click the observed control", "Click"},
		{"153", "Look for text field controls", "Inspect"},
		{"158", "Press RETURN", "Press RETURN"},
		{"173", "Look for text field controls", "Inspect"},
		{"309", "Inspect the window", "Inspect"},
	} {
		t.Run(test.seq, func(t *testing.T) {
			for _, width := range []int{24, 60, 112} {
				call := desktopTraceFact("real", "dtw", "exec", "desktop-world", map[string]any{"app": "md.obsidian", "source": scripts[test.seq]})
				events := desktopTracePlacement(t, call, desktopTraceResult(call, "completed", desktopTraceResultJSON), test.seq != "309", "")
				for _, replay := range []bool{false, true} {
					m, b := desktopTraceModel(t, events, width, replay)
					rows := b.Render(m.blockRenderContext(width))
					action := test.action
					if width == 24 && test.seq != "158" {
						action = test.compact
					}
					if len(rows) != 2 || rows[0].Plain != "• Using computer" || !strings.HasPrefix(rows[1].Plain, "  └ "+action) ||
						!strings.Contains(rows[1].Plain, " in md.") || displayColumns(rows[1].Plain) > width {
						t.Fatalf("width=%d replay=%t: %s", width, replay, joinRenderedPlain(rows))
					}
				}
			}
		})
	}
}

func desktopWorldAsyncEvents(t *testing.T) []*session.Event {
	t.Helper()
	scripts := desktopWorldSessionScripts(t)
	direct := func(id, sequence string) *session.EventTool {
		return desktopTraceFact(id, "dtw", "exec", "desktop-world", map[string]any{"app": "Obsidian", "source": scripts[sequence]})
	}
	source := "const note = await Read({path:'fixture.md'}); return note.title;"
	root := &session.EventTool{ID: "current", Name: "js_exec", Status: "pending", Input: map[string]any{"code": source}}
	child := direct("nested", "128")
	child.Parent = &session.EventParentTool{Kind: "code_mode", CallID: root.ID, Name: root.Name}
	mixed := &session.EventTool{ID: "unrelated-child", Name: "Read", Status: "pending", Input: map[string]any{"path": "fixture.md"}, Parent: child.Parent}
	first, repeated, last := direct("first", "309"), direct("repeat", "309"), direct("last", "113")
	independent := &session.EventTool{ID: "independent", Name: "RunCommand", Status: "pending", Input: map[string]any{"command": "echo independent"}}
	final := desktopTraceResult(root, "failed", `{"error":"ReferenceError: note.title is unavailable"}`)
	final.Meta = map[string]any{"code_mode_script_completion": "failed"}
	return []*session.Event{
		desktopTraceToolStart(first), desktopTraceResult(first, "completed", desktopTraceResultJSON),
		desktopTraceToolStart(repeated), desktopTraceResult(repeated, "completed", desktopTraceResultJSON),
		desktopTraceToolStart(root), desktopTraceToolStart(child),
		desktopTraceResult(child, "completed", desktopTraceResultJSON),
		desktopTraceToolStart(independent),
		desktopTraceToolStart(last), desktopTraceResult(last, "completed", desktopTraceResultJSON),
		desktopTraceToolStart(mixed), final,
	}
}

func TestDesktopWorldTraceAsyncReclassificationAndReplay(t *testing.T) {
	events := desktopWorldAsyncEvents(t)
	for _, width := range []int{24, 112} {
		m := newACPEventstreamRegressionModel(t, width, 48)
		m.currentSessionID = "desktop-fixture"
		for index, env := range desktopTraceEnvelopes(events, m.currentSessionID) {
			m.Update(env)
			if env.Position == nil || env.Position.Durable == nil {
				continue
			}
			b := requireMainACPTurnBlockForTest(t, m)
			rows := b.Render(m.blockRenderContext(width))
			text := joinRenderedPlain(rows)
			seq := env.Position.Durable.Seq
			switch seq {
			case 4:
				if len(rows) != 2 || strings.Count(text, "Inspect") != 1 {
					t.Fatalf("duplicate observation survived at %d:\n%s", index, text)
				}
			case 5:
				if strings.Count(text, "• js_exec") != 1 || !strings.Contains(text, "• Used computer") {
					t.Fatalf("zero observed children should use the standard row:\n%s", text)
				}
			case 6, 7:
				if len(rows) != 3 || !strings.Contains(text, "• Using computer") || !strings.Contains(text, "Click") || strings.Contains(text, "js_exec") || strings.Count(text, "└") != 1 {
					t.Fatalf("trusted child should extend the group:\n%s", text)
				}
			case 10:
				if strings.Count(text, "• Used computer") != 1 || strings.Count(text, "• Using computer") != 1 {
					t.Fatalf("the independent message should close the first group:\n%s", text)
				}
			case 11, 12:
				script := strings.Index(text, "• js_exec")
				message := strings.Index(text, "echo independent")
				tail := strings.Index(text, "• Using computer")
				if strings.Count(text, "• js_exec") != 1 || strings.Count(text, "Click") != 1 || strings.Count(text, "• Used computer") != 1 ||
					script < 0 || message <= script || tail <= message || !strings.Contains(text, "Read") {
					t.Fatalf("mixed script must change at its original position:\n%s", text)
				}
				if width == 112 && !strings.Contains(text, events[4].Tool.Input["code"].(string)) {
					t.Fatalf("script must show its invocation source:\n%s", text)
				}
				if seq == 12 && !strings.Contains(text, "ReferenceError") {
					t.Fatalf("outer error disappeared:\n%s", text)
				}
			}
		}
		replayed, b := desktopTraceModel(t, events, width, true)
		replayText := joinRenderedPlain(b.Render(replayed.blockRenderContext(width)))
		liveText := joinRenderedPlain(requireMainACPTurnBlockForTest(t, m).Render(m.blockRenderContext(width)))
		if replayText != liveText {
			t.Fatalf("width=%d replay differs from live:\n%s\n%s", width, liveText, replayText)
		}
	}
}

func TestDesktopWorldTracePreservesOuterFailureAfterPureChildren(t *testing.T) {
	events := desktopWorldAsyncEvents(t)
	events = append(events[:7:7], events[len(events)-1])
	for _, replay := range []bool{false, true} {
		m, b := desktopTraceModel(t, events, 112, replay)
		text := joinRenderedPlain(b.Render(m.blockRenderContext(112)))
		if strings.Count(text, "• js_exec") != 1 || strings.Count(text, "Click") != 1 ||
			!strings.Contains(text, "ReferenceError") || !strings.Contains(text, "• Used computer") {
			t.Fatalf("replay=%v lost the outer failure:\n%s", replay, text)
		}
	}
}

func TestDesktopWorldTraceClosesOnNextUserMessage(t *testing.T) {
	call := desktopTraceFact("read", "dtw", "exec", "desktop-world", map[string]any{"app": "Obsidian", "source": "return await app.look();"})
	start := desktopTraceToolStart(call)
	result := desktopTraceResult(call, "completed", desktopTraceResultJSON)
	later := model.NewMessage(model.RoleUser, model.NewTextPart("Next request"))
	for _, replay := range []bool{false, true} {
		m, b := desktopTraceModel(t, []*session.Event{start, result}, 80, replay)
		b.(*MainACPTurnBlock).SetStatus("completed", "", "", start.Time)
		if got := joinRenderedPlain(b.Render(m.blockRenderContext(80))); !strings.Contains(got, "• Using computer") {
			t.Fatalf("completion changed the activity label: %s", got)
		}
		m, b = desktopTraceModel(t, []*session.Event{start, result, {Type: session.EventTypeUser, Message: &later}}, 80, replay)
		if got := joinRenderedPlain(b.Render(m.blockRenderContext(80))); !strings.Contains(got, "• Used computer") {
			t.Fatalf("replay=%v later user message did not close group: %s", replay, got)
		}
	}
}

func TestDesktopWorldTraceKeepsEarlierFailureVisible(t *testing.T) {
	events := desktopTraceEvents()
	events = append(events[4:6:6], append(events[:4:4], events[6:]...)...)
	for _, replay := range []bool{false, true} {
		m, b := desktopTraceModel(t, events, 112, replay)
		ctx := m.blockRenderContext(112)
		text := joinRenderedPlain(b.Render(ctx))
		if !strings.Contains(text, "  └ No apps in scope") || !strings.Contains(text, "fixture permission denied") ||
			strings.Count(text, "└") != 1 || strings.Count(text, "Click") != 1 {
			t.Fatalf("replay=%v earlier error lost its diagnostic or displaced a recent summary:\n%s", replay, text)
		}
		clickScriptToken(t, m, b, ctx, acpToolPanelClickToken("failed"))
		if got := joinRenderedPlain(b.Render(ctx)); !strings.Contains(got, "Error details") {
			t.Fatalf("replay=%v earlier error cannot disclose its result:\n%s", replay, got)
		}
	}
}
