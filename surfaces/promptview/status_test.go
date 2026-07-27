package promptview

import (
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
