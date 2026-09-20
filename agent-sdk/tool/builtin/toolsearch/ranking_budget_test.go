package toolsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/judgment"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type rankingEvaluatorFunc func(context.Context, judgment.Request) (judgment.Response, error)

func (rankingEvaluatorFunc) Name() string { return "ranking-fixture" }
func (f rankingEvaluatorFunc) Evaluate(ctx context.Context, r judgment.Request) (judgment.Response, error) {
	return f(ctx, r)
}

func TestSemanticRankingPacksLongMultilingualCandidates(t *testing.T) {
	var definitions []tool.Definition
	for i := range 60 {
		definitions = append(definitions, tool.Definition{Name: fmt.Sprintf("tool-%02d", i), Description: strings.Repeat("搜索文件与日历事件", 100)})
	}
	calls, seen := 0, map[string]bool{}
	evaluator := rankingEvaluatorFunc(func(_ context.Context, r judgment.Request) (judgment.Response, error) {
		calls++
		raw, err := json.Marshal(r)
		if err != nil || len(raw) > 24000 {
			t.Fatalf("request bytes=%d err=%v", len(raw), err)
		}
		var payload struct {
			State struct{ Candidates []struct{ Name string } }
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		answers := map[string]judgment.Answer{}
		for i, c := range payload.State.Candidates {
			if seen[c.Name] {
				t.Fatalf("duplicate candidate %s", c.Name)
			}
			seen[c.Name] = true
			score := 0.0
			if c.Name == "tool-59" {
				score = 2
			}
			answers[strconv.Itoa(i)] = judgment.Answer{Type: judgment.Score, Score: &score}
		}
		return judgment.Response{Answers: answers}, nil
	})
	got, err := NewSemanticRanker(evaluator).Rank(t.Context(), "查找资料", definitions, 3)
	if err != nil || len(got) != 1 || got[0] != "tool-59" || len(seen) != 60 || calls <= 3 {
		t.Fatalf("names=%v seen=%d calls=%d err=%v", got, len(seen), calls, err)
	}
}

func TestSemanticRankingRejectsOversizedQueryAndInvalidScores(t *testing.T) {
	definitions := []tool.Definition{{Name: "tool", Description: "search"}}
	evaluator := rankingEvaluatorFunc(func(context.Context, judgment.Request) (judgment.Response, error) {
		t.Fatal("oversized query dispatched")
		return judgment.Response{}, nil
	})
	if _, err := NewSemanticRanker(evaluator).Rank(t.Context(), strings.Repeat("x", 24000), definitions, 1); err == nil {
		t.Fatal("oversized query accepted")
	}
	for _, score := range []float64{-1, 3, math.NaN(), math.Inf(1)} {
		evaluator = rankingEvaluatorFunc(func(context.Context, judgment.Request) (judgment.Response, error) {
			return judgment.Response{Answers: map[string]judgment.Answer{"0": {Type: judgment.Score, Score: &score}}}, nil
		})
		if _, err := NewSemanticRanker(evaluator).Rank(t.Context(), "search", definitions, 1); err == nil {
			t.Fatalf("invalid score %g accepted", score)
		}
	}
}
