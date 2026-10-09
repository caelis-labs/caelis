package toolsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/judgment"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type rankingEvaluatorFunc func(context.Context, judgment.Request) (judgment.Response, error)

func (rankingEvaluatorFunc) Name() string { return "ranking-fixture" }
func (f rankingEvaluatorFunc) Evaluate(ctx context.Context, r judgment.Request) (judgment.Response, error) {
	return f(ctx, r)
}

func TestSemanticRankingCoversCompleteCatalogAcrossBatches(t *testing.T) {
	var definitions []tool.Definition
	for i := range 60 {
		definitions = append(definitions, tool.Definition{Name: fmt.Sprintf("tool-%02d", i), Description: "Find a named resource", InputSchema: map[string]any{"secret": "SCHEMA_MUST_STAY_PRIVATE"}})
	}
	calls, answered := 0, map[string]bool{}
	var mu sync.Mutex
	evaluator := rankingEvaluatorFunc(func(_ context.Context, r judgment.Request) (judgment.Response, error) {
		raw, err := json.Marshal(r)
		if err != nil {
			return judgment.Response{}, err
		}
		if len(raw) > 24000 || strings.Contains(string(raw), "SCHEMA_MUST_STAY_PRIVATE") {
			return judgment.Response{}, fmt.Errorf("invalid Jev request bytes=%d", len(raw))
		}
		var payload struct {
			State struct{ Candidates []struct{ Name string } }
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return judgment.Response{}, err
		}
		if len(payload.State.Candidates) == 0 || len(payload.State.Candidates) > maxJevBatchQuestions {
			return judgment.Response{}, fmt.Errorf("batch catalog length=%d", len(payload.State.Candidates))
		}
		answers := map[string]judgment.Answer{}
		for key := range r.Questions {
			index, err := strconv.Atoi(key)
			if err != nil {
				return judgment.Response{}, err
			}
			name := payload.State.Candidates[index].Name
			mu.Lock()
			if answered[name] {
				mu.Unlock()
				return judgment.Response{}, fmt.Errorf("duplicate question for %s", name)
			}
			answered[name] = true
			mu.Unlock()
			score := 0.0
			if name == "tool-59" {
				score = 2
			}
			answers[key] = judgment.Answer{Type: judgment.Score, Score: &score}
		}
		mu.Lock()
		calls++
		mu.Unlock()
		return judgment.Response{Answers: answers}, nil
	})
	got, err := NewSemanticRanker(evaluator).Rank(t.Context(), "查找资料", definitions, 3, SearchModel{})
	if err != nil || len(got) != 1 || got[0] != "tool-59" || len(answered) != 60 || calls != 3 {
		t.Fatalf("names=%v seen=%d calls=%d err=%v", got, len(answered), calls, err)
	}
}

func TestSemanticRankingCoversThousandCandidatesWithBoundedParallelBatches(t *testing.T) {
	definitions := make([]tool.Definition, 1000)
	for i := range definitions {
		definitions[i] = tool.Definition{Name: fmt.Sprintf("server_%03d__lookup", i), Description: strings.Repeat("synthetic capability ", 12)}
	}
	var active, peak atomic.Int32
	seen := make(map[string]bool, len(definitions))
	var mu sync.Mutex
	evaluator := rankingEvaluatorFunc(func(ctx context.Context, request judgment.Request) (judgment.Response, error) {
		now := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); now > old && !peak.CompareAndSwap(old, now); old = peak.Load() {
		}
		select {
		case <-ctx.Done():
			return judgment.Response{}, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
		raw, err := json.Marshal(request)
		if err != nil {
			return judgment.Response{}, err
		}
		if len(raw) > maxJevRequestBytes {
			return judgment.Response{}, fmt.Errorf("Jev request bytes=%d", len(raw))
		}
		var payload struct {
			State struct{ Candidates []agentCandidate }
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return judgment.Response{}, err
		}
		answers := make(map[string]judgment.Answer, len(request.Questions))
		for key := range request.Questions {
			index, err := strconv.Atoi(key)
			if err != nil || index >= len(payload.State.Candidates) {
				return judgment.Response{}, fmt.Errorf("invalid question index %q", key)
			}
			name := payload.State.Candidates[index].Name
			mu.Lock()
			if seen[name] {
				mu.Unlock()
				return judgment.Response{}, fmt.Errorf("duplicate candidate %s", name)
			}
			seen[name] = true
			mu.Unlock()
			score := 0.0
			if name == "server_999__lookup" {
				score = 2
			}
			answers[key] = judgment.Answer{Type: judgment.Score, Score: &score}
		}
		return judgment.Response{Answers: answers}, nil
	})
	got, err := NewSemanticRanker(evaluator).Rank(t.Context(), "last server", definitions, 3, SearchModel{})
	if err != nil || len(got) != 1 || got[0] != "server_999__lookup" || len(seen) != 1000 || peak.Load() < 2 || peak.Load() > maxJevParallel {
		t.Fatalf("selection=%v covered=%d peak=%d error=%v", got, len(seen), peak.Load(), err)
	}
}

func TestSemanticRankingRejectsOversizedInputAndInvalidScores(t *testing.T) {
	definitions := []tool.Definition{{Name: "tool", Description: "search"}}
	evaluator := rankingEvaluatorFunc(func(context.Context, judgment.Request) (judgment.Response, error) {
		t.Fatal("oversized input dispatched")
		return judgment.Response{}, nil
	})
	if _, err := NewSemanticRanker(evaluator).Rank(t.Context(), strings.Repeat("x", 24000), definitions, 1, SearchModel{}); err == nil {
		t.Fatal("oversized input accepted")
	}
	for _, score := range []float64{-1, 3, math.NaN(), math.Inf(1)} {
		evaluator = rankingEvaluatorFunc(func(context.Context, judgment.Request) (judgment.Response, error) {
			return judgment.Response{Answers: map[string]judgment.Answer{"0": {Type: judgment.Score, Score: &score}}}, nil
		})
		if _, err := NewSemanticRanker(evaluator).Rank(t.Context(), "search", definitions, 1, SearchModel{}); err == nil {
			t.Fatalf("invalid score %g accepted", score)
		}
	}
}
