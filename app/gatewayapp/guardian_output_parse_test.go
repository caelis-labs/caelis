package gatewayapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func TestParseGuardianAssessmentForModeEnvelopePolicy(t *testing.T) {
	t.Parallel()

	valid := `{"outcome":"allow","risk_level":"low","user_authorization":"high","rationale":"bounded inspection"}`
	tests := []struct {
		name    string
		mode    model.OutputMode
		input   string
		wantErr bool
	}{
		{name: "schema standalone", mode: model.OutputModeSchema, input: valid},
		{name: "schema fenced", mode: model.OutputModeSchema, input: "```json\n" + valid + "\n```", wantErr: true},
		{name: "schema prose", mode: model.OutputModeSchema, input: "Decision follows:\n" + valid, wantErr: true},
		{name: "json standalone", mode: model.OutputModeJSON, input: valid},
		{name: "json fenced", mode: model.OutputModeJSON, input: "```json\n" + valid + "\n```", wantErr: true},
		{name: "text standalone", mode: model.OutputModeText, input: valid},
		{name: "text json fence", mode: model.OutputModeText, input: "```json\n" + valid + "\n```"},
		{name: "text bare fence", mode: model.OutputModeText, input: "```\n" + valid + "\n```"},
		{name: "text prose", mode: model.OutputModeText, input: "Guardian decision:\n" + valid + "\nDone."},
		{name: "text prose and fence", mode: model.OutputModeText, input: "Guardian decision:\n```json\n" + valid + "\n```\nDone."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := parseGuardianAssessmentForMode(test.input, test.mode, nil)
			if test.wantErr {
				if err == nil {
					t.Fatalf("parseGuardianAssessmentForMode() = %#v, nil; want error", parsed)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGuardianAssessmentForMode() error = %v", err)
			}
			if parsed.Outcome != "allow" || parsed.RiskLevel != "low" {
				t.Fatalf("parsed = %#v, want low-risk allow", parsed)
			}
		})
	}
}

func TestParseGuardianAssessmentForModeRejectsAmbiguousTextBeforeValidation(t *testing.T) {
	t.Parallel()

	valid := `{"outcome":"deny","risk_level":"high","user_authorization":"low","rationale":"too broad"}`
	input := `Example: {"outcome":}` + "\nActual: " + valid
	_, err := parseGuardianAssessmentForMode(input, model.OutputModeText, nil)
	if err == nil || !strings.Contains(err.Error(), "more than one top-level JSON object") {
		t.Fatalf("parseGuardianAssessmentForMode() error = %v, want ambiguity before candidate validation", err)
	}
}

