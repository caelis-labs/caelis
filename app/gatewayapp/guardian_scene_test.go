package gatewayapp

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

func TestGuardianReviewFailureLogsCauseAndIdentity(t *testing.T) {
	service, active := newApprovalReviewerTestSession(t, t.Context())
	responses := make([]string, guardianAssessmentMaxAttempts)
	for i := range responses {
		responses[i] = "invalid"
	}
	llm := &approvalReviewerFakeModel{responses: responses}
	var output bytes.Buffer
	reviewer := newGuardianApprovalApprover(service, slog.New(slog.NewJSONHandler(&output, nil)))
	req := approvalReviewerTestRequest(active, llm, "inspect", nil)
	_, err := reviewer.Decide(t.Context(), req)
	if err == nil {
		t.Fatal("invalid assessments should fail the review")
	}
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) != nil || entry["msg"] != "Guardian review failed" {
			continue
		}
		if entry["error"] != err.Error() || entry["session_id"] != req.SessionRef.SessionID || entry["review_id"] != req.ReviewID {
			t.Fatalf("failure diagnostics lost cause or identity: %v", entry)
		}
		return
	}
	t.Fatalf("failure diagnostics missing: %s", output.String())
}

func TestStackNewGuardianApproverUsesStackSessions(t *testing.T) {
	sessions := inmemory.NewStore(inmemory.Config{})
	diagnostics := slog.New(slog.NewTextHandler(io.Discard, nil))
	stack := &Stack{composition: runtimeComposition{
		sessions: sessions,
		authorities: runtimeHostAuthorities{
			diagnostics: diagnostics,
		},
	}}

	approver := stack.composition.newGuardianApprover()
	if approver == nil {
		t.Fatal("newGuardianApprover() = nil")
		return
	}
	if approver.sessions != sessions {
		t.Fatal("newGuardianApprover() did not preserve the Stack session service")
	}
	runner, ok := approver.systemAgents.(*systemManagedAgentRuntime)
	if !ok || runner.config.Diagnostics != diagnostics {
		t.Fatal("newGuardianApprover() did not preserve private Runtime diagnostics")
	}
}
