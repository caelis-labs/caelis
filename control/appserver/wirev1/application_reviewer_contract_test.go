package wirev1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/wirev1/generated"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func TestApplicationGuardianGeneratedWirePreservesReviewPolicy(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("../../../api/control/v1/fixtures", "application-guardian-create.json"))
	if err != nil {
		t.Fatal(err)
	}
	var request generated.CreateSessionRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodeJSONWithNumbers(t, raw), decodeJSONWithNumbers(t, roundTrip)) {
		t.Fatalf("generated create client lost review fields: got %s, want %s", roundTrip, raw)
	}
	state := sessionprofile.ReviewerState{SessionID: "session-reviewed-1", ApprovalMode: "auto-review", Reviewer: &sessionprofile.Reviewer{Kind: "guardian", Model: "configured-reviewer-model"}, Status: "unavailable", Reason: "reviewer not configured"}
	validateWireValue(t, "SessionReviewerState", state)
	validateWireValue(t, "SessionReviewerState", sessionprofile.ReviewerState{SessionID: "session-manual-1", ApprovalMode: "manual", Status: "manual"})
}

func TestApplicationGuardianSchemaRejectsUnknownReviewSelectors(t *testing.T) {
	for _, tc := range []struct{ schema, body string }{
		{"SessionReviewer", `{"kind":"other","model":"configured-model"}`},
		{"SessionReviewer", `{"kind":"guardian","model":""}`},
		{"SessionPermissions", `{"approval_mode":"unsafe"}`},
		{"SessionToolDefinition", `{"name":"t","description":"t","input_schema":{},"approval_policy":"unsafe"}`},
		{"SessionReviewerState", `{"session_id":"s","approval_mode":"auto-review","status":"healthy"}`},
	} {
		if err := openAPIValidator(t, tc.schema).Validate(decodeJSONWithNumbers(t, []byte(tc.body))); err == nil {
			t.Fatalf("schema %s accepted invalid review selector: %s", tc.schema, tc.body)
		}
	}
}
