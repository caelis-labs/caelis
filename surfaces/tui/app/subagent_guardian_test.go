package tuiapp

import (
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/charmbracelet/x/ansi"
)

func guardianScreenTestProfile() modelprofile.ModelProfile {
	return modelprofile.ModelProfile{ID: "provider:jev", DisplayName: "Jev", Judgment: true,
		Backend: modelprofile.Backend{Provider: &modelprofile.ProviderBackend{ModelConfigID: "jev"}},
		Effort:  modelprofile.EffortCapability{DefaultEffort: "none", Choices: []modelprofile.EffortChoice{{Canonical: "none"}}}}
}

func TestTeamGuardianHasOneRowAndConditionalAuxiliarySelector(t *testing.T) {
	m, service := newSubagentOverlayTestModel(t)
	selectSubagentTestRow(t, m, "handle:guardian")
	if row := m.currentSubagentRow(); row.companion != nil {
		t.Fatal("unconnected classifier has a selector")
	}
	for _, row := range m.subagentMainRows() {
		if row.handle == agentbinding.HandleGuardianScreen {
			t.Fatal("screening is a separate team item")
		}
	}
	m.subagentOverlay.status.Targets = append(m.subagentOverlay.status.Targets, guardianScreenTestProfile())
	selectSubagentTestRow(t, m, "handle:guardian")
	frame := ansi.Strip(m.renderSubagentOverlay())
	if !strings.Contains(frame, "Classifier Disabled") || !strings.Contains(frame, "tab field") {
		t.Fatalf("missing paired selectors:\n%s", frame)
	}
	if strings.Contains(frame, "Guardian Screening") {
		t.Fatal("auxiliary rendered as a separate role")
	}
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyTab))
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	if m.subagentOverlay.bindingHandle != agentbinding.HandleGuardianScreen {
		t.Fatal("Tab did not select the auxiliary")
	}
	frame = ansi.Strip(m.renderSubagentOverlay())
	if !strings.Contains(frame, "Choose classifier") || !strings.Contains(frame, "Jev") || strings.Contains(frame, "gpt-5.6-sol") {
		t.Fatalf("incorrect classifier options:\n%s", frame)
	}
	selectSubagentTestRow(t, m, "binding:provider:jev")
	cmd := m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("missing classifier binding command")
	}
	m.Update(cmd())
	if service.bindRequest.Handle != agentbinding.HandleGuardianScreen || service.bindRequest.ProfileID != "provider:jev" {
		t.Fatalf("binding=%+v", service.bindRequest)
	}
	// Back at Guardian, the primary selector still opens the existing Agent picker.
	selectSubagentTestRow(t, m, "handle:guardian")
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyTab))
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	if m.subagentOverlay.bindingHandle != agentbinding.HandleGuardian {
		t.Fatal("primary selector changed its binding target")
	}
	if strings.Contains(ansi.Strip(m.renderSubagentOverlay()), "Jev") {
		t.Fatal("Agent picker contains a classifier")
	}
}

func TestTeamGuardianSelectorMouseGeometryAndNarrowRendering(t *testing.T) {
	for _, width := range []int{56, 100, 120} {
		for _, screen := range []bool{false, true} {
			m, service := newSubagentOverlayTestModel(t)
			m.width = width
			classifier := guardianScreenTestProfile()
			if width == 120 {
				classifier.DisplayName = "typesafe/jev-1.13.0"
			}
			m.subagentOverlay.status.Targets = append(m.subagentOverlay.status.Targets, classifier)
			for i := range m.subagentOverlay.status.Handles {
				item := &m.subagentOverlay.status.Handles[i]
				if item.Definition.Handle == agentbinding.HandleGuardianScreen {
					item.Profile = classifier
					item.Binding = agentbinding.Binding{Handle: agentbinding.HandleGuardianScreen, ProfileID: item.Profile.ID, Effort: "none"}
				}
			}
			selectSubagentTestRow(t, m, "handle:guardian")
			m.subagentOverlay.field = subagentFieldModel
			if screen {
				m.subagentOverlay.field = subagentFieldAuxiliary
			}
			frame := ansi.Strip(m.renderSubagentOverlay())
			for _, line := range strings.Split(frame, "\n") {
				if displayColumns(line) > width {
					t.Fatalf("overflow: %q", line)
				}
			}
			if !strings.Contains(frame, classifier.DisplayName) {
				t.Fatalf("auxiliary model missing:\n%s", frame)
			}
			guardianLine := lineContaining(strings.Split(frame, "\n"), "Guardian")
			orbitLine := lineContaining(strings.Split(frame, "\n"), "orbit")
			guardianModel := strings.Index(guardianLine, "openai")
			orbitModel := strings.Index(orbitLine, "openai")
			auxiliaryModel := strings.Index(guardianLine, classifier.DisplayName)
			if guardianModel < 0 || orbitModel < 0 || auxiliaryModel < 0 {
				t.Fatalf("missing model columns:\n%s", frame)
			}
			modelX := displayColumns(guardianLine[:guardianModel])
			if modelX != displayColumns(orbitLine[:orbitModel]) || strings.Contains(guardianLine, "Agent") || strings.Contains(guardianLine, "Screen") {
				t.Fatalf("Guardian column differs from other models:\n%s", frame)
			}
			if width == 120 && screen {
				t.Log(frame)
				if path := os.Getenv("CAELIS_GUARDIAN_UI_REPORT"); path != "" {
					if err := os.WriteFile(path, []byte(frame), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			geometry := m.subagentOverlay.geometry
			// Click the displayed model text, independently of the stored hit bounds.
			x := geometry.x + displayColumns(guardianLine[:auxiliaryModel])
			cells := geometry.cells[m.subagentOverlay.index]
			aux := cells[len(cells)-1]
			if x < aux.x || x >= aux.x+aux.width {
				t.Fatalf("classifier hit bounds=%+v, rendered start=%d", aux, x)
			}
			want := agentbinding.HandleGuardianScreen
			if !screen {
				x = geometry.x + modelX
				want = agentbinding.HandleGuardian
			}
			mouse := tea.Mouse{X: x, Y: geometry.rows[m.subagentOverlay.index], Button: tea.MouseLeft}
			m.handleSubagentOverlayMouse(tea.MouseClickMsg(mouse))
			m.handleSubagentOverlayMouse(tea.MouseReleaseMsg(mouse))
			if m.subagentOverlay.bindingHandle != want {
				t.Fatalf("width=%d screen=%v opened=%s", width, screen, m.subagentOverlay.bindingHandle)
			}
			selectSubagentTestRow(t, m, "binding:reset")
			cmd := m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
			if cmd == nil {
				t.Fatal("missing reset command")
			}
			cmd()
			if service.reset != want {
				t.Fatalf("reset changed the other selector: %s", service.reset)
			}
		}
	}
}

func TestTeamGuardianDisconnectHidesAuxiliaryWithoutChangingAgent(t *testing.T) {
	m, _ := newSubagentOverlayTestModel(t)
	m.subagentOverlay.status.Targets = append(m.subagentOverlay.status.Targets, guardianScreenTestProfile())
	selectSubagentTestRow(t, m, "handle:guardian")
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyTab))
	m.subagentOverlay.status.Targets = m.subagentOverlay.status.Targets[:2]
	m.refreshSubagentRows("handle:guardian")
	if m.currentSubagentRow().companion != nil {
		t.Fatal("disconnect retained the auxiliary control")
	}
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	if m.subagentOverlay.bindingHandle != agentbinding.HandleGuardian {
		t.Fatal("stale auxiliary focus changed the Agent picker")
	}
}
