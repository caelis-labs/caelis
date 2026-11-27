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
	if cmd := m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter)); cmd != nil {
		t.Fatal("Enter on Guardian model applied before Classifier selection")
	}
	if m.subagentOverlay.page != subagentPageClassifier {
		t.Fatalf("page = %v, want Classifier", m.subagentOverlay.page)
	}
	if row := m.currentSubagentRow(); row.handle != agentbinding.HandleGuardianScreen || !row.reset {
		t.Fatalf("Classifier page did not start on Disabled: %+v", row)
	}
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyDown))
	if row := m.currentSubagentRow(); row.binding.ProfileID != "provider:jev" {
		t.Fatalf("Down did not select Jev: %+v", row)
	}
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEscape))
	if m.subagentOverlay.page != subagentPageBinding || m.currentSubagentRow().binding.Effort != "high" {
		t.Fatalf("Esc from Classifier lost binding draft: page=%v row=%+v", m.subagentOverlay.page, m.currentSubagentRow())
	}
	if len(service.guardianBindings) != 0 || service.bindRequest.Handle != "" {
		t.Fatal("navigating Guardian applied a draft")
	}
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyDown))
	cmd := m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("Guardian apply did not dispatch")
	}
	m.Update(cmd())
	got := service.guardianBindings
	if len(got) != 2 || got[0] != (agentbinding.Binding{Handle: agentbinding.HandleGuardian, ProfileID: "provider:sol", Effort: "high"}) ||
		got[1] != (agentbinding.Binding{Handle: agentbinding.HandleGuardianScreen, ProfileID: "provider:jev", Effort: "none"}) {
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
		m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
		frame := ansi.Strip(m.renderSubagentOverlay())
		if !strings.Contains(frame, "Classifier") || !strings.Contains(frame, "Disabled") || !strings.Contains(frame, "Jev") {
			t.Fatalf("width %d Classifier page missing expected items:\n%s", width, frame)
		}
		for _, line := range strings.Split(frame, "\n") {
			if displayColumns(line) > width {
				t.Fatalf("width %d overflow: %q", width, line)
			}
		}
		cmd := clickTeamText(t, m, "Jev", "Jev")
		if cmd == nil {
			t.Fatal("clicking Jev on Classifier page did not apply")
		}
		cmd()
		if service.bindRequest.Handle != agentbinding.HandleGuardianScreen || len(service.guardianBindings) != 0 {
			t.Fatalf("width %d rewrote unchanged review model: %+v", width, service.bindRequest)
		}
	}
}

func TestTeamGuardianNoOpEnterSkipsMutation(t *testing.T) {
	m, service := teamCellTestModel(t)
	selectSubagentTestRow(t, m, "handle:guardian")
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	if cmd := m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter)); cmd != nil {
		t.Fatal("unchanged Guardian + Classifier triggered a mutation")
	}
	if service.bindRequest.Handle != "" || len(service.guardianBindings) != 0 || m.subagentOverlay.page != subagentPageMain {
		t.Fatalf("no-op Enter mutated or did not return to overview: page=%v", m.subagentOverlay.page)
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
	if cmd := m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter)); cmd != nil {
		t.Fatal("unchanged Guardian without classifier triggered mutation")
	}
	if service.bindRequest.Handle != "" || service.reset != "" || len(service.guardianBindings) != 0 || m.subagentOverlay.page != subagentPageMain {
		t.Fatal("no-op Guardian changed configuration or did not return")
	}
}

func TestTeamGuardianClassifierShowsProviderOnDuplicateName(t *testing.T) {
	m, _ := teamCellTestModel(t)
	m.subagentOverlay.status.Targets = append(m.subagentOverlay.status.Targets,
		modelprofile.ModelProfile{
			ID: "provider:jev-magpie", DisplayName: "Jev", Judgment: true,
			Backend: modelprofile.Backend{Provider: &modelprofile.ProviderBackend{ModelConfigID: "magpie/jev"}},
			Effort:  modelprofile.EffortCapability{DefaultEffort: "none", Choices: []modelprofile.EffortChoice{{Canonical: "none"}}},
		},
		modelprofile.ModelProfile{
			ID: "provider:jev-codex", DisplayName: "Jev", Judgment: true,
			Backend: modelprofile.Backend{Provider: &modelprofile.ProviderBackend{ModelConfigID: "openai-codex/jev"}},
			Effort:  modelprofile.EffortCapability{DefaultEffort: "none", Choices: []modelprofile.EffortChoice{{Canonical: "none"}}},
		},
	)
	selectSubagentTestRow(t, m, "handle:guardian")
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	frame := ansi.Strip(m.renderSubagentOverlay())
	if !strings.Contains(frame, "magpie") || !strings.Contains(frame, "openai-codex") {
		t.Fatalf("Classifier page dropped duplicate-name provider disambiguator:\n%s", frame)
	}
}
