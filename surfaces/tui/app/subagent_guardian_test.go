package tuiapp

import (
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

func TestTeamGuardianDraftsBothBindingsAndAppliesOnce(t *testing.T) {
	m, service := teamCellTestModel(t)
	selectSubagentTestRow(t, m, "handle:guardian")
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyLeft))
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyDown))
	if row := m.currentSubagentRow(); row.handle != agentbinding.HandleGuardianScreen || !row.reset {
		t.Fatalf("down at the end of the review choices did not enter the classifier: %+v", row)
	}
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyDown))
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyDown))
	if row := m.currentSubagentRow(); row.binding.Effort != "high" {
		t.Fatalf("switching panes lost review draft: %+v", row)
	}
	if len(service.guardianBindings) != 0 || service.bindRequest.Handle != "" {
		t.Fatal("moving through Guardian applied a draft")
	}
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyDown))
	cmd := m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("Guardian apply did not dispatch")
	}
	m.Update(cmd())
	got := service.guardianBindings
	if len(got) != 2 || got[0] != (agentbinding.Binding{Handle: agentbinding.HandleGuardianScreen, ProfileID: "provider:jev", Effort: "none"}) ||
		got[1] != (agentbinding.Binding{Handle: agentbinding.HandleGuardian, ProfileID: "provider:sol", Effort: "high"}) {
		t.Fatalf("wrong paired binding request: %+v", got)
	}
	if service.bindRequest.Handle != "" || service.reset != "" || m.subagentOverlay.page != subagentPageMain {
		t.Fatal("Guardian submitted an independent mutation or failed to return to the overview")
	}
}

func TestTeamGuardianSelectorMouseGeometryAndNarrowRendering(t *testing.T) {
	for _, width := range []int{35, 60, 100, 120} {
		m, service := teamCellTestModel(t)
		m.width = width
		selectSubagentTestRow(t, m, "handle:guardian")
		m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
		frame := ansi.Strip(m.renderSubagentOverlay())
		review := strings.Index(frame, "Review model")
		classifier := strings.Index(frame, "Classifier")
		if review < 0 || classifier <= review || !strings.Contains(frame, "Jev") ||
			!strings.Contains(frame[review:classifier], "\n") || strings.Contains(frame, "tab") {
			t.Fatalf("width %d did not show stacked sections with arrow navigation:\n%s", width, frame)
		}
		if cmd := clickTeamText(t, m, "Jev", "Jev"); cmd != nil {
			t.Fatal("mouse selection applied Guardian without confirmation")
		}
		if row := m.currentSubagentRow(); !m.subagentOverlay.classifierFocused || row.binding.ProfileID != "provider:jev" {
			t.Fatalf("width %d missed classifier row: %+v", width, row)
		}
		cmd := m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
		if cmd == nil {
			t.Fatal("missing classifier binding command")
		}
		cmd()
		if service.bindRequest.Handle != agentbinding.HandleGuardianScreen || len(service.guardianBindings) != 0 {
			t.Fatalf("width %d rewrote review model: %+v", width, service.bindRequest)
		}
		for _, line := range strings.Split(frame, "\n") {
			if displayColumns(line) > width {
				t.Fatalf("width %d overflow: %q", width, line)
			}
		}
	}
}

func TestTeamGuardianWithoutConnectedClassifier(t *testing.T) {
	m, service := newSubagentOverlayTestModel(t)
	selectSubagentTestRow(t, m, "handle:guardian")
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	frame := ansi.Strip(m.renderSubagentOverlay())
	if strings.Contains(frame, "Classifier") || strings.Contains(frame, "Disabled") {
		t.Fatalf("picker exposed a disconnected classifier:\n%s", frame)
	}
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyDown))
	if row := m.currentSubagentRow(); row.handle != agentbinding.HandleGuardian || !row.reset {
		t.Fatalf("arrow navigation did not stay in the review models: %+v", row)
	}
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEscape))
	if service.bindRequest.Handle != "" || service.reset != "" || len(service.guardianBindings) != 0 {
		t.Fatal("cancel changed Guardian configuration")
	}
}

func TestTeamGuardianFilterDoesNotChooseInactiveDraft(t *testing.T) {
	for _, query := range []string{"alternative-review", "no review matches"} {
		t.Run(query, func(t *testing.T) {
			m, service := teamCellTestModel(t)
			alt := testProviderModelProfile()
			alt.ID, alt.DisplayName = "provider:alt-review", "alternative-review"
			m.subagentOverlay.status.Targets = append(m.subagentOverlay.status.Targets, alt)
			selectSubagentTestRow(t, m, "handle:guardian")
			m.Update(subagentSpecialKey(tea.KeyEnter))
			m.Update(tea.PasteMsg{Content: query})
			if cmd := clickTeamText(t, m, "Jev", "Jev"); cmd != nil {
				t.Fatal("classifier selection submitted before Enter")
			}
			if m.subagentOverlay.other.query != "" || m.subagentOverlay.other.draft.ProfileID != "provider:sol" {
				t.Fatalf("inactive section lost its original draft: %+v", m.subagentOverlay.other)
			}
			_, cmd := m.Update(subagentSpecialKey(tea.KeyEnter))
			if cmd == nil {
				t.Fatal("Enter did not apply the classifier")
			}
			m.Update(cmd())
			if got := service.bindRequest; got.Handle != agentbinding.HandleGuardianScreen || got.ProfileID != "provider:jev" || len(service.guardianBindings) != 0 {
				t.Fatalf("expected only the classifier write, got single=%+v pair=%+v", got, service.guardianBindings)
			}
		})
	}
}
