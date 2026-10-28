package providers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestAnthropicCacheBreakpointsPreserveDispatchedBoundaries(t *testing.T) {
	user := func(s string) model.Message { return model.NewTextMessage(model.RoleUser, s) }
	assistant := func(s string) model.Message { return model.NewTextMessage(model.RoleAssistant, s) }
	for _, tc := range []struct {
		name     string
		messages []model.Message
		want     []int
		tools    bool
	}{
		{"initial", []model.Message{user("first")}, []int{0}, true},
		{"ordinary", []model.Message{user("first"), assistant("answer"), user("second")}, []int{0, 2}, true},
		{"frozen", []model.Message{user("first"), assistant("answer"), user("second"), user("compact")}, []int{0, 2, 3}, false},
		{"completed", []model.Message{user("first"), assistant("answer"), user("second"), assistant("final"), user("compact")}, []int{2, 4}, true},
		{"queued", []model.Message{user("first"), assistant("answer"), user("q1"), user("q2"), user("q3")}, []int{0, 3, 4}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := &model.Request{Instructions: []model.Part{model.NewTextPart("system")}, Messages: tc.messages, Tools: []model.ToolSpec{model.NewFunctionToolSpec("lookup", "lookup", map[string]any{"type": "object"})}}
			before, _ := json.Marshal(req)
			llm := newAnthropic(Config{Provider: "anthropic", Model: "claude-sonnet-4-6"}, "").(*anthropicSDKLLM)
			params, err := llm.buildRequest(req)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(params)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Messages []struct{ Content []map[string]any } `json:"messages"`
				System   []map[string]any                     `json:"system"`
				Tools    []map[string]any                     `json:"tools"`
			}
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			count := 0
			check := func(block map[string]any) bool {
				c, ok := block["cache_control"]
				if ok {
					count++
					if !reflect.DeepEqual(c, map[string]any{"type": "ephemeral", "ttl": "5m"}) {
						t.Fatalf("cache control = %#v", c)
					}
				}
				return ok
			}
			var marked []int
			for i, m := range wire.Messages {
				for _, b := range m.Content {
					if check(b) {
						marked = append(marked, i)
					}
				}
			}
			if !reflect.DeepEqual(marked, tc.want) {
				t.Fatalf("boundaries %v, want %v; %s", marked, tc.want, raw)
			}
			if !check(wire.System[0]) || check(wire.Tools[0]) != tc.tools || count > 4 {
				t.Fatalf("fixed prefix/count: %s", raw)
			}
			after, _ := json.Marshal(req)
			if string(before) != string(after) {
				t.Fatal("mutated canonical request")
			}
			compatible := newAnthropicWithDefaults(Config{Provider: "deepseek", Model: "deepseek-flash"}, "", anthropicProviderDefaults{baseURL: defaultAnthropicBaseURL}).(*anthropicSDKLLM)
			other, err := compatible.buildRequest(req)
			if err != nil {
				t.Fatal(err)
			}
			otherRaw, _ := json.Marshal(other)
			var obj map[string]any
			_ = json.Unmarshal(otherRaw, &obj)
			if string(otherRaw) == string(raw) {
				t.Fatal("native cache policy leaked into compatible entry")
			}
		})
	}
}

func TestAnthropicCacheUsageSparseDeltaPresence(t *testing.T) {
	var start anthropic.Usage
	if err := json.Unmarshal([]byte(`{"input_tokens":0,"cache_read_input_tokens":6000,"cache_creation_input_tokens":300,"cache_creation":{"ephemeral_5m_input_tokens":200,"ephemeral_1h_input_tokens":100},"output_tokens":0}`), &start); err != nil {
		t.Fatal(err)
	}
	merged := mergeAnthropicUsage(start, `{"output_tokens":10,"cache_creation":{"ephemeral_5m_input_tokens":0},"cache_creation_input_tokens":100}`)
	u := anthropicUsageToKernel(merged)
	if u.PromptTokens != 100 || u.CachedInputTokens != 6000 || u.CacheWriteTokens != 100 || u.CacheWrite5mTokens != 0 || u.CacheWrite1hTokens != 100 || u.TotalTokens != 6110 || !u.CacheWrite5mTokensReported || !u.CacheWrite1hTokensReported || !u.CachedInputTokensReported || !u.CacheWriteTokensReported {
		t.Fatalf("usage = %+v", u)
	}
	var zero anthropic.Usage
	_ = json.Unmarshal([]byte(`{"cache_creation_input_tokens":0}`), &zero)
	z := anthropicUsageToKernel(zero)
	if !z.IsReported() || !z.CacheWriteTokensReported || z.CachedInputTokensReported {
		t.Fatalf("zero presence = %+v", z)
	}
	if anthropicUsageToKernel(anthropic.Usage{}).IsReported() {
		t.Fatal("missing usage reported as zero")
	}
}

