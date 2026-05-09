package tuiapp

import (
	"strings"
	"testing"

	tuidriver "github.com/OnslaughtSnail/caelis/tui/driver"
)

func TestStatusViewModelDisplaysActiveJobs(t *testing.T) {
	vm := statusViewModelFromSnapshot(tuidriver.StatusSnapshot{
		TotalTokens:         42000,
		ContextWindowTokens: 128000,
		ActiveJobs:          3,
		Running:             true,
	})

	if got := vm.Jobs; got != 3 {
		t.Fatalf("Jobs = %d, want 3", got)
	}
	if got := vm.FooterContextText(""); !strings.Contains(got, "3 jobs") {
		t.Fatalf("footerContextText() = %q, want active job count", got)
	}
}
