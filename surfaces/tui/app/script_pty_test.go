package tuiapp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/projection"
)

// This opt-in executable runs the production TUI in a real terminal. F6 only
// advances synthetic canonical input; all rendering, resize and mouse input
// uses the real Model. No Runtime, model, file tool or desktop action executes.
func TestScriptGroupPTY(t *testing.T) {
	dir := os.Getenv("CAELIS_SCRIPT_PTY")
	if dir == "" {
		t.Skip("requires isolated PTY and task-owned evidence directory")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := Config{AppName: "CAELIS candidate", Version: os.Getenv("CAELIS_CANDIDATE_HEAD"), Workspace: "/tmp/owned-script-fixture", ModelAlias: "fixture/no-model"}
	m := NewModel(cfg)
	m.beginLiveTurn(SubmissionModeDefault, false, time.Now())
	driver := &scriptPTYDriver{Model: m, dir: dir, stages: scriptPTYStages()}
	if _, err := tea.NewProgram(driver, tea.WithoutSignalHandler()).Run(); err != nil {
		t.Fatal(err)
	}
}

type scriptPTYDriver struct {
	*Model
	dir    string
	stage  int
	stages [][]*session.Event
}

func (d *scriptPTYDriver) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var commands []tea.Cmd
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "f10" {
		return d, tea.Quit
	}
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "f6" {
		if d.stage < len(d.stages) {
			for _, event := range d.stages[d.stage] {
				base := projection.EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: "visual-fixture"}, event, projection.SessionEventTransport{TurnID: "visual-turn"})
				for _, env := range projection.ProjectSessionEventEnvelope(base, event) {
					next, cmd := d.handleACPEventEnvelope(env)
					d.Model = next.(*Model)
					commands = append(commands, cmd)
				}
			}
			d.stage++
		}
		d.snapshot("stage")
		return d, tea.Batch(commands...)
	}
	next, cmd := d.Model.Update(msg)
	d.Model = next.(*Model)
	switch msg.(type) {
	case tea.MouseClickMsg, tea.MouseReleaseMsg, tea.WindowSizeMsg, tea.KeyPressMsg:
		d.snapshot("input")
	}
	return d, cmd
}
func (d *scriptPTYDriver) snapshot(cause string) {
	d.markViewportStructureDirty()
	d.syncViewportContent()
	data, _ := json.MarshalIndent(map[string]any{"stage": d.stage, "cause": cause, "viewport": d.viewportPlainLines, "source_head": os.Getenv("CAELIS_CANDIDATE_HEAD"), "drawer": d.activeTextDrawer()}, "", "  ")
	_ = os.WriteFile(filepath.Join(d.dir, "state.json"), data, 0600)
	if cause == "stage" {
		_ = os.WriteFile(filepath.Join(d.dir, fmt.Sprintf("stage-%02d.json", d.stage)), data, 0600)
	}
}
func scriptPTYStages() [][]*session.Event {
	tool := func(id, name, parent, status, path, output string) *session.Event {
		typ := session.EventTypeToolCall
		if status != "started" {
			typ = session.EventTypeToolResult
		}
		entry := &session.EventTool{ID: id, Name: name, Status: status, Input: map[string]any{"path": path}}
		if name == "Grep" {
			entry.Input = map[string]any{"path": path, "pattern": "contract"}
		}
		if name == "js_exec" {
			entry.Input = map[string]any{"code": "SOURCE_MUST_STAY_HIDDEN"}
		}
		if name == "desktop_world__exec" {
			entry.Input = map[string]any{"source": "return await app.look();"}
		}
		if parent != "" {
			entry.Parent = &session.EventParentTool{CallID: parent, Name: "js_exec"}
		}
		if output != "" {
			entry.Content = []session.EventToolContent{{Type: "content", Text: output}}
		}
		event := &session.Event{SessionID: "visual-fixture", Type: typ, Tool: entry}
		if name == "js_exec" && status != "started" {
			completion := "completed"
			if id == "s3" {
				completion = "failed"
			}
			event.Meta = map[string]any{"code_mode_script_completion": completion}
		}
		return event
	}
	narrative := &session.Event{SessionID: "visual-fixture", Type: session.EventTypeAssistant, Protocol: &session.EventProtocol{Method: session.ProtocolMethodSessionUpdate, Update: &session.ProtocolUpdate{SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage), Content: session.ProtocolTextContent("Now **check the next file**. This explanation stays outside both scripts.")}}}
	return [][]*session.Event{
		{tool("native1", "Read", "", "completed", "README.md", ""), tool("native2", "Grep", "", "completed", "docs", "")},
		{tool("s1", "js_exec", "", "started", "", "")},
		{tool("c1", "Read", "s1", "started", "notes/first.txt", "")},
		{tool("c1", "Read", "s1", "completed", "notes/first.txt", "PRIVATE_CHILD_CONTENT"), tool("c1b", "Read", "s1", "completed", "notes/second.txt", "PRIVATE_SECOND_CONTENT"), tool("c2", "desktop_world__exec", "s1", "started", "", "")},
		{tool("c2", "desktop_world__exec", "s1", "completed", "", ""), tool("s1", "js_exec", "", "completed", "", `{"PRIVATE_FINAL_SUMMARY":"two observations"}`)},
		{narrative, tool("s2", "js_exec", "", "started", "", ""), tool("c3", "Read", "s2", "started", "notes/missing.txt", "")},
		{tool("c3", "Read", "s2", "failed", "notes/missing.txt", "not_found: notes/missing.txt does not exist"), tool("c4", "Read", "s2", "started", "go.mod", "")},
		{tool("c4", "Read", "s2", "completed", "go.mod", "PRIVATE_RECOVERY"), tool("s2", "js_exec", "", "failed", "", `{"PRIVATE_CAUGHT_SUMMARY":"recovered"}`)},
		{tool("s3", "js_exec", "", "started", "", "")},
		{tool("s3", "js_exec", "", "failed", "", "probe.js:3: TypeError: missing value\n at probe.js:3:7")},
		{tool("s4", "js_exec", "", "started", "", "")},
		{tool("s4", "js_exec", "", "completed", "", "42")},
	}
}
