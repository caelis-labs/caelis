package gatewayapp

import (
	"math"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/judgment"
)

func TestGuardianScreeningUsesOptionDistributionAndNarrowScope(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		options                []approval.Option
		probabilities          map[string]float64
		choice, impact, wantID string
		confidence             float64
	}{
		{"routine clear lead", nil, map[string]float64{"0": .85, "1": .04, "unavailable": .11}, "0", "routine", "allow_once", .1},
		{"allow deny disagreement", nil, map[string]float64{"0": .55, "1": .4, "unavailable": .05}, "0", "routine", "", 1},
		{"material missing facts", nil, map[string]float64{"0": .65, "1": .01, "unavailable": .34}, "0", "routine", "", 1},
		{"high impact needs stronger lead", nil, map[string]float64{"0": .85, "1": .04, "unavailable": .11}, "0", "high_impact", "", 1},
		{"unknown impact needs stronger lead", nil, map[string]float64{"0": .85, "1": .04, "unavailable": .11}, "0", "unknown", "", 1},
		{"high impact clear lead", nil, map[string]float64{"0": .98, "1": .01, "unavailable": .01}, "0", "high_impact", "allow_once", .1},
		{"deny clear lead", nil, map[string]float64{"0": .01, "1": .98, "unavailable": .01}, "1", "routine", "reject_once", .1},
		{"same decision split scopes", []approval.Option{{ID: "once", Kind: "allow_once"}, {ID: "always", Kind: "allow_always"}, {ID: "no", Kind: "reject_once"}}, map[string]float64{"0": .40, "1": .50, "2": .04, "unavailable": .06}, "1", "routine", "once", .1},
		{"reordered renamed options", []approval.Option{{ID: "a", Kind: "reject_once"}, {ID: "b", Kind: "allow_always"}, {ID: "c", Kind: "allow_once"}}, map[string]float64{"0": .04, "1": .50, "2": .40, "unavailable": .06}, "1", "routine", "c", .1},
		{"persistent scope remains strict", []approval.Option{{ID: "persist", Kind: "allow_always"}, {ID: "no", Kind: "reject_once"}}, map[string]float64{"0": .85, "1": .04, "unavailable": .11}, "0", "routine", "", 1},
		{"missing distribution", nil, nil, "0", "routine", "", 1},
		{"NaN", nil, map[string]float64{"0": math.NaN(), "1": 0, "unavailable": 0}, "0", "routine", "", 1},
		{"wrong total", nil, map[string]float64{"0": .9, "1": .4, "unavailable": 0}, "0", "routine", "", 1},
		{"unknown option", nil, map[string]float64{"0": .95, "extra": .04, "unavailable": .01}, "0", "routine", "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := guardianWindowRequest(t, "distribution")
			if tc.options != nil {
				req.Approval.Options = tc.options
			}
			impact := map[string]float64{"routine": 0, "high_impact": 0, "unknown": 0}
			impact[tc.impact] = 1
			response := judgment.Response{Answers: map[string]judgment.Answer{
				"decision":     {Type: judgment.Choice, Choice: tc.choice, Confidence: &tc.confidence, Probabilities: tc.probabilities},
				"consequences": {Type: judgment.Choice, Choice: tc.impact, Probabilities: impact},
			}}
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
		{map[string]float64{"a": .8, "b": .2, "c": 0}, true},
		{map[string]float64{"a": .8, "b": .1, "c": .1}, true},
		{map[string]float64{"a": .5, "b": .5}, false},
		{map[string]float64{"a": .65, "b": .35}, false},
		{map[string]float64{"a": .9, "b": .09}, true},
		{map[string]float64{"a": .92, "b": .09}, true},
		{map[string]float64{"a": 1}, false},
	} {
		if got := guardianDistributionDominates(tc.values, "a", 4, .5); got != tc.want {
			t.Fatalf("%v got=%v", tc.values, got)
		}
	}
}

func TestGuardianScreeningMalformedConsequencesCannotRelaxDecision(t *testing.T) {
	for _, values := range []map[string]float64{
		{"routine": .99, "unexpected": .01},
		{"routine": .99, "high_impact": .01},
		{"routine": .99, "high_impact": 0, "unexpected": .01},
		{"routine": 1.01, "high_impact": -.01, "unknown": 0},
		{"routine": math.NaN(), "high_impact": 0, "unknown": 0},
	} {
		req := guardianWindowRequest(t, "malformed-impact")
		response := judgment.Response{Answers: map[string]judgment.Answer{
			"decision":     {Type: judgment.Choice, Choice: "0", Probabilities: map[string]float64{"0": .85, "1": .04, "unavailable": .11}},
			"consequences": {Type: judgment.Choice, Choice: "routine", Probabilities: values},
		}}
		if selected, err := guardianScreenSelection(req, response); err == nil {
			t.Fatalf("malformed consequences %v selected %s", values, selected.ID)
		}
	}
}
