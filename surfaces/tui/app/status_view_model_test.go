package tuiapp

import (
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/surfaces/tui/driver"
)

func TestStatusViewModelFooterOmitsActiveJobs(t *testing.T) {
	vm := statusViewModelFromSnapshot(tuidriver.StatusSnapshot{
		TotalTokens:         42000,
		ContextWindowTokens: 128000,
		ActiveJobs:          3,
		Running:             true,
	})

	got := vm.FooterContextText("")
	if !strings.Contains(got, "ctx 42k / 128k · 32%") {
		t.Fatalf("footerContextText() = %q, want token usage", got)
	}
	if strings.Contains(got, "job") {
		t.Fatalf("footerContextText() = %q, should omit active job count", got)
	}
}

func TestStatusViewModelFooterModeOmitsSandboxRuntimeDetails(t *testing.T) {
	vm := statusViewModelFromSnapshot(tuidriver.StatusSnapshot{
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
