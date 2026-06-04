package tuiapp

import (
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/protocol/acp/control"
)

func TestStatusViewModelFooterOmitsActiveJobs(t *testing.T) {
	vm := statusViewModelFromSnapshot(control.StatusSnapshot{
		TotalTokens:         42000,
		ContextWindowTokens: 128000,
		ActiveJobs:          3,
		Running:             true,
	})

	got := vm.FooterContextText("")
	if !strings.Contains(got, "42k / 128k · 32%") {
		t.Fatalf("footerContextText() = %q, want token usage", got)
	}
	if strings.Contains(got, "ctx ") {
		t.Fatalf("footerContextText() = %q, should omit ctx prefix", got)
	}
	if strings.Contains(got, "job") {
		t.Fatalf("footerContextText() = %q, should omit active job count", got)
	}
}

func TestFormatStatusContextDisplayStripsLegacyCtxPrefix(t *testing.T) {
	got := formatStatusContextDisplay("ctx 4.9k / 1.0m · 0%")
	if got != "4.9k / 1.0m · 0%" {
		t.Fatalf("formatStatusContextDisplay() = %q, want legacy ctx prefix stripped", got)
	}
}

func TestStatusViewModelFooterModeOmitsSandboxRuntimeDetails(t *testing.T) {
	vm := statusViewModelFromSnapshot(control.StatusSnapshot{
		ModeLabel:              "auto-review",
		SandboxResolvedBackend: "bwrap",
		Route:                  "sandbox",
		SecuritySummary:        "bwrap",
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

func TestStatusViewModelHeaderDoesNotPrefixACPControllerProvider(t *testing.T) {
	vm := statusViewModelFromSnapshot(control.StatusSnapshot{
		Model:           "opencode/deepseek-v4-flash-free [low]",
		Provider:        "acp",
		ReasoningEffort: "low",
	})

	got := vm.HeaderModelText("")
	if got != "opencode/deepseek-v4-flash-free [low]" {
		t.Fatalf("HeaderModelText() = %q, want remote ACP model without acp/ prefix", got)
	}
}
