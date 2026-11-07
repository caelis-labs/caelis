package runtime

import (
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

// A static MCP Tool is legal assembled input. Discovering it must not duplicate
// that same registered target when the next isolated VM restores eligibility.
func TestReviewStaticMCPDiscoveryAcrossVMs(t *testing.T) {
	store, active, _ := codeModeStore(t)
	s := h1Directory(2)
	m := &h1Model{s: s, main: func(i int, _ *model.Request) []model.ToolCall {
		switch i {
		case 0:
			return h1Script(`output.text(await tools.describe("a__target"));`)
		case 1:
			return h1Script(`output.text(await tools.call("a__target",{value:1}));`)
		}
		return nil
	}}
	spec := agent.AgentSpec{Model: m, Tools: []tool.Tool{s.items[0], h1Search(s), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}
	events, _ := runCodeMode(t, store, active, spec, nil, nil)
	if s.snapshot().Effects != 1 {
		for _, e := range events {
			if e.Tool != nil && e.Tool.Name == codemode.ToolName && e.Tool.Status == "failed" {
				t.Logf("failed parent output: %+v", e.Tool.Output)
			}
		}
		t.Fatalf("static discovered target must execute once in next VM; stats=%+v", s.snapshot())
	}
}
