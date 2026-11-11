package tuiapp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/projection"
)

// Optional presentation replay of frozen native receipts. No model, tool or
// desktop action is executed, and the original evidence is never rewritten.
func TestCodeModeFrozenNativeReplay(t *testing.T) {
	input, artifacts := os.Getenv("CAELIS_TUI_CODE_MODE_EVENTS"), os.Getenv("CAELIS_TUI_CODE_MODE_ARTIFACTS")
	if input == "" || artifacts == "" {
		t.Skip("requires original canonical events and task-owned artifact directory")
	}
	file, err := os.Open(input)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var events []*session.Event
	scan := bufio.NewScanner(file)
	scan.Buffer(make([]byte, 4096), 32<<20)
	for scan.Scan() {
		var event session.Event
		if err := json.Unmarshal(scan.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, &event)
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(artifacts, 0700); err != nil {
		t.Fatal(err)
	}
	for _, width := range []int{120, 60, 36} {
		m := NewModel(Config{NoColor: false, NoAnimation: true})
		m.Update(tea.WindowSizeMsg{Width: width, Height: 35})
		for _, event := range events {
			base := projection.EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: event.SessionID}, event, projection.SessionEventTransport{TurnID: "frozen-native-replay"})
			for _, env := range projection.ProjectSessionEventEnvelope(base, event) {
				m = applyACPEnvelopeForTest(t, m, env)
			}
		}
		save := func(state string) {
			t.Helper()
			m.markViewportStructureDirty()
			m.syncViewportContent()
			name := filepath.Join(artifacts, fmt.Sprintf("%d-%s", width, state))
			if err := os.WriteFile(name+".rows.txt", []byte(strings.Join(m.viewportPlainLines, "\n")), 0600); err != nil {
				t.Fatal(err)
			}
			frame := m.View().Content
			if err := os.WriteFile(name+".ansi", []byte(frame), 0600); err != nil {
				t.Fatal(err)
			}
			updates := renderFullscreenFramesForTest(t, width, 35, frame)
			assertPhysicalFullscreenFrame(t, width, 35, frame, updates)
		}
		save("collapsed")
		for step := 1; step <= 2; step++ {
			var targets []struct {
				block Block
				token string
			}
			seen := map[string]bool{}
			for _, block := range m.doc.Blocks() {
				for _, row := range block.Render(m.blockRenderContext(width)) {
					if step > 1 && strings.HasPrefix(row.ClickToken, "acp_exploration_stable:tools:") {
						continue
					}
					key := block.BlockID() + "/" + row.ClickToken
					if row.ClickToken != "" && !seen[key] {
						targets = append(targets, struct {
							block Block
							token string
						}{block, row.ClickToken})
						seen[key] = true
					}
				}
			}
			for _, target := range targets {
				m.tryToggleFoldToken(target.block.BlockID(), target.token)
			}
			save(fmt.Sprintf("disclosure-%d", step))
		}
	}
}
