package runtime

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

var errRunWallTimeLimit = errors.New("agent-sdk/runtime: run wall-time limit reached")

type runBudgetContextKey struct{}

type runBudget struct {
	mu sync.Mutex

	limits     agent.RunLimits
	startedAt  time.Time
	modelCalls int
	toolCalls  int
	turns      int
	tokens     int
	costMicros int64
}

func prepareRunContext(ctx context.Context, limits agent.RunLimits, now time.Time) (context.Context, context.CancelFunc, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateRunLimits(limits); err != nil {
		return nil, nil, err
	}
	budget := &runBudget{limits: limits, startedAt: now}
	ctx = context.WithValue(ctx, runBudgetContextKey{}, budget)
	if limits.MaxWallTime > 0 {
		limited, cancel := context.WithTimeoutCause(ctx, limits.MaxWallTime, errRunWallTimeLimit)
		return limited, cancel, nil
	}
	limited, cancel := context.WithCancel(ctx)
	return limited, cancel, nil
}

func validateRunLimits(limits agent.RunLimits) error {
	if limits.MaxModelCalls < 0 || limits.MaxToolCalls < 0 || limits.MaxTurns < 0 ||
		limits.MaxWallTime < 0 || limits.MaxTokens < 0 || limits.MaxCostMicros < 0 {
		return fmt.Errorf("agent-sdk/runtime: run limits cannot be negative")
	}
	return nil
}

func runBudgetFromContext(ctx context.Context) *runBudget {
	if ctx == nil {
		return nil
	}
	budget, _ := ctx.Value(runBudgetContextKey{}).(*runBudget)
	return budget
}

func (b *runBudget) beforeModelCall() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limits.MaxModelCalls > 0 && b.modelCalls >= b.limits.MaxModelCalls {
		return newRunLimitError(agent.RunLimitModelCalls, int64(b.limits.MaxModelCalls), int64(b.modelCalls))
	}
	if b.limits.MaxTurns > 0 && b.turns >= b.limits.MaxTurns {
		return newRunLimitError(agent.RunLimitTurns, int64(b.limits.MaxTurns), int64(b.turns))
	}
	b.modelCalls++
	return nil
}

func (b *runBudget) finishModelTurn(usage model.Usage) error {
	return b.finishModelUsage(usage, true)
}

func (b *runBudget) finishModelUsage(usage model.Usage, completedTurn bool) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if completedTurn {
		b.turns++
	}
	tokens := usage.TotalTokens
	if tokens <= 0 {
		tokens = usage.PromptTokens + usage.CompletionTokens
	}
	b.tokens += max(tokens, 0)
	b.costMicros += max(usage.CostMicros, 0)
	if b.limits.MaxTokens > 0 && b.tokens > b.limits.MaxTokens {
		return newRunLimitError(agent.RunLimitTokens, int64(b.limits.MaxTokens), int64(b.tokens))
	}
	if b.limits.MaxCostMicros > 0 && b.costMicros > b.limits.MaxCostMicros {
		return newRunLimitError(agent.RunLimitCost, b.limits.MaxCostMicros, b.costMicros)
	}
	return nil
}

func (b *runBudget) beforeToolCall() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limits.MaxToolCalls > 0 && b.toolCalls >= b.limits.MaxToolCalls {
		return newRunLimitError(agent.RunLimitToolCalls, int64(b.limits.MaxToolCalls), int64(b.toolCalls))
	}
	b.toolCalls++
	return nil
}

func newRunLimitError(kind agent.RunLimitKind, limit, used int64) error {
	return &agent.RunLimitError{Kind: kind, Limit: limit, Used: used}
}

func translateRunLimitError(ctx context.Context, err error) error {
	if ctx == nil || !errors.Is(context.Cause(ctx), errRunWallTimeLimit) {
		return err
	}
	budget := runBudgetFromContext(ctx)
	if budget == nil {
		return err
	}
	budget.mu.Lock()
	limit := budget.limits.MaxWallTime
	used := time.Since(budget.startedAt)
	budget.mu.Unlock()
	if used < limit {
		used = limit
	}
	return newRunLimitError(agent.RunLimitWallTime, int64(limit), int64(used))
}

