package tuiapp

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/charmbracelet/x/ansi"
)

func TestTeamPickerPreservesExplicitSpeedForCurrentProfile(t *testing.T) {
	m, service := teamCellTestModel(t)
	for i := range m.subagentOverlay.status.Handles {
		item := &m.subagentOverlay.status.Handles[i]
		if item.Definition.Handle == agentbinding.HandleOrbit {
			item.Binding.Speed = "fast"
		}
	}
	selectSubagentTestRow(t, m, "handle:orbit")
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyRight))
	frame := ansi.Strip(m.renderSubagentOverlay())
	if strings.Contains(strings.ToLower(frame), "fast") || strings.Contains(frame, "tab") {
		t.Fatalf("ordinary picker displayed speed or field navigation:\n%s", frame)
	}
	cmd := m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("missing binding command")
	}
	cmd()
	if got := service.bindRequest; got.Speed != "fast" || got.Effort != "xhigh" {
		t.Fatalf("saved binding = %+v", got)
	}
}
