package providers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestRetirementProviderToolPrefixExperiment(t *testing.T) {
	tools := []tool.Tool{tool.NamedTool{Def: tool.Definition{Name: tool.ToolSearchToolName, Metadata: map[string]any{tool.MetadataToolKind: tool.MetadataToolKindToolSearch}}}, tool.NewRetirementTool()}
	var defs []tool.Definition
	for index := range 16 {
		def := tool.Definition{Name: fmt.Sprintf("server__item_%02d", index), Description: "Synthetic read capability.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}, Metadata: map[string]any{tool.MetadataToolKind: tool.MetadataToolKindMCP, tool.MetadataMCPServer: "server", tool.MetadataMCPTool: fmt.Sprint(index), tool.MetadataMCPSourceFingerprint: "version-1"}}
		defs = append(defs, def)
		tools = append(tools, tool.NamedTool{Def: def})
	}
	visibility := tool.NewToolVisibility(tools)
	visibility.AdmitToolSearchResult(tool.NewToolSearchResult(defs))
	request := &model.Request{Messages: []model.Message{model.NewTextMessage(model.RoleUser, "Synthetic prefix experiment")}, Tools: visibility.ModelSpecs()}
	wire := func(request *model.Request) ([]byte, []byte) {
		openai, err := openAICodexRequestFromModel(request, "synthetic")
		if err != nil {
			t.Fatal(err)
		}
		anthropic, err := (&anthropicSDKLLM{name: "synthetic", maxOutputTok: 128}).buildRequest(request)
		if err != nil {
			t.Fatal(err)
		}
		a, err := json.Marshal(openai.Tools)
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(anthropic.Tools)
		if err != nil {
			t.Fatal(err)
		}
		return a, b
	}
	beforeA, beforeB := wire(request)
	raw, _ := json.Marshal(map[string]any{"action": "mark", "tools": visibility.Exposure().Active})
	var output map[string]any
	_ = json.Unmarshal(raw, &output)
	if err := visibility.AdmitRetirement(output); err != nil {
		t.Fatal(err)
	}
	request.Tools = visibility.ModelSpecs()
	markedA, markedB := wire(request)
	if !bytes.Equal(beforeA, markedA) || !bytes.Equal(beforeB, markedB) {
		t.Fatal("mark changed provider tool bytes")
	}
	visibility.RestoreExposure(tool.ExposureState{Version: 1})
	request.Tools = visibility.ModelSpecs()
	retiredA, retiredB := wire(request)
	if bytes.Equal(beforeA, retiredA) || bytes.Equal(beforeB, retiredB) {
		t.Fatal("committed unload left provider schemas unchanged")
	}
	t.Logf("16 deferred tools plus manager: OpenAI tools bytes %d -> %d; Anthropic tools bytes %d -> %d; mark bytes identical; actual cache tokens/cost unknown", len(beforeA), len(retiredA), len(beforeB), len(retiredB))
}
