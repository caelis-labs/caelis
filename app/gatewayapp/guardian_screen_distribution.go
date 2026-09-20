package gatewayapp

import (
	"fmt"
	"math"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/judgment"
	"github.com/caelis-labs/caelis/internal/kernel"
)

// Screening supports the four ACP kinds, never guesses from names or IDs, and
// needs both outcomes. Other valid option sets go directly to the Agent.
func guardianScreenOptions(payload *approval.Payload) error {
	fail := func() error {
		return &guardianScreenError{reason: "options_not_screenable", cause: fmt.Errorf("classifier requires canonical allow and reject options (at most 255)")}
	}
	if payload == nil || len(payload.Options) > 255 || approval.ValidateACPOptions(payload.Options) != nil {
		return fail()
	}
	allow, deny := false, false
	for _, option := range payload.Options {
		switch option.Kind {
		case "allow_once", "allow_always":
			allow = true
		case "reject_once", "reject_always":
			deny = true
		}
	}
	if !allow || !deny {
		return fail()
	}
	return nil
}

// Selection requires both a decisive outcome distribution and supporting
// evidence judgments. Scope is selected separately: prefer once and never
// broaden authority by combining option mass.
func guardianScreenSelection(req kernel.ApprovalReviewRequest, response judgment.Response) (approval.Option, error) {
	fail := func() (approval.Option, error) {
		return approval.Option{}, fmt.Errorf("guardian classifier decision or evidence requires Agent review")
	}
	decision := response.Answers["decision"]
	if guardianScreenOptions(req.Approval) != nil || len(response.Answers) != 3 || decision.Type != judgment.Choice || len(decision.Probabilities) != len(req.Approval.Options) {
		return fail()
	}
	groups := map[string]float64{"allow": 0, "deny": 0}
	kinds := make([]string, len(req.Approval.Options))
	for index, option := range req.Approval.Options {
		p, ok := decision.Probabilities[option.ID]
		if !ok || !guardianScreenProbability(p) {
			return fail()
		}
		kind := "deny"
		if option.Kind == "allow_once" || option.Kind == "allow_always" {
			kind = "allow"
		}
		kinds[index] = kind
		groups[kind] += p
	}
	// Validate the complete answer, including the provider's stated top option.
	chosen, ok := decision.Probabilities[decision.Choice]
	if !ok {
		return fail()
	}
	for _, p := range decision.Probabilities {
		if p > chosen {
			return fail()
		}
	}
	winner := "allow"
	if groups["deny"] > groups["allow"] {
		winner = "deny"
	}
	if !guardianDistributionDominates(groups, winner, 20, .9) || !guardianScreenEvidenceSupports(winner, response.Answers) {
		return fail()
	}
	selectedIndex := -1
	for index, option := range req.Approval.Options {
		if kinds[index] != winner {
			continue
		}
		once := option.Kind == "allow_once" || option.Kind == "reject_once"
		if selectedIndex < 0 {
			selectedIndex = index
			continue
		}
		selected := req.Approval.Options[selectedIndex]
		selectedOnce := selected.Kind == "allow_once" || selected.Kind == "reject_once"
		if (once && !selectedOnce) || (once == selectedOnce && decision.Probabilities[option.ID] > decision.Probabilities[selected.ID]) {
			selectedIndex = index
		}
	}
	selected := req.Approval.Options[selectedIndex]
	// Without a once option, outcome agreement cannot establish persistent scope:
	// require a decisive lead for the exact persistent option as well.
	if selected.Kind != "allow_once" && selected.Kind != "reject_once" && !guardianDistributionDominates(decision.Probabilities, selected.ID, 20, .9) {
		return fail()
	}
	return selected, nil
}

// Compare normalized lead and odds over the runner-up. This uses the whole
// distribution, tolerates decimal rounding, and rejects ties, missing data and
// malformed probabilities. These are abstention rules, not calibrated
// correctness or authorization probabilities.
func guardianDistributionDominates(values map[string]float64, selected string, ratio, gap float64) bool {
	p, ok := values[selected]
	if !ok || len(values) < 2 {
		return false
	}
	total, runner := 0.0, 0.0
	for key, value := range values {
		if !guardianScreenProbability(value) {
			return false
		}
		total += value
		if key != selected && value > runner {
			runner = value
		}
	}
	if math.Abs(total-1) > .01+1e-9 || p <= runner {
		return false
	}
	return (p-runner)/total+1e-9 >= gap && (runner == 0 || p/runner+1e-9 >= ratio)
}

func guardianScreenProbability(p float64) bool {
	return !math.IsNaN(p) && !math.IsInf(p, 0) && p >= 0 && p <= 1
}
