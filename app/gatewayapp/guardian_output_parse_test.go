package gatewayapp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
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
		parsed, err := parseGuardianAssessmentForMode(nil, input, output.Mode, options)
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
				parsed, err := parseGuardianAssessmentForMode(nil, input, mode, options)
				if err != nil || parsed.OptionID != "allow_once" || parsed.Outcome != "allow" || parsed.Rationale != "" {
					t.Fatalf("assessment = %#v, %v; want exact allow with explanation discarded", parsed, err)
				}
			})
		}
		parsed, err := parseGuardianAssessmentForMode(nil, `{"option_id":"reject_once","rationale":"  outside task scope  ","decision":"allow","extra":true}`, mode, options)
		if err != nil || parsed.Outcome != "deny" || parsed.Rationale != "outside task scope" {
			t.Fatalf("deny assessment = %#v, %v; want original deny with rationale", parsed, err)
		}
	}
}

func TestGuardianRecoversConsistentDecisions(t *testing.T) {
	t.Parallel()
	options := []kernel.ApprovalOption{{ID: "allow_once", Kind: "allow_once"}, {ID: "allow_always", Kind: "allow_always"}, {ID: "reject_once", Kind: "reject_once"}}
	allow := `{"option_id":"allow_once"}`
	deny := `{"option_id":"reject_once","rationale":"Outside scope"}`
	for _, mode := range []model.OutputMode{model.OutputModeSchema, model.OutputModeJSON, model.OutputModeText} {
		for _, input := range []string{
			allow + "wrote",
			allow + "wrote" + allow,
			allow + "\n" + allow,
			"[decision] " + allow,
			"Decision:\n```json\n" + allow + "\n```",
			allow + " true",
		} {
			result, err := parseGuardianAssessmentForMode(nil, input, mode, options)
			if err != nil || result.OptionID != "allow_once" {
				t.Errorf("%s %q: got %+v, %v", mode, input, result, err)
			}
		}
		result, err := parseGuardianAssessmentForMode(nil, deny+`{"option_id":"reject_once","rationale":"Final reason"}`, mode, options)
		if err != nil || result.Rationale != "Final reason" {
			t.Errorf("%s repeated denial: got %+v, %v", mode, result, err)
		}
		always := `{"option_id":"allow_always"}`
		for _, pair := range []struct{ output, want string }{
			{allow + deny, "reject_once"}, {deny + always + allow, "reject_once"},
			{allow + always, "allow_once"}, {always + allow, "allow_once"},
		} {
			got, err := parseGuardianAssessmentForMode(nil, pair.output, mode, options)
			if err != nil || got.OptionID != pair.want || (got.OptionID == "reject_once" && got.Rationale != "Outside scope") {
				t.Fatalf("decision for %q = %+v, %v", pair.output, got, err)
			}
		}
		for _, input := range []string{
			"No decision", "null", "[]",
			"[" + allow + "]", "[[], " + allow + "]", `["]",` + allow + "]",
			`{"wrapper":` + allow + "}",
			allow + " {", allow + " }", `{"option_id":}` + allow,
		} {
			if result, err := parseGuardianAssessmentForMode(nil, input, mode, options); err == nil {
				t.Errorf("%s %q: accepted %+v", mode, input, result)
			}
		}
	}
}

func TestGuardianMixedGrantsUseOfferedScope(t *testing.T) {
	options := []kernel.ApprovalOption{{ID: "a", Kind: "allow_once"}, {ID: "b", Kind: "allow_once"}, {ID: "c", Kind: "allow_always"}}
	auth := &session.ApprovalAuthorization{Choices: []session.AuthorizationChoice{
		{OptionID: "a", Persistence: session.AuthorizationOnce},
		{OptionID: "b", Persistence: session.AuthorizationSession},
		{OptionID: "c", Persistence: session.AuthorizationPolicy},
	}}
	for _, pair := range []struct{ first, second, want string }{
		{"a", "b", "a"}, {"b", "a", "a"}, {"b", "c", "b"}, {"c", "b", "b"},
	} {
		text := `{"option_id":"` + pair.first + `"}{"option_id":"` + pair.second + `"}`
		got, err := parseGuardianAssessmentForMode(auth, text, model.OutputModeSchema, options)
		if err != nil || got.OptionID != pair.want {
			t.Fatalf("decision for %q = %+v, %v", text, got, err)
		}
	}
}

func TestParseGuardianAssessmentForModeEnforcesLocalLimit(t *testing.T) {
	t.Parallel()

	_, err := parseGuardianAssessmentForMode(nil, strings.Repeat("x", guardianMaxAssessmentBytes+1), model.OutputModeText, nil)
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
	parsed, err := parseGuardianAssessmentForMode(nil, "Decision:\n```json\n"+valid+"\n```", model.OutputModeText, options)
	if err != nil {
		t.Fatalf("parseGuardianAssessmentForMode(valid) error = %v", err)
	}
	if parsed.OptionID != "allow_once" || parsed.Outcome != "allow" {
		t.Fatalf("parsed = %#v, want exact allow_once decision", parsed)
	}

	longRationale := strings.Repeat("material approval reason ", 32)
	longAssessment := `{"option_id":"reject_once","rationale":"` + longRationale + `"}`
	parsed, err = parseGuardianAssessmentForMode(nil, "Result: "+longAssessment, model.OutputModeText, options)
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
				_, err := parseGuardianAssessmentForMode(nil, test.input, mode, options)
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
		parsed, err := parseGuardianAssessmentForMode(nil, decisionJSON, model.OutputModeSchema, options)
		if err != nil {
			t.Fatal(err)
		}
		decision, err := finalizeGuardianDecision(payload, parsed)
		if err != nil || decision.OptionID != tc.id || decision.Approved != tc.approved {
			t.Fatalf("Guardian choice %s = %#v, %v", tc.id, decision, err)
		}
	}
	if _, err := parseGuardianAssessmentForMode(nil, `{"option_id":"not_offered"}`, model.OutputModeSchema, options); err == nil {
		t.Fatal("unoffered Guardian choice accepted")
	}
}

func TestParseGuardianAssessmentForModeRejectsUnsupportedMode(t *testing.T) {
	t.Parallel()

	_, err := parseGuardianAssessmentForMode(nil, `{"outcome":"allow"}`, model.OutputMode("binary"), nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported output mode") {
		t.Fatalf("parseGuardianAssessmentForMode() error = %v, want unsupported mode", err)
	}
}

func TestGuardianDenyRequiresSpecificRationale(t *testing.T) {
	for _, text := range []string{`{"option_id":"reject_once"}`, `{"option_id":"reject_once","rationale":"  "}`, `{"option_id":"reject_once","rationale":null}`, `{"option_id":"reject_once","rationale":true}`} {
		_, err := parseGuardianAssessmentForMode(nil, text, model.OutputModeSchema, []kernel.ApprovalOption{{ID: "allow_once", Kind: "allow_once"}, {ID: "reject_once", Kind: "reject_once"}})
		if err == nil {
			t.Fatalf("accepted invalid decision %s", text)
		}
	}
}
