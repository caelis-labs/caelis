package tuiapp

import (
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestApprovalUsageWarningRendersWithCompletedDecision(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.beginLiveTurn(SubmissionModeDefault, false, time.Now())
	m = applyACPEnvelopeForTest(t, m, eventstream.Envelope{Kind: eventstream.KindApprovalReview, SessionID: "s", ApprovalReview: &eventstream.ApprovalReview{ToolCallID: "call", ToolName: "RunCommand", Status: "approved", Risk: "low", Authorization: "high", Text: "Authorized action.\nGuardian usage accounting could not be persisted."}})
	block := requireMainACPTurnBlockForTest(t, m)
	rows := renderedPlainRows(block.Render(m.blockRenderContext(160)))
	rendered := strings.Join(rows, "\n")
	t.Log(rendered)
	if !strings.Contains(rendered, "Authorized action.") || !strings.Contains(rendered, "Guardian usage accounting could not be persisted.") {
		t.Fatalf("rendered warning missing:\n%s", rendered)
	}
	if block.Events[0].ApprovalStatus != "approved" {
		t.Fatal("warning changed completed decision")
	}
}