func TestParseGuardianAssessmentForModeRejectsInvalidTextEnvelopes(t *testing.T) {
	t.Parallel()

	valid := `{"outcome":"deny","risk_level":"high","user_authorization":"low","rationale":"too broad"}`
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "zero objects", input: "Guardian could not decide.", want: "no JSON object"},
		{name: "multiple objects", input: valid + "\n" + valid, want: "more than one top-level JSON object"},
		{name: "unbalanced opening", input: "Decision: " + valid + " {", want: "unbalanced JSON object braces"},
		{name: "unbalanced closing", input: "Decision: " + valid + " }", want: "unbalanced JSON object braces"},
		{
			name:  "unique outer object does not fall back to nested decision",
			input: `Decision: {"wrapper":{"outcome":"allow","risk_level":"low","user_authorization":"high","rationale":"nested"}}`,
			want:  "unsupported field",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseGuardianAssessmentForMode(test.input, model.OutputModeText, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parseGuardianAssessmentForMode() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestParseGuardianAssessmentForModeEnforcesLocalLimit(t *testing.T) {
	t.Parallel()

	_, err := parseGuardianAssessmentForMode(strings.Repeat("x", guardianMaxAssessmentBytes+1), model.OutputModeText, nil)
	if err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("parseGuardianAssessmentForMode() error = %v, want local byte limit", err)
	}
}

func TestParseGuardianAssessmentForModePreservesStrictDecisionValidation(t *testing.T) {
	t.Parallel()

	options := []kernel.ApprovalOption{
		{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
		{ID: "reject_once", Name: "Reject once", Kind: "reject_once"},
	}
	valid := `{"option_id":"allow_once","risk_level":"medium","user_authorization":"high","outcome":"allow","rationale":"bounded action"}`
	parsed, err := parseGuardianAssessmentForMode("Decision:\n```json\n"+valid+"\n```", model.OutputModeText, options)
	if err != nil {
		t.Fatalf("parseGuardianAssessmentForMode(valid) error = %v", err)
	}
	if parsed.OptionID != "allow_once" || parsed.Outcome != "allow" {
		t.Fatalf("parsed = %#v, want exact allow_once decision", parsed)
	}

	longRationale := strings.Repeat("material approval reason ", 32)
	longAssessment := `{"option_id":"allow_once","risk_level":"low","user_authorization":"high","outcome":"allow","rationale":"` + longRationale + `"}`
	parsed, err = parseGuardianAssessmentForMode("Result: "+longAssessment, model.OutputModeText, options)
	if err != nil {
		t.Fatalf("parseGuardianAssessmentForMode(long rationale) error = %v", err)
	}
	if parsed.Outcome != "allow" || parsed.Rationale != strings.TrimSpace(longRationale) {
		t.Fatalf("long-rationale assessment = %#v, want preserved allow decision", parsed)
	}

	invalid := []struct {
		name  string
		input string
	}{
		{name: "missing option fields", input: `{"outcome":"allow"}`},
		{name: "unknown option", input: `{"option_id":"unknown","risk_level":"low","user_authorization":"high","outcome":"allow","rationale":"unknown"}`},
		{name: "allow option deny outcome", input: `{"option_id":"allow_once","risk_level":"high","user_authorization":"low","outcome":"deny","rationale":"contradiction"}`},
		{name: "deny option allow outcome", input: `{"option_id":"reject_once","risk_level":"low","user_authorization":"high","outcome":"allow","rationale":"contradiction"}`},
		{name: "critical allow", input: `{"option_id":"allow_once","risk_level":"critical","user_authorization":"high","outcome":"allow","rationale":"critical"}`},
		{name: "unknown field", input: `{"option_id":"allow_once","risk_level":"low","user_authorization":"high","outcome":"allow","rationale":"ok","extra":true}`},
		{name: "duplicate outcome deny then allow", input: `{"option_id":"allow_once","risk_level":"low","user_authorization":"high","outcome":"deny","outcome":"allow","rationale":"ambiguous"}`},
		{name: "duplicate outcome allow then deny", input: `{"option_id":"reject_once","risk_level":"high","user_authorization":"low","outcome":"allow","outcome":"deny","rationale":"ambiguous"}`},
		{name: "duplicate option id", input: `{"option_id":"reject_once","option_id":"allow_once","risk_level":"low","user_authorization":"high","outcome":"allow","rationale":"ambiguous"}`},
		{name: "duplicate risk", input: `{"option_id":"allow_once","risk_level":"high","risk_level":"low","user_authorization":"high","outcome":"allow","rationale":"ambiguous"}`},
		{name: "duplicate authorization", input: `{"option_id":"allow_once","risk_level":"low","user_authorization":"low","user_authorization":"high","outcome":"allow","rationale":"ambiguous"}`},
		{name: "uppercase outcome", input: `{"option_id":"allow_once","risk_level":"low","user_authorization":"high","Outcome":"allow","rationale":"wrong case"}`},
		{name: "uppercase option id", input: `{"OPTION_ID":"allow_once","risk_level":"low","user_authorization":"high","outcome":"allow","rationale":"wrong case"}`},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseGuardianAssessmentForMode("Result: "+test.input, model.OutputModeText, options)
			if err == nil {
				t.Fatalf("parseGuardianAssessmentForMode(%s) error = nil, want strict rejection", test.name)
			}
		})
	}
}

func TestParseGuardianAssessmentForModeRejectsUnsupportedMode(t *testing.T) {
	t.Parallel()

	_, err := parseGuardianAssessmentForMode(`{"outcome":"allow"}`, model.OutputMode("binary"), nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported output mode") {
		t.Fatalf("parseGuardianAssessmentForMode() error = %v, want unsupported mode", err)
	}
}
