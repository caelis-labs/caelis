package modelconfig

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/judgment"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// This opt-in check uses a running gateway without changing its settings. It
// makes two synthetic chat calls and one typed evaluation, with no retries.
func TestMagpieLiveGateway(t *testing.T) {
	if os.Getenv("CAELIS_MAGPIE_LIVE") != "1" {
		t.Skip("requires an explicitly selected live Magpie gateway")
	}
	client := &http.Client{Timeout: 45 * time.Second}
	rows, err := AssembleConnect(t.Context(), ConnectRequest{Provider: "magpie", HTTPClient: client, Models: []ModelSelection{{Name: "xiaomi/mimo-v2.6-flash", MaxOutputTokens: 1024}}}, ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := BuildModel(rows[0], 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	request := &model.Request{Stream: true, Messages: []model.Message{model.NewTextMessage(model.RoleUser, "This is a synthetic integration test. Call acceptance_probe once with value OK. After its result, reply only OK.")}, Tools: []model.ToolSpec{model.NewFunctionToolSpec("acceptance_probe", "Return the supplied synthetic value", map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string", "enum": []string{"OK"}}}, "required": []string{"value"}, "additionalProperties": false})}}
	var first *model.Response
	for event, err := range resolved.Model.Generate(t.Context(), request) {
		if err != nil {
			t.Fatal(err)
		}
		if event != nil && event.Response != nil {
			first = event.Response
		}
	}
	if first == nil || len(first.Message.ToolCalls()) != 1 {
		t.Fatal("gateway did not return exactly one synthetic tool call")
	}
	call := first.Message.ToolCalls()[0]
	var args struct {
		Value string `json:"value"`
	}
	if json.Unmarshal([]byte(call.Args), &args) != nil || call.Name != "acceptance_probe" || args.Value != "OK" {
		t.Fatal("synthetic tool call was malformed")
	}
	// Rebuild from the persisted shape before continuing the canonical context.
	raw, _ := json.Marshal(SanitizePersistedConfig(rows[0]))
	var saved Config
	if err = json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	endpoint := SanitizePersistedProviderEndpoint(ProviderEndpointFromConfig(rows[0]))
	saved = MergeConfigProviderEndpoint(saved, endpoint)
	saved.HTTPClient = client
	resolved, err = BuildModel(saved, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	request.Messages = append(request.Messages, first.Message, model.NewMessage(model.RoleTool, model.NewToolResultJSONPart(call.ID, call.Name, map[string]any{"value": "OK"}, false)))
	request.DisableTools = true
	var second *model.Response
	for event, err := range resolved.Model.Generate(t.Context(), request) {
		if err != nil {
			t.Fatal(err)
		}
		if event != nil && event.Response != nil {
			second = event.Response
		}
	}
	if second == nil || strings.TrimSpace(second.Message.TextContent()) != "OK" {
		t.Fatal("gateway did not complete the persisted tool-result context")
	}
	t.Logf("chat: requested=%s returned=%s calls=2 input=%d output=%d", rows[0].Model, second.Model, first.Usage.PromptTokens+second.Usage.PromptTokens, first.Usage.CompletionTokens+second.Usage.CompletionTokens)
	rows, err = AssembleConnect(t.Context(), ConnectRequest{Provider: "magpie-judgment", HTTPClient: client, Models: []ModelSelection{{Name: "typesafe/jev-latest"}}}, ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	evaluator, err := BuildJudgment(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	answer, err := evaluator.Evaluate(t.Context(), judgment.Request{State: map[string]string{"query": "read a local source file", "candidate": "ReadFile reads a local source file"}, Questions: map[string]judgment.Question{"relevance": {Type: judgment.Score, Instructions: "Score how relevant the candidate is to the query", Criteria: []string{"irrelevant", "relevant"}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("judgment: requested=%s returned=%s calls=1 input=%d output=%d", rows[0].Model, answer.Model, answer.Usage.InputTokens, answer.Usage.OutputTokens)
}
