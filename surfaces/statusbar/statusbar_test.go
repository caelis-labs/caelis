package statusbar

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/protocol/acp/control"
)

func TestFromSnapshotFooterOmitsActiveJobs(t *testing.T) {
	vm := FromSnapshot(control.StatusSnapshot{
		Usage: control.StatusUsage{
			TotalTokens:         42000,
			ContextWindowTokens: 128000,
		},
		Runtime: control.StatusRuntime{
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

func TestFormatContextUsage(t *testing.T) {
	if got := FormatContextUsage(12600, 88000); got != "13k / 88k · 14%" {
		t.Fatalf("FormatContextUsage() = %q, want %q", got, "13k / 88k · 14%")
	}
	if got := FormatContextUsage(42000, 128000); got != "42k / 128k · 32%" {
		t.Fatalf("FormatContextUsage() = %q, want %q", got, "42k / 128k · 32%")
	}
	if got := FormatContextUsage(1234, 0); got != "1.2k" {
		t.Fatalf("FormatContextUsage() no window = %q, want token count", got)
	}
}

func TestFromSnapshotFooterModeOmitsSandboxRuntimeDetails(t *testing.T) {
	vm := FromSnapshot(control.StatusSnapshot{
		Session: control.StatusSession{ModeLabel: "auto-review"},
		SandboxStatus: control.StatusSandbox{
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
	vm := FromSnapshot(control.StatusSnapshot{
		ModelStatus: control.StatusModel{
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
	vm := FromSnapshot(control.StatusSnapshot{
		ModelStatus: control.StatusModel{
			Display:         "grouped/model",
			Provider:        "grouped-provider",
			ReasoningEffort: "high",
		},
		Session: control.StatusSession{
			ModeLabel: "manual",
		},
		SandboxStatus: control.StatusSandbox{
			ResolvedBackend: "windows",
			Route:           "sandbox",
			SecuritySummary: "windows",
		},
		Usage: control.StatusUsage{
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
