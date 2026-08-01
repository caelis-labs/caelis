package promptview

import (
	"slices"
	"testing"

	controlstatus "github.com/caelis-labs/caelis/control/status"
)

func TestSharedCompactStatusProjection(t *testing.T) {
	t.Parallel()

	model := ModelDisplayFromStatus(controlstatus.StatusModel{
		Display:         "grouped/model",
		Provider:        "grouped-provider",
		ReasoningEffort: "high",
	})
	if got := model.Text(""); got != "grouped-provider/grouped/model [high]" {
		t.Fatalf("ModelDisplay.Text() = %q", got)
	}
	if got := FormatContextUsage(12600, 88000); got != "13k / 88k · 14%" {
		t.Fatalf("FormatContextUsage() = %q", got)
	}
}

func TestStatusDisplayTreatsLegacySandboxFallbackReasonAsRepairContext(t *testing.T) {
	t.Parallel()

	view := StatusDisplayFromSnapshot(controlstatus.StatusSnapshot{
		SandboxStatus: controlstatus.StatusSandbox{
			FallbackReason: "bwrap is unavailable",
		},
	})
	if !slices.Contains(view.Fields, DisplayField{Label: "Repair", Value: "bwrap is unavailable"}) {
		t.Fatalf("Fields = %#v, want sandbox repair context", view.Fields)
	}
	wantWarning := "Required sandbox backend is unavailable; repair is required and no implicit Host fallback is active"
	if !slices.Contains(view.Warnings, wantWarning) {
		t.Fatalf("Warnings = %#v, want %q", view.Warnings, wantWarning)
	}
	for _, field := range view.Fields {
		if field.Label == "Fallback" {
			t.Fatalf("Fields = %#v, legacy fallback label must not be displayed", view.Fields)
		}
	}
}
