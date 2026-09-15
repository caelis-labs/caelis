package gatewayapp

import (
	"github.com/caelis-labs/caelis/agent-sdk/model"
	sdkruntime "github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// One full-request evaluation owns capacity. User authorization is reduced only
// under physical input pressure, after recoverable non-user history. The SDK's
// model-size watermarks trigger batched eviction instead of per-pool byte caps.
func guardianFitHistory(events []*session.Event, llm model.LLM, input string, output *model.OutputSpec, current string) ([]*session.Event, bool) {
	cfg := guardianCompactionConfig(llm, output)
	evaluate := func() sdkruntime.ModelRequestBudget {
		return sdkruntime.EvaluateModelRequestBudget(llm, guardianModelRequest(events, input, output), cfg)
	}
	budget := evaluate()
	if !budget.Compaction.ShouldCompact && budget.Usage.TotalTokens <= budget.Usage.EffectiveInputBudget {
		return events, true
	}
	// Keep a cache epoch useful for several appends after crossing the watermark.
	target := max(0, budget.Usage.TotalTokens-min(budget.Usage.EffectiveInputBudget/10, 16384))
	for budget.Compaction.ShouldCompact || budget.Usage.TotalTokens > target {
		next := guardianDropOldestTurn(events, current)
		if len(next) == len(events) {
			break
		}
		events = next
		budget = evaluate()
	}
	for budget.Usage.TotalTokens > budget.Usage.EffectiveInputBudget {
		bytes := 0
		for _, e := range events {
			if guardianIsUser(e) {
				bytes += len(session.EventText(e))
			}
		}
		next := guardianTrimUsers(session.CloneEvents(events), bytes/2)
		before := budget.Usage.TotalTokens
		events = next
		budget = evaluate()
		if budget.Usage.TotalTokens >= before {
			break
		}
	}
	return events, budget.Usage.TotalTokens <= budget.Usage.EffectiveInputBudget
}
