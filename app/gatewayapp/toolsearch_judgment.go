package gatewayapp

import (
	"context"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/judgment"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
)

// Resolve from the activation at each search so explicit provider removal
// revokes later evaluations without replacing the ready MCP tool source.
type boundToolSearchRanker struct {
	resolve func(context.Context) (judgment.Evaluator, error)
}

func (r boundToolSearchRanker) Rank(ctx context.Context, query string, candidates []tool.Definition, limit int) ([]string, error) {
	evaluator, err := r.resolve(ctx)
	if err != nil {
		return nil, err
	}
	if evaluator == nil {
		return nil, fmt.Errorf("ToolSearch judgment is unbound")
	}
	return toolsearch.NewSemanticRanker(evaluator).Rank(ctx, query, candidates, limit)
}
