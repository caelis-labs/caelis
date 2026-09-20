package gatewayapp

import (
	"math"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/judgment"
)

func withAnswer(answers map[string]judgment.Answer, id string, answer judgment.Answer) map[string]judgment.Answer {
	answers[id] = answer
	return answers
}

// The two Noul evidence gates abstain unless the outcome distribution and the
// supporting facts agree. These cutoffs are conservative operating rules, not
// calibrated correctness probabilities. Every endpoint is inclusive, and any
// missing, malformed, nonfinite or out-of-range gate answer defers to the Agent
// instead of settling. The denial path is the only one that may settle despite a
// material information gap.
func TestGuardianScreenEvidenceGatesDecisiveOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers map[string]judgment.Answer
		wantID  string
	}{
		// Allow needs unknown<=.2 and violation<=.1; both endpoints are inclusive.
		{name: "allow at upper endpoints", answers: guardianScreenAnswers("allow_once", 1, .2, .1), wantID: "allow_once"},
		{name: "allow below endpoints", answers: guardianScreenAnswers("allow_once", .98, .05, .05), wantID: "allow_once"},
		{name: "allow just beyond unknown endpoint", answers: guardianScreenAnswers("allow_once", 1, .2000001, 0)},
		{name: "allow just beyond violation endpoint", answers: guardianScreenAnswers("allow_once", 1, 0, .1000001)},
		// Deny needs visible_violation>=.9; material_unknown cannot veto a settled
		// violation, but it must still be a valid Noul answer.
		{name: "deny at violation endpoint", answers: guardianScreenAnswers("reject_once", 1, 0, .9), wantID: "reject_once"},
		{name: "deny with unknown=1 at violation endpoint", answers: guardianScreenAnswers("reject_once", 1, 1, .9), wantID: "reject_once"},
		{name: "unknown=1 with full deny support still settles", answers: guardianScreenAnswers("reject_once", 1, 1, 1), wantID: "reject_once"},
		{name: "deny just below violation endpoint", answers: guardianScreenAnswers("reject_once", 1, 0, .8999999)},
		// Conflicting, gray or unsupported evidence abstains.
		{name: "allow distribution with violation conflict", answers: guardianScreenAnswers("allow_once", .98, 0, .9)},
		{name: "allow distribution with material gap", answers: guardianScreenAnswers("allow_once", .98, .5, 0)},
		{name: "dominant allow .97 masked by unknown", answers: guardianScreenAnswers("allow_once", .97, .9, 0)},
		{name: "deny distribution without established violation", answers: guardianScreenAnswers("reject_once", .98, 0, 0)},
		{name: "both gates gray under allow", answers: guardianScreenAnswers("allow_once", .98, .5, .5)},
		{name: "both gates gray under deny", answers: guardianScreenAnswers("reject_once", .98, .5, .5)},
		// Malformed evidence must never be read as a decisive zero or one.
		{name: "no evidence answers", answers: map[string]judgment.Answer{
			"decision": choiceAnswer("allow_once", 1),
		}},
		{name: "missing material unknown", answers: map[string]judgment.Answer{
			"decision":          choiceAnswer("allow_once", 1),
			"visible_violation": noulAnswer(0),
		}},
		{name: "missing visible violation", answers: map[string]judgment.Answer{
			"decision":         choiceAnswer("allow_once", 1),
			"material_unknown": noulAnswer(0),
		}},
		{name: "extra answer", answers: map[string]judgment.Answer{
			"decision":          choiceAnswer("allow_once", 1),
			"material_unknown":  noulAnswer(0),
			"visible_violation": noulAnswer(1),
			"severity":          noulAnswer(0),
		}},
		{name: "material unknown wrong type", answers: withAnswer(guardianScreenAnswers("allow_once", 1, 0, 0), "material_unknown", judgment.Answer{Type: judgment.Choice, Choice: "true"})},
		{name: "visible violation wrong type", answers: withAnswer(guardianScreenAnswers("allow_once", 1, 0, 0), "visible_violation", judgment.Answer{Type: judgment.Score})},
		{name: "material unknown nil value", answers: withAnswer(guardianScreenAnswers("allow_once", 1, 0, 0), "material_unknown", judgment.Answer{Type: judgment.Noul})},
		{name: "visible violation nil value", answers: withAnswer(guardianScreenAnswers("allow_once", 1, 0, 0), "visible_violation", judgment.Answer{Type: judgment.Noul})},
		{name: "material unknown NaN", answers: withAnswer(guardianScreenAnswers("allow_once", 1, 0, 0), "material_unknown", noulAnswer(math.NaN()))},
		{name: "material unknown infinity", answers: withAnswer(guardianScreenAnswers("allow_once", 1, 0, 0), "material_unknown", noulAnswer(math.Inf(1)))},
		{name: "visible violation NaN", answers: withAnswer(guardianScreenAnswers("allow_once", 1, 0, 0), "visible_violation", noulAnswer(math.NaN()))},
		{name: "visible violation infinity", answers: withAnswer(guardianScreenAnswers("allow_once", 1, 0, 0), "visible_violation", noulAnswer(math.Inf(-1)))},
		{name: "material unknown above range", answers: withAnswer(guardianScreenAnswers("allow_once", 1, 0, 0), "material_unknown", noulAnswer(1.01))},
		{name: "material unknown below range", answers: withAnswer(guardianScreenAnswers("allow_once", 1, 0, 0), "material_unknown", noulAnswer(-.01))},
		{name: "visible violation above range", answers: withAnswer(guardianScreenAnswers("allow_once", 1, 0, 0), "visible_violation", noulAnswer(1.01))},
		{name: "visible violation below range", answers: withAnswer(guardianScreenAnswers("allow_once", 1, 0, 0), "visible_violation", noulAnswer(-.01))},
		{name: "malformed unknown blocks deny", answers: withAnswer(guardianScreenAnswers("reject_once", 1, 1, 1), "material_unknown", noulAnswer(math.NaN()))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := guardianWindowRequest(t, "evidence")
			selected, err := guardianScreenSelection(req, judgment.Response{Answers: tc.answers})
			if tc.wantID == "" {
				if err == nil {
					t.Fatalf("unsupported evidence settled on %s", selected.ID)
				}
				return
			}
			if err != nil || selected.ID != tc.wantID {
				t.Fatalf("selected=%s err=%v", selected.ID, err)
			}
		})
	}
}
