package runtime

import (
	"fmt"
	"os"
	"reflect"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/prefixusage"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
)

// Deterministic exposure/prefix experiment, not a model cache benchmark. The
// local estimator is deliberately labelled; no billed/cache tokens are invented.
func TestRetirementLongSessionPrefixExperiment(t *testing.T) {
	if os.Getenv("CAELIS_MCP_RETIREMENT_EXPERIMENT") != "1" {
		t.Skip("set CAELIS_MCP_RETIREMENT_EXPERIMENT=1 for the 80-turn experiment")
	}
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled_%v", enabled), func(t *testing.T) {
			const turns = 80
			source := &runtimeDeferredSource{}
			for index := range turns {
				name := fmt.Sprintf("catalog__item_%03d", index)
				source.ready = append(source.ready, tool.NamedTool{Def: tool.Definition{Name: name, Description: "Synthetic capability for prefix measurement only.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string", "description": "The synthetic document query."}}}, Metadata: map[string]any{tool.MetadataToolKind: tool.MetadataToolKindMCP, tool.MetadataMCPServer: "catalog", tool.MetadataMCPTool: name, tool.MetadataMCPSourceFingerprint: fmt.Sprintf("source-%d", index)}}})
			}
			store, active := newTestSessionService(t, fmt.Sprintf("long-retirement-%v", enabled))
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, PolicyRegistry: retirementPolicy(), DefaultPolicyMode: "retirement-test"})
			if err != nil {
				t.Fatal(err)
			}
			search := toolsearch.NewSource(source, toolsearch.NewLexicalRanker())
			peak, peakTokens, totalAdmitted := 0, 0, 0
			for turn := range turns {
				binding := tool.BindingForDefinition(source.ready[turn].Definition())
				script := &retirementScriptModel{steps: func(index int, _ *model.Request) []model.ToolCall {
					if index == 0 {
						return []model.ToolCall{{ID: "find", Name: tool.ToolSearchToolName, Args: fmt.Sprintf(`{"query":%q,"limit":1}`, binding.Name)}}
					}
					if index == 1 && enabled {
						return []model.ToolCall{{ID: "mark", Name: tool.RetireToolName, Args: retirementMarkArgs(binding)}}
					}
					return nil
				}}
				run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: fmt.Sprintf("synthetic task %d", turn), AgentSpec: agent.AgentSpec{Model: script, Tools: []tool.Tool{search}, DeferredTools: source}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := drainRunnerEvents(t, run.Handle); err != nil {
					t.Fatal(err)
				}
				after := script.requests[1]
				found := false
				for _, spec := range after.Tools {
					if spec.Function != nil && spec.Function.Name == binding.Name {
						found = true
					}
				}
				if found {
					totalAdmitted++
				}
				peak = max(peak, len(after.Tools))
				peakTokens = max(peakTokens, prefixusage.ForRequest(after).Tokens)
				if enabled && !reflect.DeepEqual(after.Tools, script.requests[2].Tools) {
					t.Fatal("mark changed schemas before checkpoint")
				}
				if (turn+1)%16 == 0 {
					t.Logf("completed_turns=%d peak_schemas=%d", turn+1, peak)
					if result, err := rt.Compact(t.Context(), CompactRequest{SessionRef: active.SessionRef, Model: &compactionRequestCaptureModel{}}); err != nil || !result.Compacted {
						t.Fatalf("compact=%+v %v", result, err)
					}
				}
			}
			probe := &retirementScriptModel{steps: func(int, *model.Request) []model.ToolCall { return nil }}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "measure", AgentSpec: agent.AgentSpec{Model: probe, Tools: []tool.Tool{search}, DeferredTools: source}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := drainRunnerEvents(t, run.Handle); err != nil {
				t.Fatal(err)
			}
			wantPeak, wantAdmitted, wantFinal := 65, 64, 65
			if enabled {
				wantPeak, wantAdmitted, wantFinal = 18, 80, 1
			}
			if peak != wantPeak || totalAdmitted != wantAdmitted || len(probe.requests[0].Tools) != wantFinal {
				t.Fatalf("peak=%d admitted=%d final=%d", peak, totalAdmitted, len(probe.requests[0].Tools))
			}
			t.Logf("turns=%d checkpoint_interval=16 enabled=%v peak_schemas=%d final_schemas=%d admitted_unique=%d peak_prefix_estimated_tokens=%d cache_read_tokens=unknown cost=unknown", turns, enabled, peak, len(probe.requests[0].Tools), totalAdmitted, peakTokens)
		})
	}
}
