package transcript

import "testing"

func TestApprovalReviewCurrentAndHistoricalResults(t *testing.T) {
	for _, tc := range []struct{ text, status, rationale string }{
		{"approved", "approved", ""},
		{" denied: 当前请求未授权修改该路径 ", "denied", "当前请求未授权修改该路径"},
		{"denied:", "denied", ""},
		{"failed: reviewer unavailable", "failed", "reviewer unavailable"},
		{"timed_out", "timed_out", ""},
		{"needs_user", "needs_user", ""},
		{"needs-user: confirm target", "needs-user", "confirm target"},
		{"Automatic approval review approved (risk: low, authorization: allow)", "approved", ""},
		{"Automatic approval review denied: unsafe command", "denied", "unsafe command"},
	} {
		t.Run(tc.text, func(t *testing.T) {
			got := ApprovalReviewDisplayParts("", "", "", tc.text)
			if got.Status != tc.status || got.Rationale != tc.rationale {
				t.Fatalf("display = %#v", got)
			}
		})
	}
	current := ApprovalReviewDisplayParts("denied", "", "", "denied: risk: high; authorization: missing")
	if current.Risk != "" || current.Authorization != "" {
		t.Fatalf("rationale became structured fields: %#v", current)
	}
	if got := ApprovalReviewTailOutput(ApprovalReviewFields{Status: "approved", Text: "approved"}); got != "Auto approval · approved\n" {
		t.Fatalf("tail = %q", got)
	}
	// Typed status stays authoritative when a diagnostic contains another status.
	if got := ApprovalReviewDisplayParts("failed", "", "", "denied: delivery failed"); got.Status != "failed" {
		t.Fatalf("status = %q", got.Status)
	}
}
