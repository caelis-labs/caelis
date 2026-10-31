package providers

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
)

type changingSearchSource struct{ tools []tool.Tool }

func (s *changingSearchSource) Tools() []tool.Tool { return s.tools }

func TestToolSearchProviderDeclarationStable(t *testing.T) {
	source := &changingSearchSource{}
	search := toolsearch.NewSource(source)
	wire := func() map[string][]byte {
		t.Helper()
		req := &model.Request{Tools: tool.ModelSpecs([]tool.Tool{search})}
		codex, err := openAICodexRequestFromModel(req, "synthetic")
		if err != nil {
			t.Fatal(err)
		}
		responses, err := (&openAIResponsesLLM{name: "synthetic"}).buildRequest(req)
		if err != nil {
			t.Fatal(err)
		}
		anthropic, err := (&anthropicSDKLLM{name: "synthetic", maxOutputTok: 128}).buildRequest(req)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string][]byte{}
		for name, value := range map[string]any{"codex": codex.Tools, "responses": responses.Tools, "compat": fromKernelTools(model.FunctionToolDefinitions(req.Tools), true), "anthropic": anthropic.Tools, "gemini": toGeminiTools(req.Tools, false), "ollama": fromKernelTools(model.FunctionToolDefinitions(req.Tools), false)} {
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			out[name] = raw
		}
		return out
	}
	before := wire()
	for _, name := range []string{"ready", "refreshed", ""} {
		source.tools = nil
		if name != "" {
			source.tools = []tool.Tool{tool.NamedTool{Def: tool.Definition{Name: name, Metadata: map[string]any{tool.MetadataToolKind: tool.MetadataToolKindMCP, tool.MetadataMCPServer: name}}}}
		}
		for provider, after := range wire() {
			if !bytes.Equal(before[provider], after) {
				t.Fatalf("%s declaration changed after %q", provider, name)
			}
		}
	}
}