type limitedLLM struct {
	inner  model.LLM
	budget *runBudget
}

type limitedSearchLLM struct {
	*limitedLLM
}

func (l *limitedLLM) Name() string {
	if l == nil || l.inner == nil {
		return ""
	}
	return l.inner.Name()
}

func (l *limitedLLM) ContextWindowTokens() int {
	if l == nil || l.inner == nil {
		return 0
	}
	if provider, ok := l.inner.(interface{ ContextWindowTokens() int }); ok {
		return provider.ContextWindowTokens()
	}
	return 0
}

func (l *limitedLLM) ProviderName() string {
	if l == nil || l.inner == nil {
		return ""
	}
	if provider, ok := l.inner.(interface{ ProviderName() string }); ok {
		return strings.TrimSpace(provider.ProviderName())
	}
	return ""
}

func (l *limitedLLM) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		if l == nil || l.inner == nil {
			yield(nil, errors.New("model: llm is nil"))
			return
		}
		if err := l.budget.beforeModelCall(); err != nil {
			yield(nil, err)
			return
		}
		for event, err := range l.inner.Generate(ctx, model.CloneRequest(req)) {
			if err != nil {
				yield(nil, translateRunLimitError(ctx, err))
				return
			}
			if event != nil && event.Response != nil && event.TurnComplete {
				if limitErr := l.budget.finishModelTurn(event.Usage); limitErr != nil {
					yield(nil, limitErr)
					return
				}
			}
			if !yield(event, nil) {
				return
			}
		}
	}
}

func (l *limitedSearchLLM) SearchWeb(ctx context.Context, req model.WebSearchRequest) (model.WebSearchResponse, error) {
	if l == nil || l.limitedLLM == nil || l.inner == nil {
		return model.WebSearchResponse{}, errors.New("model: llm is nil")
	}
	if err := l.budget.beforeModelCall(); err != nil {
		return model.WebSearchResponse{}, err
	}
	searcher, ok := l.inner.(model.WebSearcher)
	if !ok {
		return model.WebSearchResponse{}, errors.New("model: web search is unavailable for this provider")
	}
	resp, err := searcher.SearchWeb(ctx, req)
	if err != nil {
		return model.WebSearchResponse{}, translateRunLimitError(ctx, err)
	}
	if err := l.budget.finishModelUsage(resp.Usage, false); err != nil {
		return model.WebSearchResponse{}, err
	}
	return resp, nil
}

func limitsRequireRuntimeInstrumentation(limits agent.RunLimits) bool {
	return limits.MaxModelCalls > 0 || limits.MaxToolCalls > 0 || limits.MaxTurns > 0 ||
		limits.MaxTokens > 0 || limits.MaxCostMicros > 0
}

func wrapModelForRunLimits(llm model.LLM, budget *runBudget) model.LLM {
	if llm == nil || budget == nil {
		return llm
	}
	wrapped := &limitedLLM{inner: llm, budget: budget}
	if _, ok := llm.(model.WebSearcher); ok {
		return &limitedSearchLLM{limitedLLM: wrapped}
	}
	return wrapped
}

type limitedTool struct {
	inner  tool.Tool
	budget *runBudget
}

func (t limitedTool) Definition() tool.Definition {
	if t.inner == nil {
		return tool.Definition{}
	}
	return t.inner.Definition()
}

func (t limitedTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if t.inner == nil {
		return tool.Result{}, errors.New("tool: tool is nil")
	}
	if err := t.budget.beforeToolCall(); err != nil {
		return tool.Result{}, err
	}
	result, err := t.inner.Call(ctx, call)
	return result, translateRunLimitError(ctx, err)
}

func wrapToolsForRunLimits(tools []tool.Tool, budget *runBudget) []tool.Tool {
	if len(tools) == 0 || budget == nil {
		return tools
	}
	out := make([]tool.Tool, 0, len(tools))
	for _, item := range tools {
		if item == nil {
			continue
		}
		out = append(out, limitedTool{inner: item, budget: budget})
	}
	return out
}
