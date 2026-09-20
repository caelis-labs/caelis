package gatewayapp

import (
	"math"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/judgment"
)

func TestGuardianScreeningUsesOptionDistributionAndNarrowScope(t *testing.T) {
	for _, tc := range []struct {
		name               string
		options            []approval.Option
		probabilities      map[string]float64
		choice, wantID     string
		unknown, violation float64
	}{
		{"clear allow", nil, map[string]float64{"allow_once": .98, "reject_once": .02}, "allow_once", "allow_once", 0, 0},
		{"clear deny", nil, map[string]float64{"allow_once": .02, "reject_once": .98}, "reject_once", "reject_once", 0, 1},
		{"close distribution", nil, map[string]float64{"allow_once": .55, "reject_once": .45}, "allow_once", "", 0, 0},
		{"tie", nil, map[string]float64{"allow_once": .5, "reject_once": .5}, "allow_once", "", 0, 0},
		{"no relaxed routine threshold", nil, map[string]float64{"allow_once": .85, "reject_once": .15}, "allow_once", "", 0, 0},
		{"same decision split scopes", []approval.Option{{ID: "once", Kind: "allow_once"}, {ID: "always", Kind: "allow_always"}, {ID: "no", Kind: "reject_once"}}, map[string]float64{"once": .40, "always": .58, "no": .02}, "always", "once", 0, 0},
		{"reordered misleading names", []approval.Option{{ID: "allow", Name: "Approve", Kind: "reject_once"}, {ID: "b", Kind: "allow_always"}, {ID: "reject", Name: "Deny", Kind: "allow_once"}}, map[string]float64{"allow": .02, "b": .58, "reject": .40}, "b", "reject", 0, 0},
		{"persistent exact lead", []approval.Option{{ID: "persist", Kind: "allow_always"}, {ID: "no", Kind: "reject_once"}}, map[string]float64{"persist": .98, "no": .02}, "persist", "persist", 0, 0},
		{"persistent split is inconclusive", []approval.Option{{ID: "p1", Kind: "allow_always"}, {ID: "p2", Kind: "allow_always"}, {ID: "no", Kind: "reject_once"}}, map[string]float64{"p1": .50, "p2": .48, "no": .02}, "p1", "", 0, 0},
		{"missing distribution", nil, nil, "allow_once", "", 0, 0},
		{"NaN", nil, map[string]float64{"allow_once": math.NaN(), "reject_once": 0}, "allow_once", "", 0, 0},
		{"infinity", nil, map[string]float64{"allow_once": math.Inf(1), "reject_once": 0}, "allow_once", "", 0, 0},
		{"negative", nil, map[string]float64{"allow_once": 1.01, "reject_once": -.01}, "allow_once", "", 0, 0},
		{"wrong total", nil, map[string]float64{"allow_once": .9, "reject_once": .4}, "allow_once", "", 0, 0},
		{"unknown key", nil, map[string]float64{"allow_once": .98, "extra": .02}, "allow_once", "", 0, 0},
		{"synthetic option", nil, map[string]float64{"allow_once": .98, "reject_once": .01, "unavailable": .01}, "allow_once", "", 0, 0},
		{"choice disagrees with top", nil, map[string]float64{"allow_once": .98, "reject_once": .02}, "reject_once", "", 0, 0},
		{"unsupported alias", []approval.Option{{ID: "yes", Kind: "allow"}, {ID: "no", Kind: "deny"}}, map[string]float64{"yes": .98, "no": .02}, "yes", "", 0, 0},
		{"only one outcome", []approval.Option{{ID: "yes", Kind: "allow_once"}}, map[string]float64{"yes": 1}, "yes", "", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := guardianWindowRequest(t, "distribution")
			if tc.options != nil {
				req.Approval.Options = tc.options
			}
			// Scalar confidence never overrides the full distribution.
			confidence := .1
			response := judgment.Response{Answers: guardianScreenAnswersWith(
				judgment.Answer{Type: judgment.Choice, Choice: tc.choice, Confidence: &confidence, Probabilities: tc.probabilities},
				tc.unknown, tc.violation,
			)}
			selected, err := guardianScreenSelection(req, response)
			if tc.wantID == "" {
				if err == nil {
					t.Fatalf("unexpected selection %s", selected.ID)
				}
			} else if err != nil || selected.ID != tc.wantID {
				t.Fatalf("selected=%s err=%v", selected.ID, err)
			}
		})
	}
}

func TestGuardianDistributionLeadComparesWholeShape(t *testing.T) {
	for _, tc := range []struct {
		values map[string]float64
		want   bool
	}{
		{map[string]float64{"a": .98, "b": .02}, true},
		{map[string]float64{"a": .97, "b": .02, "c": .01}, true},
		{map[string]float64{"a": .5, "b": .5}, false},
		{map[string]float64{"a": .95, "b": .05}, false},
		{map[string]float64{"a": .96, "b": .04}, true},
		{map[string]float64{"a": .97, "b": .02}, true},
		{map[string]float64{"a": .99, "b": .02}, true},
		{map[string]float64{"a": 1}, false},
	} {
		if got := guardianDistributionDominates(tc.values, "a", 20, .9); got != tc.want {
			t.Fatalf("%v got=%v", tc.values, got)
		}
	}
}
