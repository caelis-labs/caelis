package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolutil"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

type codeModeBusinessModel struct {
	model.LLM
	requests []*model.Request
}

func (m *codeModeBusinessModel) Capabilities() model.Capabilities {
	caps, _ := model.CapabilitiesOf(m.LLM)
	return caps
}

func (m *codeModeBusinessModel) Generate(ctx context.Context, request *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.requests = append(m.requests, model.CloneRequest(request))
	return m.LLM.Generate(ctx, request)
}

// MCP, Goja, Runtime and provider HTTP encoding are real; the provider responses
// are deterministic loopback fixtures and do not use a live model or account.
func TestCodeModeMCPBusinessContract(t *testing.T) {
	const chain = `const first=await live.value({});const second=await Consume({name:first.data.name});return {summary:second.data.summary,media:first.media};`
	for _, tc := range []struct {
		name, code, want string
		image            bool
	}{
		{"chain_pure", chain, "checked:business", false},
		{"chain_mixed", chain, "checked:business", true},
		{"direct_resource", `return await live.value({});`, "https://example.invalid/report", true},
		{"structured_no_schema", `const r=await live.no_schema({});return r.data.name;`, "business", false},
		{"direct", `return await live.value({});`, "9223372036854775807", true},
		{"retained_complete", `return retainedBody;`, "9223372036854775807", true},
		{"retained_media", `return retainedBody.media[0];`, "image/png", true},
		{"retained_copy", `return {...retainedBody.media[0]};`, "handle", false},
		{"retained_delete", `const parsed=retainedBody.data;parsed.content=retainedBody.media;delete parsed.content;return {nested:{...parsed}};`, "business", false},
		{"dependency", `const r=await live.value({});return {selected:r.data.name};`, "selected", false},
		{"nested", `return {record:await live.value({})};`, "record", true},
		{"selected_media", `const r=await live.value({});return {image:r.media[0]};`, "image", true},
		{"copied_media", `const r=await live.value({});return {...r.media[0]};`, "handle", false},
		{"fallback", `const r=await live.fallback({});if(r.data!==undefined||JSON.parse(r.text).name!=='not-business'||r.media.length!==1)throw Error('Body content contract');return r;`, "not-business", true},
		{"delete_content", `const response=await live.value({});const r=response.data;r.content=response.media;delete r.content;return r;`, "business", false},
		{"nested_delete_content", `const response=await live.value({});const r=response.data;r.content=response.media;delete r.content;return {record:r};`, "record", false},
		{"copy_delete_content", `const response=await live.value({});const r=response.data;r.content=response.media;delete r.content;return {...r};`, "business", false},
		{"copied_fallback_handle", `const r=await live.fallback({});return {media:r.media.map(p=>({...p}))};`, "media", false},
		{"nested_copied_fallback_handle", `const r=await live.fallback({});return {record:{media:r.media.map(p=>({...p}))}};`, "record", false},
		{"copied_fallback_references", `return {...await live.fallback({})};`, "media", true},
		{"selected_after_delete", `const response=await live.value({});const r=response.data;r.content=response.media;delete r.content;return {record:r,image:response.media[0]};`, "record", true},
		{"schema", `const s=await tools.search({name:'live__value'});if(s.output_format!=='body'||!s.output_schema.properties.name)throw Error('output contract missing');return await live.value({});`, "9223372036854775807", true},
		{"invalid_schema", `try{await live.invalid({});throw Error('resolved invalid output');}catch(e){if(!e.result||e.result!==e.reply||typeof e.result.text!=='function'||!e.result.text().includes('outputSchema')||e.result.text().includes('MCP arguments'))throw Error('invalid diagnostic contract');let rejected=false;try{e.result.json()}catch(parseError){rejected=true}if(!rejected)throw Error('promoted unverified structured content');return {caught:true,diagnostic:e.result.text()};}`, "unverified", false},
		{"missing_schema_result", `return await live.missing({});`, "outputSchema requires structuredContent", false},
		{"null_schema_result", `return await live.null_result({});`, "structuredContent must be a JSON object", false},
		{"non_object_result", `return await live.non_object({});`, "structuredContent must be a JSON object", false},
		{"exact_echo_only", `const r=await live.echo({});if(Object.keys(r.data).length!==0||r.text!=="before\n{ }\nafter")throw Error('changed content: '+r.text);return r;`, "before", false},
		{"failed_schema", `return await live.failed({});`, "business failure", false},
		{"failed_missing_schema_result", `return await live.failed_missing({});`, "business failure", false},
		{"raw_json_collision", `const r=await live.collision({});if(r.data.raw_json!=='business-raw')throw Error('lost business field');return r;`, "business-raw", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var store session.Service
			var active session.Session
			var replayRoot string
			if tc.name == "direct_resource" {
				var root string
				store, active, root = codeModeStore(t)
				replayRoot = filepath.Join(root, "sessions")
			} else {
				store, active = newTestSessionService(t, "business-contract")
			}
			var bitmap bytes.Buffer
			if err := png.Encode(&bitmap, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
				t.Fatal(err)
			}
			var effects atomic.Int32
			server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "business", Version: "1"}, nil)
			for _, name := range []string{"value", "fallback", "invalid", "failed", "collision", "no_schema", "missing", "null_result", "non_object", "echo", "failed_missing"} {
				def := &mcpsdk.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}
				switch name {
				case "fallback", "no_schema", "non_object":
				case "echo":
					def.OutputSchema = map[string]any{"type": "object"}
				default:
					def.OutputSchema = map[string]any{"type": "object", "required": []string{"name", "n"}, "properties": map[string]any{"name": map[string]any{"type": "string"}, "n": map[string]any{"type": "integer"}}}
				}
				server.AddTool(def, func(_ context.Context, _ *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
					effects.Add(1)
					structured := map[string]any{"name": "business", "n": json.Number("9223372036854775807")}
					if name == "invalid" {
						structured["n"] = "unverified"
					}
					if name == "collision" {
						structured["raw_json"] = "business-raw"
					}
					content := []mcpsdk.Content{
						&mcpsdk.TextContent{Text: `{"name":"not-business"}`},
						&mcpsdk.ImageContent{Data: bitmap.Bytes(), MIMEType: "image/png"},
					}
					switch tc.name {
					case "chain_pure":
						content = nil
					case "chain_mixed", "direct_resource":
						content = append(content, &mcpsdk.ResourceLink{URI: "https://example.invalid/report", Name: "report"})
					}
					result := &mcpsdk.CallToolResult{Content: content, StructuredContent: structured}
					switch name {
					case "fallback", "missing":
						result.StructuredContent = nil
					case "null_result":
						result.StructuredContent = json.RawMessage(`null`)
					case "non_object":
						result.StructuredContent = []any{"not an object"}
					case "echo":
						result.StructuredContent = map[string]any{}
						result.Content = []mcpsdk.Content{&mcpsdk.TextContent{Text: "before"}, &mcpsdk.TextContent{Text: "{}"}, &mcpsdk.TextContent{Text: "{ }"}, &mcpsdk.TextContent{Text: "after"}}
					case "failed", "failed_missing":
						result.IsError = true
						result.Content = []mcpsdk.Content{&mcpsdk.TextContent{Text: "business failure"}}
						result.StructuredContent = nil
						if name == "failed" {
							result.StructuredContent = map[string]any{"reason": "failure payload"}
						}
					}
					return result, nil
				})
			}
			endpoint := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil))
			defer endpoint.Close()
			manager, err := mcp.NewManager(t.Context(), []mcp.ServerSpec{{PluginID: "fixture", Name: "live", Transport: mcp.TransportStreamableHTTP, URL: endpoint.URL}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			<-manager.Initialized()
			stages := []string{tc.code}
			if strings.HasPrefix(tc.name, "retained_") {
				stages = []string{`let retainedBody=await live.value({});return {loaded:true};`, tc.code}
			}
			var mu sync.Mutex
			var requests [][]byte
			maxRequests := len(stages) + 1
			if replayRoot != "" {
				maxRequests++
			}
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					http.Error(w, "read", 500)
					return
				}
				mu.Lock()
				requests = append(requests, raw)
				n := len(requests)
				mu.Unlock()
				if n > maxRequests {
					t.Error("unexpected extra model request")
					http.Error(w, "extra", 500)
					return
				}
				output := []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "done"}}}}
				if n <= len(stages) {
					args, _ := json.Marshal(map[string]any{"code": stages[n-1]})
					id := "business"
					if n < len(stages) {
						id = "load-body"
					}
					output = []any{map[string]any{"type": "function_call", "name": codemode.ToolName, "call_id": id, "arguments": string(args)}}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "object": "response", "model": "fixture", "status": "completed", "output": output, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}})
			}))
			defer provider.Close()
			llm, err := providers.New(providers.Config{Provider: "fixture", API: model.APIOpenAIResponses, Model: "fixture", BaseURL: provider.URL, HTTPClient: provider.Client(), Auth: providers.AuthConfig{Type: model.AuthNone}, ImageInput: true, ContextWindowTokens: 200000})
			if err != nil {
				t.Fatal(err)
			}
			driver := &codeModeBusinessModel{LLM: llm}
			var consumed atomic.Int32
			consume := tool.NamedTool{Def: tool.Definition{Name: "Consume", InputSchema: map[string]any{"type": "object"}}, Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
				var args struct{ Name string }
				if err := json.Unmarshal(call.Input, &args); err != nil || args.Name != "business" {
					return tool.Result{}, fmt.Errorf("unexpected Consume argument: %s", call.Input)
				}
				consumed.Add(1)
				return toolutil.JSONResult(map[string]any{"summary": "checked:" + args.Name})
			}}
			spec := agent.AgentSpec{Model: driver, Tools: []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}, consume}, DeferredTools: manager}
			runCodeMode(t, store, active, spec, nil, nil)
			if strings.HasPrefix(tc.name, "chain_") && consumed.Load() != 1 {
				t.Fatalf("downstream calls=%d; want 1", consumed.Load())
			}
			if effects.Load() != 1 || len(driver.requests) != len(stages)+1 {
				t.Fatalf("effects=%d requests=%d", effects.Load(), len(driver.requests))
			}
			visible := errorDeliveryModelResult(t, driver.requests[len(stages)], "business")
			wantError := strings.HasSuffix(tc.name, "_result") || tc.name == "invalid_schema" || tc.name == "failed_schema"
			if visible.IsError != wantError {
				t.Fatalf("unexpected script disposition: %s", errorDeliveryJSON(t, visible.Content))
			}
			raw, _ := json.Marshal(visible.Content)
			if !strings.Contains(string(raw), tc.want) {
				t.Fatalf("missing %s: %s", tc.want, raw)
			}
			if strings.HasPrefix(tc.name, "failed_") && bytes.Contains(raw, []byte("outputSchema")) {
				t.Fatalf("MCP error received success-output validation: %s", raw)
			}
			if tc.name == "failed_schema" && !bytes.Contains(raw, []byte("failure payload")) {
				t.Fatalf("MCP error lost its structured diagnostic: %s", raw)
			}
			images := 0
			for _, p := range visible.Content {
				if p.Media != nil {
					images++
					if p.Media.Source.Data != base64.StdEncoding.EncodeToString(bitmap.Bytes()) {
						t.Fatal("changed image bytes")
					}
				}
			}
			wantImages := 0
			if tc.image {
				wantImages = 1
			}
			if images != wantImages {
				t.Fatalf("images=%d expected=%v: %s", images, tc.image, raw)
			}
			mu.Lock()
			wire := append([][]byte(nil), requests...)
			mu.Unlock()
			if len(wire) != len(stages)+1 {
				t.Fatalf("provider HTTP requests=%d", len(wire))
			}
			var next struct {
				Input []struct {
					Type   string          `json:"type"`
					CallID string          `json:"call_id"`
					Output json.RawMessage `json:"output"`
				} `json:"input"`
			}
			if err := json.Unmarshal(wire[len(stages)], &next); err != nil {
				t.Fatal(err)
			}
			httpImages := 0
			found := false
			for _, input := range next.Input {
				if input.Type != "function_call_output" || input.CallID != "business" {
					continue
				}
				found = true
				if !bytes.Contains(input.Output, []byte(tc.want)) {
					t.Fatalf("provider HTTP output lacks %q: %s", tc.want, input.Output)
				}
				// Text-only tool output is a string; multimodal output is an array.
				if len(input.Output) > 0 && input.Output[0] == '"' {
					continue
				}
				var contentParts []struct {
					Type     string `json:"type"`
					ImageURL string `json:"image_url"`
				}
				if err := json.Unmarshal(input.Output, &contentParts); err != nil {
					t.Fatal(err)
				}
				for _, content := range contentParts {
					if content.Type == "input_image" {
						httpImages++
						if content.ImageURL != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(bitmap.Bytes()) {
							t.Fatal("next provider HTTP changed original PNG bytes")
						}
					}
				}
			}
			if !found || httpImages != images {
				t.Fatalf("provider HTTP images=%d; model request images=%d", httpImages, images)
			}
			if dir := os.Getenv("CAELIS_CODE_MODE_BUSINESS_ARTIFACTS"); dir != "" {
				dir = filepath.Join(dir, tc.name)
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				for i, request := range wire {
					if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("request-%02d.json", i+1)), request, 0600); err != nil {
						t.Fatal(err)
					}
				}
				for name, raw := range map[string][]byte{"first.request.json": wire[0], "next.request.json": wire[len(stages)], "source.png": bitmap.Bytes()} {
					if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if replayRoot != "" {
				reopened := sessionfile.NewStore(sessionfile.Config{RootDir: replayRoot})
				probe := &codeModeBusinessModel{LLM: llm}
				spec.Model = probe
				runCodeMode(t, reopened, active, spec, nil, nil)
				want := append(model.CloneMessages(driver.requests[len(stages)].Messages),
					model.NewTextMessage(model.RoleAssistant, "done"),
					model.NewTextMessage(model.RoleUser, "synthetic Code Mode task"))
				if len(probe.requests) != 1 || !reflect.DeepEqual(probe.requests[0].Messages, want) || effects.Load() != 1 {
					t.Fatalf("reopened context differs from original context or the tool was dispatched again (requests=%d calls=%d)", len(probe.requests), effects.Load())
				}
			}
			t.Logf("next provider HTTP: images=%d, original PNG bytes=%d", httpImages, bitmap.Len())
		})
	}
}
