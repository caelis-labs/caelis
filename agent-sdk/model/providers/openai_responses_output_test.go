package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestOpenAIResponsesOfficialStrictFunctionSchemas(t *testing.T) {
	var payload map[string]any
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"test-model","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`)
	}))
	defer server.Close()

	llm := newOpenAIResponses(Config{
		Provider:   "openai",
		API:        APIOpenAI,
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")
	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "use tools")},
		Tools: []model.ToolSpec{
			{
				Kind: model.ToolSpecKindFunction,
				Function: &model.FunctionToolSpec{
					Name:        "closed",
					Description: "closed strict tool",
					Strict:      true,
					Parameters: map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]any{
							"path": map[string]any{"type": "string"},
						},
						"required": []any{"path"},
					},
				},
			},
			{
				Kind: model.ToolSpecKindFunction,
				Function: &model.FunctionToolSpec{
					Name:        "optional",
					Description: "requested strict with nullable optional field",
					Strict:      true,
					Parameters: map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]any{
							"path":  map[string]any{"type": "string"},
							"limit": map[string]any{"type": "integer"},
							"mode":  map[string]any{"type": "string", "enum": []string{"fast", "safe"}},
							"include": map[string]any{
								"anyOf": []any{
									map[string]any{"type": "string"},
									map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
								},
							},
						},
						"required": []any{"path"},
					},
				},
			},
			{
				Kind: model.ToolSpecKindFunction,
				Function: &model.FunctionToolSpec{
					Name:        "open",
					Description: "requested strict but schema is open",
					Strict:      true,
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"path": map[string]any{"type": "string"},
						},
						"required": []any{"path"},
					},
				},
			},
		},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
	}

	tools, _ := payload["tools"].([]any)
	if len(tools) != 3 {
		t.Fatalf("tools len = %d, want 3: %#v", len(tools), payload["tools"])
	}
	closedFunction := tools[0].(map[string]any)
	if got := closedFunction["strict"]; got != true {
		t.Fatalf("closed function strict = %#v, want true", got)
	}
	optionalFunction := tools[1].(map[string]any)
	if got := optionalFunction["strict"]; got != true {
		t.Fatalf("optional function strict = %#v, want true", got)
	}
	optionalParams := optionalFunction["parameters"].(map[string]any)
	required, _ := optionalParams["required"].([]any)
	if got := strings.Join(stringSliceFromProviderAny(required), ","); got != "include,limit,mode,path" {
		t.Fatalf("optional required = %#v, want include,limit,mode,path", required)
	}
	optionalProps := optionalParams["properties"].(map[string]any)
	limitType, _ := optionalProps["limit"].(map[string]any)["type"].([]any)
	if got := strings.Join(stringSliceFromProviderAny(limitType), ","); got != "integer,null" {
		t.Fatalf("optional limit type = %#v, want integer,null", limitType)
	}
	modeEnum, _ := optionalProps["mode"].(map[string]any)["enum"].([]any)
	if len(modeEnum) != 3 || modeEnum[0] != "fast" || modeEnum[1] != "safe" || modeEnum[2] != nil {
		t.Fatalf("optional mode enum = %#v, want fast/safe/null", modeEnum)
	}
	includeAnyOf, _ := optionalProps["include"].(map[string]any)["anyOf"].([]any)
	if len(includeAnyOf) != 3 {
		t.Fatalf("optional include anyOf = %#v, want string/array/null", includeAnyOf)
	}
	includeNull, _ := includeAnyOf[2].(map[string]any)
	if got := includeNull["type"]; got != "null" {
		t.Fatalf("optional include null variant = %#v, want type null", includeNull)
	}
	openFunction := tools[2].(map[string]any)
	if got := openFunction["strict"]; got != false {
		t.Fatalf("open function strict = %#v, want explicit false", got)
	}
}

func TestOpenAIResponsesOfficialPriorityServiceTier(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		tier model.ServiceTier
		omit bool
	}{
		{name: "priority", tier: model.ServiceTierPriority},
		{name: "omitted when unset", omit: true},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var payload map[string]any
			server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/responses" {
					http.NotFound(w, r)
					return
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Errorf("decode request payload: %v", err)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"model":"gpt-5.4","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`)
			}))
			defer server.Close()

			llm := newOpenAIResponses(Config{
				Provider:   "openai",
				API:        APIOpenAI,
				Model:      "gpt-5.4",
				BaseURL:    server.URL,
				HTTPClient: server.Client(),
				Timeout:    2 * time.Second,
			}, "token")
			for _, err := range llm.Generate(context.Background(), &model.Request{
				Messages:    []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
				ServiceTier: tt.tier,
			}) {
				if err != nil {
					t.Fatalf("Generate() error = %v", err)
				}
			}
			got, ok := payload["service_tier"]
			if tt.omit {
				if ok {
					t.Fatalf("service_tier = %#v, want omitted", got)
				}
				return
			}
			if !ok || got != "priority" {
				t.Fatalf("service_tier = %#v, %v, want priority", got, ok)
			}
		})
	}
}
