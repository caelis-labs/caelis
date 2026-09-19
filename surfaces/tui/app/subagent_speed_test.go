package tuiapp

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestTeamFastPickerKeepsEffortAndSubmitsTypedBinding(t *testing.T) {
	for _, profileID := range []string{"provider:sol", "acp:claude:opus"} {
		t.Run(profileID, func(t *testing.T) {
			m, service := newSubagentOverlayTestModel(t)
			for i, p := range m.subagentOverlay.status.Targets {
				if p.ID == profileID {
					m.subagentOverlay.status.Targets[i].Speed = modelprofile.SpeedCapability{DefaultSpeed: "fast", Choices: []modelprofile.SpeedChoice{{Canonical: "standard", WireValue: "default"}, {Canonical: "fast", WireValue: "priority"}}}
				}
			}
			selectSubagentTestRow(t, m, "handle:orbit")
			m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
			selectSubagentTestRow(t, m, "binding:"+profileID)
			effort := m.currentSubagentRow().binding.Effort
			if m.currentSubagentRow().binding.Speed != "" {
				t.Fatal("displaying a backend default changed an inherited selection")
			}
			m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyTab))
			frame := ansi.Strip(m.View().Content)
			if !strings.Contains(frame, "Fast on") || !strings.Contains(frame, "tab") {
				t.Fatalf("missing Fast control:\n%s", frame)
			}
			m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyLeft))
			frame = ansi.Strip(m.View().Content)
			if !strings.Contains(frame, "Fast off") || m.currentSubagentRow().binding.Effort != effort {
				t.Fatalf("Fast altered effort:\n%s", frame)
			}
			t.Log(frame)
			cmd := m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
			if cmd == nil {
				t.Fatal("missing typed binding command")
			}
			cmd()
			if service.bindRequest.ProfileID != profileID || service.bindRequest.Speed != "standard" || service.bindRequest.Effort != effort {
				t.Fatalf("binding: %#v", service.bindRequest)
			}
		})
	}
}

func TestTeamUnsupportedSpeedHasNoFastControl(t *testing.T) {
	m, _ := newSubagentOverlayTestModel(t)
	selectSubagentTestRow(t, m, "handle:orbit")
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	if frame := ansi.Strip(m.View().Content); strings.Contains(frame, "Fast ") {
		t.Fatalf("unsupported speed rendered as Off:\n%s", frame)
	}
}
