package gatewayapp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/internal/kernel"
	"github.com/google/jsonschema-go/jsonschema"
)

func TestGuardianOutputSpecAllowsCanonicalExplanation(t *testing.T) {
	t.Parallel()
	options := []kernel.ApprovalOption{{ID: "allow_once", Kind: "allow_once"}, {ID: "reject_once", Kind: "reject_once"}}
	output, err := guardianOutputSpec(&kernel.ApprovalPayload{Options: options})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(output.JSONSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{`{"option_id":"allow_once"}`, `{"option_id":"allow_once","rationale":"This action is authorized."}`} {
		var value any
		if err := json.Unmarshal([]byte(input), &value); err != nil {
			t.Fatal(err)
		}
		if err := resolved.Validate(value); err != nil {
			t.Fatalf("generation schema rejects canonical explanation: %v", err)
		}
		parsed, err := parseGuardianAssessmentForMode(input, output.Mode, options)
		if err != nil || parsed.OptionID != "allow_once" || parsed.Outcome != "allow" || parsed.Rationale != "" {
			t.Fatalf("assessment = %#v, %v; want exact allow with explanation discarded", parsed, err)
		}
	}
}

func TestParseGuardianAssessmentIgnoresExplanatoryFields(t *testing.T) {
	t.Parallel()
	options := []kernel.ApprovalOption{{ID: "allow_once", Kind: "allow_once"}, {ID: "reject_once", Kind: "reject_once"}}
	for _, mode := range []model.OutputMode{model.OutputModeText, model.OutputModeJSON, model.OutputModeSchema} {
		for _, input := range []string{
			`{"option_id":"allow_once","rationale":"This action is authorized."}`,
			`{"option_id":"allow_once","rationale":""}`,
			`{"option_id":"allow_once","rationale":null}`,
			`{"option_id":"allow_once","rationale":{"explanation":"informational"},"extra":[true,{"option_id":"reject_once"}]}`,
			`{"option_id":"allow_once","outcome":"deny","risk_level":"critical","user_authorization":"low"}`,
		} {
			t.Run(string(mode)+"/"+input, func(t *testing.T) {
				parsed, err := parseGuardianAssessmentForMode(input, mode, options)
				if err != nil || parsed.OptionID != "allow_once" || parsed.Outcome != "allow" || parsed.Rationale != "" {
					t.Fatalf("assessment = %#v, %v; want exact allow with explanation discarded", parsed, err)
				}
			})
		}
		parsed, err := parseGuardianAssessmentForMode(`{"option_id":"reject_once","rationale":"  outside task scope  ","decision":"allow","extra":true}`, mode, options)
		if err != nil || parsed.Outcome != "deny" || parsed.Rationale != "outside task scope" {
			t.Fatalf("deny assessment = %#v, %v; want original deny with rationale", parsed, err)
		}
	}
}

func TestParseGuardianAssessmentForModeEnvelopePolicy(t *testing.T) {
	t.Parallel()

	valid := `{"option_id":"allow_once"}`
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
			parsed, err := parseGuardianAssessmentForMode(test.input, test.mode, []kernel.ApprovalOption{{ID: "allow_once", Kind: "allow_once"}})
			if test.wantErr {
				if err == nil {
					t.Fatalf("parseGuardianAssessmentForMode() = %#v, nil; want error", parsed)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGuardianAssessmentForMode() error = %v", err)
			}
			if parsed.OptionID != "allow_once" {
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
			input: `Decision: {"wrapper":{"option_id":"allow_once"}}`,
			want:  "selected option id is required",
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
	valid := `{"option_id":"allow_once"}`
	parsed, err := parseGuardianAssessmentForMode("Decision:\n```json\n"+valid+"\n```", model.OutputModeText, options)
	if err != nil {
		t.Fatalf("parseGuardianAssessmentForMode(valid) error = %v", err)
	}
	if parsed.OptionID != "allow_once" || parsed.Outcome != "allow" {
		t.Fatalf("parsed = %#v, want exact allow_once decision", parsed)
	}

	longRationale := strings.Repeat("material approval reason ", 32)
	longAssessment := `{"option_id":"reject_once","rationale":"` + longRationale + `"}`
	parsed, err = parseGuardianAssessmentForMode("Result: "+longAssessment, model.OutputModeText, options)
	if err != nil {
		t.Fatalf("parseGuardianAssessmentForMode(long rationale) error = %v", err)
	}
	if parsed.Outcome != "deny" || parsed.Rationale != strings.TrimSpace(longRationale) {
		t.Fatalf("long-rationale assessment = %#v, want preserved deny decision", parsed)
	}

	invalid := []struct {
		name  string
		input string
	}{
		{name: "missing option fields", input: `{"outcome":"allow"}`},
		{name: "unknown option", input: `{"option_id":"unknown","risk_level":"low","user_authorization":"high","outcome":"allow","rationale":"unknown"}`},
		{name: "missing option with extra decision", input: `{"decision":"allow","approved":true,"rationale":"authorized"}`},
		{name: "empty option", input: `{"option_id":"","decision":"allow"}`},
		{name: "null option", input: `{"option_id":null}`},
		{name: "numeric option", input: `{"option_id":1}`},
		{name: "object option", input: `{"option_id":{"id":"allow_once"}}`},
		{name: "array option", input: `{"option_id":["allow_once"]}`},
		{name: "malformed decision", input: `{"option_id":}`},
		{name: "malformed extra field", input: `{"option_id":"allow_once","extra":}`},
		{name: "duplicate outcome deny then allow", input: `{"option_id":"allow_once","risk_level":"low","user_authorization":"high","outcome":"deny","outcome":"allow","rationale":"ambiguous"}`},
		{name: "duplicate outcome allow then deny", input: `{"option_id":"reject_once","risk_level":"high","user_authorization":"low","outcome":"allow","outcome":"deny","rationale":"ambiguous"}`},
		{name: "duplicate option id", input: `{"option_id":"reject_once","option_id":"allow_once","risk_level":"low","user_authorization":"high","outcome":"allow","rationale":"ambiguous"}`},
		{name: "duplicate option reversed", input: `{"option_id":"allow_once","option_id":"reject_once"}`},
		{name: "escaped duplicate option", input: `{"option_id":"reject_once","option\u005fid":"allow_once"}`},
		{name: "duplicate rationale", input: `{"option_id":"allow_once","rationale":"no","rationale":"yes"}`},
		{name: "duplicate risk", input: `{"option_id":"allow_once","risk_level":"high","risk_level":"low","user_authorization":"high","outcome":"allow","rationale":"ambiguous"}`},
		{name: "duplicate authorization", input: `{"option_id":"allow_once","risk_level":"low","user_authorization":"low","user_authorization":"high","outcome":"allow","rationale":"ambiguous"}`},
		{name: "uppercase option id", input: `{"OPTION_ID":"allow_once","risk_level":"low","user_authorization":"high","outcome":"allow","rationale":"wrong case"}`},
		{name: "option case alias after exact key", input: `{"option_id":"reject_once","OPTION_ID":"allow_once","rationale":"no"}`},
		{name: "option case alias before exact key", input: `{"OPTION_ID":"reject_once","option_id":"allow_once"}`},
		{name: "rationale case alias", input: `{"option_id":"reject_once","Rationale":"reason"}`},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			for _, mode := range []model.OutputMode{model.OutputModeText, model.OutputModeJSON, model.OutputModeSchema} {
				_, err := parseGuardianAssessmentForMode(test.input, mode, options)
				if err == nil {
					t.Fatalf("parseGuardianAssessmentForMode(%s, %s) error = nil, want strict rejection", test.name, mode)
				}
			}
		})
	}
}

func TestGuardianRetainsExactMCPScopeChoice(t *testing.T) {
	options := []kernel.ApprovalOption{{ID: "allow_once", Kind: "allow_once"}, {ID: "allow_session", Kind: "allow_once"}, {ID: "allow_always", Kind: "allow_always"}, {ID: "cancel", Kind: "reject_once"}}
	payload := &kernel.ApprovalPayload{Options: options}
	for _, tc := range []struct {
		id       string
		approved bool
	}{{"allow_once", true}, {"allow_session", true}, {"allow_always", true}, {"cancel", false}} {
		decisionJSON := `{"option_id":"` + tc.id + `"}`
		if !tc.approved {
			decisionJSON = `{"option_id":"` + tc.id + `","rationale":"The current tool call is not authorized."}`
		}
		parsed, err := parseGuardianAssessmentForMode(decisionJSON, model.OutputModeSchema, options)
		if err != nil {
			t.Fatal(err)
		}
		decision, err := finalizeGuardianDecision(payload, parsed)
		if err != nil || decision.OptionID != tc.id || decision.Approved != tc.approved {
			t.Fatalf("Guardian choice %s = %#v, %v", tc.id, decision, err)
		}
	}
	if _, err := parseGuardianAssessmentForMode(`{"option_id":"not_offered"}`, model.OutputModeSchema, options); err == nil {
		t.Fatal("unoffered Guardian choice accepted")
	}
}

func TestParseGuardianAssessmentForModeRejectsUnsupportedMode(t *testing.T) {
	t.Parallel()

	_, err := parseGuardianAssessmentForMode(`{"outcome":"allow"}`, model.OutputMode("binary"), nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported output mode") {
		t.Fatalf("parseGuardianAssessmentForMode() error = %v, want unsupported mode", err)
	}
}

func TestGuardianDenyRequiresSpecificRationale(t *testing.T) {
	for _, text := range []string{`{"option_id":"reject_once"}`, `{"option_id":"reject_once","rationale":"  "}`, `{"option_id":"reject_once","rationale":null}`, `{"option_id":"reject_once","rationale":true}`} {
		_, err := parseGuardianAssessmentForMode(text, model.OutputModeSchema, []kernel.ApprovalOption{{ID: "allow_once", Kind: "allow_once"}, {ID: "reject_once", Kind: "reject_once"}})
		if err == nil {
			t.Fatalf("accepted invalid decision %s", text)
		}
	}
}

func TestGuardianNativeAssessmentRejectsNonObjectAndTrailingValues(t *testing.T) {
	for _, mode := range []model.OutputMode{model.OutputModeJSON, model.OutputModeSchema} {
		for _, input := range []string{`null`, `[]`, `"allow_once"`, `{"option_id":"allow_once"} true`, `{"option_id":"allow_once"} trailing`, `{"option_id":"allow_once"} {"option_id":"reject_once"}`} {
			if _, err := parseGuardianAssessmentForMode(input, mode, []kernel.ApprovalOption{{ID: "allow_once", Kind: "allow_once"}}); err == nil {
				t.Fatalf("accepted %s assessment %s", mode, input)
			}
		}
	}
}
