package promptview

import (
	"slices"
	"strings"
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
	if got := FormatContextUsage(0, 500000); got != "" {
		t.Fatalf("FormatContextUsage(0, window) = %q, want empty until observed usage", got)
	}
	if got := FormatContextUsage(1600, 0); got != "1.6k" {
		t.Fatalf("FormatContextUsage(total, 0) = %q, want compact total", got)
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

func TestFormatDoctorSnapshotSurfacesSandboxBlocker(t *testing.T) {
	t.Parallel()

	got := FormatDoctorSnapshot(controlstatus.StatusSnapshot{
		ModelStatus: controlstatus.StatusModel{Provider: "openai", Name: "gpt-5.6"},
		SandboxStatus: controlstatus.StatusSandbox{
			FallbackReason: "bwrap cannot start: permission denied",
			InstallHint:    "install bubblewrap or repair sandbox setup",
			HostExecution:  true,
		},
	})
	for _, want := range []string{
		"blocked sandbox: bwrap cannot start: permission denied",
		"fix: install bubblewrap or repair sandbox setup",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("FormatDoctorSnapshot() = %q, want %q", got, want)
		}
	}
	if strings.Contains(got, "info fix: /doctor") {
		t.Fatalf("FormatDoctorSnapshot() = %q, must not tell the user to run /doctor from /doctor", got)
	}
}

func TestStatusDisplayOmitsHabitualYOLOWarningCopy(t *testing.T) {
	t.Parallel()

	view := StatusDisplayFromSnapshot(controlstatus.StatusSnapshot{
		SandboxStatus: controlstatus.StatusSandbox{
			Type:            "host",
			Route:           "host",
			HostExecution:   true,
			FullAccessMode:  true,
			SecuritySummary: "YOLO: unrestricted host access; approval review disabled",
		},
	})
	warnings := strings.Join(view.Warnings, "\n")
	if strings.Contains(warnings, "YOLO mode is active") || strings.Contains(warnings, "Human and Guardian") {
		t.Fatalf("Warnings = %#v, want no repeated YOLO essay", view.Warnings)
	}
	if strings.Contains(warnings, "Auto-Review remains enabled") {
		t.Fatalf("Warnings = %#v, must not claim approval review remains active", view.Warnings)
	}
}