func TestAnthropicFactoryCachePolicy(t *testing.T) {
	for _, api := range []APIType{APIAnthropic, APIAnthropicCompatible} {
		t.Run(string(api), func(t *testing.T) {
			var body []byte
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				body, _ = io.ReadAll(req.Body)
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"type":"message","role":"assistant","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}`)), Request: req}, nil
			})
			factory := NewFactory()
			if err := factory.Register(Config{Alias: "cache-route", API: api, Provider: string(api), Model: "claude-sonnet-4-6", Auth: AuthConfig{Type: AuthAPIKey, Token: "fixture"}, HTTPClient: &http.Client{Transport: transport}}); err != nil {
				t.Fatal(err)
			}
			llm, err := factory.NewByAlias("cache-route")
			if err != nil {
				t.Fatal(err)
			}
			for _, err := range model.Generate(t.Context(), llm, &model.Request{Messages: []model.Message{model.NewTextMessage(model.RoleUser, "source")}}) {
				if err != nil {
					t.Fatal(err)
				}
			}
			if got := bytes.Contains(body, []byte(`"cache_control"`)); got != (api == APIAnthropic) {
				t.Fatalf("API %s cache markers=%v", api, got)
			}
		})
	}
}

// Compare the content and request settings that determine prefix reuse, not a
// textual startsWith of the HTTP envelope. This proves lookup eligibility, not
// a server cache hit or a monetary saving.
func anthropicContentPrefix(t *testing.T, body map[string]any, last int) []byte {
	t.Helper()
	raw, _ := json.Marshal(body)
	var copy map[string]any
	_ = json.Unmarshal(raw, &copy)
	delete(copy, "stream")
	copy["messages"] = copy["messages"].([]any)[:last+1]
	var strip func(any)
	strip = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			delete(v, "cache_control")
			for _, child := range v {
				strip(child)
			}
		case []any:
			for _, child := range v {
				strip(child)
			}
		}
	}
	strip(copy)
	raw, err := json.Marshal(copy)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestAnthropicActualHTTPPrefixesForAsyncIdleAndQueuedMessages(t *testing.T) {
	var captured map[string]any
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		captured = nil
		if err := json.NewDecoder(request.Body).Decode(&captured); err != nil {
			return nil, err
		}
		response := `{"id":"fixture","type":"message","role":"assistant","model":"fixture","content":[{"type":"text","text":"complete fixture answer"}],"stop_reason":"end_turn","usage":{"input_tokens":0,"cache_read_input_tokens":6000,"cache_creation_input_tokens":0,"output_tokens":5}}`
		header := http.Header{"Content-Type": []string{"application/json"}}
		if stream, _ := captured["stream"].(bool); stream {
			header.Set("Content-Type", "text/event-stream")
			response = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"fixture\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"fixture\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":0,\"cache_read_input_tokens\":6000,\"cache_creation_input_tokens\":0,\"output_tokens\":0}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
		}
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(response)), Request: request}, nil
	})
	llm := newAnthropic(Config{Provider: "anthropic", Model: "claude-sonnet-4-6", HTTPClient: &http.Client{Transport: transport}}, "fixture")
	send := func(t *testing.T, req *model.Request) map[string]any {
		t.Helper()
		var final *model.Response
		for event, err := range model.Generate(t.Context(), llm, req) {
			if err != nil {
				t.Fatal(err)
			}
			if event != nil && event.Response != nil {
				final = event.Response
			}
		}
		if final == nil || final.Usage.TotalTokens != 6005 || !final.Usage.CacheWriteTokensReported || final.Usage.CacheWriteTokens != 0 || !final.Usage.CachedInputTokensReported {
			t.Fatalf("stream usage/presence=%+v", final)
		}
		return captured
	}
	frozen := &model.Request{Instructions: []model.Part{model.NewTextPart("stable system"), model.NewTextPart("stable policy")}, Tools: []model.ToolSpec{model.NewFunctionToolSpec("lookup", "stable tool", map[string]any{"type": "object"})}, Messages: []model.Message{model.NewTextMessage(model.RoleUser, "first"), model.NewTextMessage(model.RoleAssistant, "answer"), model.NewTextMessage(model.RoleUser, "actual sent tail")}}
	reasoning := model.NewReasoningPart("preserved reasoning", model.ReasoningVisibilityVisible)
	reasoning.Reasoning.Replay = &model.ReplayMeta{Provider: "anthropic", Kind: anthropicReplayKindThinkingSignature, Token: "preserved-signature"}
	frozen.Messages[1].Parts = append([]model.Part{reasoning}, frozen.Messages[1].Parts...)
	frozen.Messages[2].Parts = append(frozen.Messages[2].Parts, model.NewMediaPart(model.MediaModalityImage, model.MediaSource{Kind: model.MediaSourceURL, URI: "https://example.test/immutable.png"}, "image/png", "source image"))
	first := send(t, frozen)
	expected := anthropicContentPrefix(t, first, 2)
	for _, variant := range []string{"async", "idle", "queued"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", variant, stream), func(t *testing.T) {
				req := model.CloneRequest(frozen)
				req.Stream = stream
				switch variant {
				case "idle":
					req.Messages = append(req.Messages, model.NewTextMessage(model.RoleAssistant, "new final answer"))
				case "queued":
					req.Messages = append(req.Messages, model.NewTextMessage(model.RoleAssistant, "new tool step"))
					for i := range 25 {
						req.Messages = append(req.Messages, model.NewTextMessage(model.RoleUser, fmt.Sprintf("queued %d", i)))
					}
				}
				req.Messages = append(req.Messages, model.NewTextMessage(model.RoleUser, "compact the preceding context"))
				body := send(t, req)
				if !bytes.Equal(expected, anthropicContentPrefix(t, body, 2)) {
					t.Fatal("actual HTTP prefix changed during compaction append")
				}
				blocks := body["messages"].([]any)[2].(map[string]any)["content"].([]any)
				if _, ok := blocks[len(blocks)-1].(map[string]any)["cache_control"]; !ok {
					t.Fatal("original dispatch boundary lost its explicit breakpoint")
				}
				encoded, _ := json.Marshal(body)
				if bytes.Count(encoded, []byte(`"cache_control"`)) > 4 {
					t.Fatal("too many breakpoints")
				}
				for _, changed := range []string{"system", "tool", "history"} {
					negative := model.CloneRequest(req)
					switch changed {
					case "system":
						negative.Instructions[0] = model.NewTextPart("different policy")
					case "tool":
						negative.Tools[0] = model.NewFunctionToolSpec("lookup", "different schema", map[string]any{"type": "string"})
					case "history":
						negative.Messages[0] = model.NewTextMessage(model.RoleUser, "edited history")
					}
					if bytes.Equal(expected, anthropicContentPrefix(t, send(t, negative), 2)) {
						t.Fatalf("claimed prefix reuse after %s changed", changed)
					}
				}
			})
		}
	}
}

