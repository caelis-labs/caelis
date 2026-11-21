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
	for i, original := range events {
		ev := session.CloneEvent(original)
		ev.SessionID = m.currentSessionID
		ev.ID, ev.Seq = fmt.Sprintf("desktop-event-%d", i), uint64(i+1)
		if ev.Visibility == "" {
			ev.Visibility = session.VisibilityCanonical
			if ev.Tool != nil && ev.Tool.Parent != nil {
				ev.Visibility = session.VisibilityMirror
			}
		}
		base := projection.EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: ev.SessionID}, ev, projection.SessionEventTransport{TurnID: "turn"})
		for _, envelope := range projection.ProjectSessionEventEnvelope(base, ev) {
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
	}
	if replay {
		m.beginSessionHistory(sessionViewStartMsg{state: appserver.SessionState{SessionID: m.currentSessionID}})
		m.sessionHistory.model.handleTranscriptEventsMsg(TranscriptEventsMsg{Events: projectResumeReplayEvents(history), ReconnectReplay: true})
		m.commitSessionHistory()
	}
	b := requireMainACPTurnBlockForTest(t, m)
	return m, b
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
				for _, hidden := range []string{"unknown", "coverage", "RESULT_DETAIL", "OUTER_SOURCE", "await app", "approved", "permission denied"} {
					if strings.Contains(collapsed, hidden) {
						t.Fatalf("collapsed trace exposes detail %q:\n%s", hidden, collapsed)
					}
				}
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
				for _, want := range []string{"App1", "App2", "App5", "+2 more"} {
					if !strings.Contains(got, want) {
						t.Fatalf("replay=%v missing %s in running group:\n%s", replay, want, got)
					}
				}
				if strings.Contains(got, "App3") || strings.Contains(got, "App4") {
					t.Fatalf("running group exceeds three default calls:\n%s", got)
				}
				label, key := "Using computer", "computer:1"
				if nested {
					label, key = "Running script", "tools:script"
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
				if !strings.Contains(expanded, "App3") || !strings.Contains(expanded, "App4") || strings.Contains(expanded, "+2 more") {
					t.Fatalf("group disclosure lost calls:\n%s", expanded)
				}
			}
		})
	}
}

func TestDesktopWorldTraceTerminalEvidence(t *testing.T) {
	// S01 uses a native selector and a key array, with a read between the inputs.
	const beforeKey = `await app.look({role:'text_field',text:'Search inventory'});
await app.click({selector:{role:'text_field',text:'Search inventory'}});
await app.look({role:'text_field',text:'Search inventory'});
`
	const key = `await app.keypress({keys:['META','A']});`
	const branch = `await app.look({text:'Web note'});
if (matches.length === 1) { await app.click({target:matches[0].id}); } else { await app.capture({}); }`
	const literalChord = `const chord = ['META','A'];
await app.look();
if (matches.length !== 1) return null;
await app.click({target:matches[0].id});
await app.keypress({keys:chord});
await app.typeText({text:'hidden'});`
	for _, test := range []struct {
		name, source, status, result string
		wide, narrow, forbidden      string
	}{
		{"complete", beforeKey + key, "completed", `{"execution_id":"ok","effects":{"dispatch":{"dispatched":2}}}`,
			"Click “Search inventory”, press META+A", "Click, press", "Inspect"},
		{"interrupted", beforeKey + key, "failed", `{"execution_id":"interrupted","effects":{"dispatch":{"dispatched":1}},"error":{"code":"execution_interrupted"}}`,
			"Script failed", "Failed", "press"},
		{"unknown dispatch", beforeKey + key, "completed", `{"execution_id":"uncertain","effects":{"dispatch":{"dispatched":1,"unknown":1}}}`,
			"Run a script", "Run script", "press"},
		{"early return", beforeKey + "return 42;\n" + key, "completed", `{"execution_id":"returned","effects":{"dispatch":{"dispatched":1}},"result":42}`,
			"Click “Search inventory”", "Click", "press"},
		{"refused input caught by script", beforeKey + "try { " + key + " } catch (e) { return e.code; }", "completed",
			`{"execution_id":"caught","effects":{"dispatch":{"dispatched":1,"not_dispatched":1}},"result":"input_not_ready"}`,
			"Run a script", "Run script", "press"},
		{"missing dispatch evidence", beforeKey + key, "completed", `{"execution_id":"no-effects"}`,
			"Run a script", "Run script", "press"},
		{"conditional readonly fallback", branch, "completed", `{"execution_id":"read-fallback","status":"completed"}`,
			"May take a screenshot", "May capture", "click"},
		{"conditional input", branch, "completed", `{"execution_id":"branch-click","effects":{"dispatch":{"dispatched":1}}}`,
			"May click", "May click", "capture"},
		{"literal chord", literalChord, "completed", `{"execution_id":"chord","effects":{"dispatch":{"dispatched":3}}}`,
			"Click and press META+A", "Click, press", "type"},
		{"repeated input totals cannot prove later sites", `for (const row of rows) await app.click(row); await app.keypress("ESC");`,
			"completed", `{"execution_id":"loop","effects":{"dispatch":{"dispatched":2}}}`,
			"May click and may press ESC", "May click", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, nested := range []bool{false, true} {
				fact := desktopTraceFact("desktop", "dtw", "exec", "desktop-world", map[string]any{"app": "fixture.desktop", "source": test.source})
				final := desktopTraceResult(fact, test.status, test.result)
				events := desktopTracePlacement(t, fact, final, nested, "")
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
						if width == 112 && test.name == "complete" {
							ctx := m.blockRenderContext(width)
							clickScriptToken(t, m, b, ctx, acpToolPanelClickToken(fact.ID))
							open := joinRenderedPlain(b.Render(ctx))
							if !strings.Contains(open, "Result") || !strings.Contains(open, `"execution_id"`) || !strings.Contains(open, "app.keypress") {
								t.Fatalf("nested=%t replay=%t lost disclosed arguments or result:\n%s", nested, replay, open)
							}
						}
					}
				}
			}
		})
	}
}

