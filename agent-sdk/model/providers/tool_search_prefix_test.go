package providers

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

func TestJSExecProviderDeclarationPreservesSourceSelector(t *testing.T) {
	req := &model.Request{Tools: tool.ModelSpecs([]tool.Tool{&codemode.Tool{Name: codemode.ToolName}})}
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
	for name, value := range map[string]any{"codex": codex.Tools, "responses": responses.Tools, "compat": fromKernelTools(model.FunctionToolDefinitions(req.Tools), true), "anthropic": anthropic.Tools, "gemini": toGeminiTools(req.Tools, false), "ollama": fromKernelTools(model.FunctionToolDefinitions(req.Tools), false)} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(raw, []byte(`"oneOf"`)) {
			t.Fatalf("%s lost the mutually exclusive source selector schema: %s", name, raw)
		}
	}
}