func TestAnthropicCacheMarksOuterToolResult(t *testing.T) {
	llm := newAnthropic(Config{Model: "claude-sonnet-4-6"}, "fixture").(*anthropicSDKLLM)
	req := &model.Request{Messages: []model.Message{model.NewTextMessage(model.RoleUser, "source"), {Role: model.RoleAssistant, Parts: []model.Part{model.NewToolUsePart("one", "lookup", json.RawMessage(`{}`)), model.NewToolUsePart("two", "lookup", json.RawMessage(`{}`))}}, {Role: model.RoleTool, Parts: []model.Part{model.NewToolResultJSONPart("one", "lookup", map[string]any{"text": "first"}, false)}}, {Role: model.RoleTool, Parts: []model.Part{model.NewToolResultJSONPart("two", "lookup", map[string]any{"text": "second"}, false)}}}}
	params, err := llm.buildRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err = json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	messages := body["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("tool results were not one wire user: %s", raw)
	}
	blocks := messages[2].(map[string]any)["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("tool batch changed: %s", raw)
	}
	for i, block := range blocks {
		result := block.(map[string]any)
		if result["tool_use_id"] != []string{"one", "two"}[i] {
			t.Fatal("tool/result pairing changed")
		}
		if i == 1 && result["cache_control"] == nil {
			t.Fatal("last outer result has no marker")
		}
		nested, _ := json.Marshal(result["content"])
		if bytes.Contains(nested, []byte("cache_control")) {
			t.Fatal("cache marker entered result contents")
		}
	}
}