// Native Code Mode records carry the result without EventTool.Output on the
// child. The parent may return the exact same structured reply.
func desktopTracePlacement(t *testing.T, call *session.EventTool, final *session.Event, nested bool, code string) []*session.Event {
	t.Helper()
	start := &session.Event{Type: session.EventTypeToolCall, Tool: call}
	if !nested {
		return []*session.Event{start, final}
	}
	if code == "" {
		args, err := json.Marshal(call.Input)
		if err != nil {
			t.Fatal(err)
		}
		code = "return await " + call.Name + "(" + string(args) + ");"
	}
	root := &session.EventTool{ID: "outer", Name: "js_exec", Status: "pending", Input: map[string]any{"code": code}}
	call.Parent = &session.EventParentTool{Kind: "code_mode", CallID: root.ID, Name: root.Name}
	final.Tool.Parent = call.Parent
	raw, err := json.Marshal(final.Tool.Output)
	if err != nil {
		t.Fatal(err)
	}
	rootFinal := desktopTraceResult(root, "completed", string(raw))
	final.Tool.Output = nil
	return []*session.Event{{Type: session.EventTypeToolCall, Tool: root}, start, final, rootFinal}
}

func TestDesktopWorldAppsEmptyScopedResult(t *testing.T) {
	for _, test := range []struct {
		name, code, output, want string
		nested                   bool
	}{
		{"direct", "", `{"apps":[],"coverage":{"complete":false},"next":null}`, "No apps in scope", false},
		{"nested returned", "", `{"apps":[],"coverage":{"complete":false},"next":null}`, "No apps in scope", true},
		{"another page", "", `{"apps":[],"coverage":{"complete":false},"next":"cursor"}`, "Find apps", true},
		{"different arguments", `return await dtw__apps({"query":"Not Chrome"});`, `{"apps":[],"next":null}`, "Find apps", true},
		{"unrelated parent return", `await dtw__apps({"query":"Chrome"}); return {"apps":[]};`, `{"apps":[],"next":null}`, "Find apps", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fact := desktopTraceFact("apps", "dtw", "apps", "desktop-world", map[string]any{"query": "Chrome"})
			events := desktopTracePlacement(t, fact, desktopTraceResult(fact, "completed", test.output), test.nested, test.code)
			for _, replay := range []bool{false, true} {
				m, b := desktopTraceModel(t, events, 112, replay)
				got := joinRenderedPlain(b.Render(m.blockRenderContext(112)))
				if !strings.Contains(got, test.want) || !strings.Contains(got, "Chrome") || strings.Contains(got, "coverage") {
					t.Fatalf("replay=%t want %q and search query:\n%s", replay, test.want, got)
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
		if strings.Count(text, "Used computer") != 2 || strings.Count(text, "Ran script") != 1 {
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
		if !strings.Contains(text, "dtw__exec") || strings.Contains(text, "Using computer") || strings.Contains(text, "Peer · Click") {
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
