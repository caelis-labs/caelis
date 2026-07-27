package statusbar

import (
	"strings"
	"testing"

	controlstatus "github.com/caelis-labs/caelis/control/status"
)

func TestFromSnapshotFooterOmitsActiveJobs(t *testing.T) {
	vm := FromSnapshot(controlstatus.StatusSnapshot{
		Usage: controlstatus.StatusUsage{
			TotalTokens:         42000,
			ContextWindowTokens: 128000,
		},
		Runtime: controlstatus.StatusRuntime{
			ActiveJobs: 3,
			Running:    true,
		},
	})

	got := vm.FooterContextText("")
	if !strings.Contains(got, "42k / 128k · 32%") {
		t.Fatalf("FooterContextText() = %q, want token usage", got)
	}
	if strings.Contains(got, "ctx ") {
		t.Fatalf("FooterContextText() = %q, should omit ctx prefix", got)
	}
	if strings.Contains(got, "job") {
		t.Fatalf("FooterContextText() = %q, should omit active job count", got)
	}
}

func TestFromSnapshotFooterModeOmitsSandboxRuntimeDetails(t *testing.T) {
	vm := FromSnapshot(controlstatus.StatusSnapshot{
		Session: controlstatus.StatusSession{ModeLabel: "auto-review"},
		SandboxStatus: controlstatus.StatusSandbox{
			ResolvedBackend: "bwrap",
			Route:           "sandbox",
			SecuritySummary: "bwrap",
		},
	})

	got := vm.FooterModeText("")
	if got != "auto-review" {
		t.Fatalf("FooterModeText() = %q, want mode only", got)
	}
	for _, unexpected := range []string{"bwrap", "sandbox"} {
		if strings.Contains(got, unexpected) {
			t.Fatalf("FooterModeText() = %q, should omit %q", got, unexpected)
		}
	}
}

func TestHeaderModelTextDoesNotPrefixACPControllerProvider(t *testing.T) {
	vm := FromSnapshot(controlstatus.StatusSnapshot{
		ModelStatus: controlstatus.StatusModel{
			Display:         "opencode/deepseek-v4-flash-free [low]",
			Provider:        "acp",
			ReasoningEffort: "low",
		},
	})

	got := vm.HeaderModelText("")
	if got != "opencode/deepseek-v4-flash-free [low]" {
		t.Fatalf("HeaderModelText() = %q, want remote ACP model without acp/ prefix", got)
	}
}

func TestFromSnapshotUsesGroupedStatus(t *testing.T) {
	vm := FromSnapshot(controlstatus.StatusSnapshot{
		ModelStatus: controlstatus.StatusModel{
			Display:         "grouped/model",
			Provider:        "grouped-provider",
			ReasoningEffort: "high",
		},
		Session: controlstatus.StatusSession{
			ModeLabel: "manual",
		},
		SandboxStatus: controlstatus.StatusSandbox{
			ResolvedBackend: "windows",
			Route:           "sandbox",
			SecuritySummary: "windows",
		},
		Usage: controlstatus.StatusUsage{
			TotalTokens:         64000,
			ContextWindowTokens: 128000,
		},
	})

	if got := vm.HeaderModelText(""); got != "grouped-provider/grouped/model [high]" {
		t.Fatalf("HeaderModelText() = %q, want grouped model", got)
	}
	if got := vm.FooterModeText(""); got != "manual" {
		t.Fatalf("FooterModeText() = %q, want grouped mode", got)
	}
	if got := vm.FooterContextText(""); !strings.Contains(got, "64k / 128k · 50%") {
		t.Fatalf("FooterContextText() = %q, want grouped usage", got)
	}
}
