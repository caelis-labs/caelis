package toolsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestAgentSearchCoversThousandCandidatesAcrossModelBatches(t *testing.T) {
	const target = "server_999__lookup"
	definitions := make([]tool.Definition, 1000)
	for i := range definitions {
		definitions[i] = tool.Definition{
			Name:        fmt.Sprintf("server_%03d__lookup", i),
			Description: strings.Repeat("synthetic description ", 10),
		}
	}
	seen := make(map[string]int, len(definitions))
	firstPassCalls := 0
	llm := &selectionModel{respond: func(_ int, req *model.Request) (*model.Response, error) {
		var input struct {
			Candidates []agentCandidate `json:"candidates"`
		}
		if err := json.Unmarshal([]byte(req.Messages[0].TextContent()), &input); err != nil {
			t.Fatal(err)
		}
		if len(req.Messages[0].TextContent()) > maxCatalogBytes {
			t.Fatal("model request exceeded bounded catalog size")
		}
		chosen := input.Candidates[0].Name
		if len(input.Candidates) > 16 {
			firstPassCalls++
			for _, candidate := range input.Candidates {
				seen[candidate.Name]++
			}
		}
		for _, candidate := range input.Candidates {
			if candidate.Name == target {
				chosen = target
			}
		}
		selection, _ := json.Marshal(map[string]any{"tools": []string{chosen}})
		return &model.Response{Message: model.NewTextMessage(model.RoleAssistant, string(selection)), TurnComplete: true, Status: model.ResponseStatusCompleted}, nil
	}}
	got, err := NewAgentRanker().Rank(t.Context(), "find the last server", definitions, 1, SearchModel{
		Model: llm, ReadSchema: func(context.Context, string) (tool.Definition, error) {
			t.Fatal("schema read was not requested")
			return tool.Definition{}, nil
		},
	})
	if err != nil || len(got) != 1 || got[0] != target {
		t.Fatalf("selection=%v error=%v, want %s", got, err, target)
	}
	if firstPassCalls < 2 || len(seen) != len(definitions) {
		t.Fatalf("first pass batches=%d covered=%d, want all 1000", firstPassCalls, len(seen))
	}
	for name, count := range seen {
		if count != 1 {
			t.Fatalf("first pass candidate %q seen %d times", name, count)
		}
	}
}

func TestAgentSearchFailsExplicitlyWhenOneCandidateCannotFit(t *testing.T) {
	llm := &selectionModel{}
	_, err := NewAgentRanker().Rank(t.Context(), "record", []tool.Definition{{
		Name: "too_large", Description: strings.Repeat("x", maxCatalogBytes),
	}}, 1, SearchModel{Model: llm, ReadSchema: func(context.Context, string) (tool.Definition, error) {
		return tool.Definition{}, nil
	}})
	if err == nil || !strings.Contains(err.Error(), "one candidate exceeds") || len(llm.requests) != 0 {
		t.Fatalf("oversized candidate error=%v model requests=%d", err, len(llm.requests))
	}
}

func TestAgentSearchBatchCannotInspectCandidateFromAnotherBatch(t *testing.T) {
	definitions := make([]tool.Definition, 1000)
	for i := range definitions {
		definitions[i] = tool.Definition{Name: fmt.Sprintf("server_%03d__lookup", i), Description: strings.Repeat("synthetic description ", 10)}
	}
	llm := &selectionModel{respond: func(_ int, _ *model.Request) (*model.Response, error) {
		return &model.Response{Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
			ID: "foreign-batch", Name: inspectSchemaToolName, Args: `{"name":"server_999__lookup"}`,
		}}, ""), TurnComplete: true}, nil
	}}
	reads := 0
	_, err := NewAgentRanker().Rank(t.Context(), "lookup", definitions, 1, SearchModel{
		Model: llm, ReadSchema: func(context.Context, string) (tool.Definition, error) {
			reads++
			return tool.Definition{}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "catalog batch") || reads != 0 || len(llm.requests) != 1 {
		t.Fatalf("cross-batch schema error=%v reads=%d requests=%d", err, reads, len(llm.requests))
	}
}
