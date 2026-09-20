package gatewayapp

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/judgment"
	"github.com/caelis-labs/caelis/internal/kernel"
)

// Selection compares the supplied option distribution after grouping only
// canonically equivalent decisions. Probability split across permission scopes
// is not disagreement about allow versus deny. Scope is selected separately and
// never broadened by combining support for several options.
func guardianScreenSelection(req kernel.ApprovalReviewRequest, response judgment.Response) (approval.Option, error) {
	fail := func() (approval.Option, error) {
		return approval.Option{}, fmt.Errorf("guardian classifier decision distribution requires Agent review")
	}
	decision := response.Answers["decision"]
	if req.Approval == nil || decision.Type != judgment.Choice || len(decision.Probabilities) != len(req.Approval.Options)+1 {
		return fail()
	}
	if err := approval.ValidateStrictOptions(req.Approval.Options); err != nil {
		return fail()
	}
	deferred, ok := decision.Probabilities["unavailable"]
	if !ok {
		return fail()
	}
	groups := map[string]float64{"allow": 0, "deny": 0, "defer": deferred}
	kinds := make([]approval.OptionDecision, len(req.Approval.Options))
	for index, option := range req.Approval.Options {
		p, ok := decision.Probabilities[strconv.Itoa(index)]
		if !ok || !guardianScreenProbability(p) {
			return fail()
		}
		_, kind, err := approval.ResolveStrictOption(req.Approval.Options, option.ID)
		if err != nil {
			return fail()
		}
		kinds[index] = kind
		groups[string(kind)] += p
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
	winner := approval.OptionDecisionAllow
	if groups["deny"] > groups["allow"] {
		winner = approval.OptionDecisionDeny
	}
	selectedIndex := -1
	for index, option := range req.Approval.Options {
		if kinds[index] != winner {
			continue
		}
		if selectedIndex < 0 || (strings.HasSuffix(option.Kind, "_once") && !strings.HasSuffix(req.Approval.Options[selectedIndex].Kind, "_once")) ||
			(option.Kind == req.Approval.Options[selectedIndex].Kind && decision.Probabilities[strconv.Itoa(index)] > decision.Probabilities[strconv.Itoa(selectedIndex)]) {
			selectedIndex = index
		}
	}
	if selectedIndex < 0 {
		return fail()
	}
	selected := req.Approval.Options[selectedIndex]
	impact := response.Answers["consequences"]
	_, hasHighImpact := impact.Probabilities["high_impact"]
	_, hasUnknownImpact := impact.Probabilities["unknown"]
	routine := impact.Type == judgment.Choice && impact.Choice == "routine" && len(impact.Probabilities) == 3 && hasHighImpact && hasUnknownImpact && guardianDistributionDominates(impact.Probabilities, "routine", 4, .5)
	ratio, gap := 20.0, .9
	if winner == approval.OptionDecisionAllow && selected.Kind == "allow_once" && routine {
		ratio, gap = 3, .5
	}
	if !guardianDistributionDominates(groups, string(winner), ratio, gap) {
		return fail()
	}
	// Without a once option, require clear support for the exact persistent
	// option too: outcome agreement alone cannot establish permission scope.
	if !strings.HasSuffix(selected.Kind, "_once") && !guardianDistributionDominates(decision.Probabilities, strconv.Itoa(selectedIndex), 20, .9) {
		return fail()
	}
	return selected, nil
}

// Compare normalized lead and odds over the runner-up. This uses the whole
// distribution, tolerates decimal rounding, and rejects ties,
// missing data and malformed probabilities. The operating points are measured
// abstention rules, not calibrated correctness or authorization probabilities.
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
