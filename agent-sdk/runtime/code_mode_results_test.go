package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolutil"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"image"
	"image/color"
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
)

func TestCodeModeProjectionDistinguishesNullFromText(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		structured bool
		want       string
	}{
		{name: "text-only"},
		{name: "structured-null", structured: true, want: "null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &codeModeHost{limits: codemode.DefaultLimits(), codeModeResults: newCodeModeResults()}
			result := tool.Result{Content: []tool.ContentBlock{tool.Text(`{"not":"data"}`)}}
			if tc.structured {
				result.Content = append(result.Content, tool.StructuredContent{Value: json.RawMessage(`null`)})
			}
			def := tool.Definition{OutputSchema: map[string]any{"type": "null"}}
			reply, err := h.project("call", result, codemode.OutcomeSucceeded, def)
			if err != nil || string(reply.Value) != tc.want || reply.BodyText != `{"not":"data"}` || len(reply.Parts) != len(result.Content) {
				t.Fatalf("unexpected projection: %+v, %v", reply, err)
			}
		})
	}
}

func TestCodeModeProjectionFailureDoesNotRetainMedia(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"ok":true}`)
	image := tool.Image{Data: []byte("original-bytes"), MIMEType: "image/png", Name: "fixture"}
	result := tool.Result{Content: []tool.ContentBlock{
		image,
		tool.StructuredContent{Value: raw},
	}}
	for _, failure := range []string{"duplicate_structured", "structured_budget"} {
		t.Run(failure, func(t *testing.T) {
			limits := codemode.DefaultLimits()
			retained := len(image.Data) + len(image.MIMEType) + len(image.Name) + len(raw)
			limits.RetainedBytes = retained * 2
			h := &codeModeHost{limits: limits, codeModeResults: newCodeModeResults()}
			first, err := h.project("first", result, codemode.OutcomeSucceeded, tool.Definition{OutputSchema: map[string]any{"type": "object"}})
			if err != nil {
				t.Fatal(err)
			}
			invalid := result
			invalid.Content = append(result.Content, tool.StructuredContent{Value: raw})
			if failure == "structured_budget" {
				invalid = result
				// Original parts fit exactly; the additional structured Value does not.
				h.limits.ReplyBytes = len("failed")*6 + 128 + 256 + len("image/png")*6 + len(raw)*6 + 64
			}
			for i := range 5 {
				if _, err := h.project("failed", invalid, codemode.OutcomeSucceeded, tool.Definition{OutputSchema: map[string]any{"type": "object"}}); err == nil {
					t.Fatalf("failure %d accepted", i)
				}
				if len(h.handles) != 1 || len(h.replyParts) != 1 || h.retained != retained {
					t.Errorf("failure %d changed retained state: handles=%d replies=%d bytes=%d", i, len(h.handles), len(h.replyParts), h.retained)
				}
			}
			h.limits.ReplyBytes = limits.ReplyBytes
			second, err := h.project("second", result, codemode.OutcomeSucceeded, tool.Definition{OutputSchema: map[string]any{"type": "object"}})
			if err != nil || h.retained != limits.RetainedBytes || len(h.handles) != 2 || len(h.replyParts) != 2 {
				t.Fatalf("legal projection at exact retention limit: handles=%d replies=%d bytes=%d err=%v", len(h.handles), len(h.replyParts), h.retained, err)
			}
			if _, err := h.project("overflow", tool.Result{Content: []tool.ContentBlock{tool.Text("x")}}, codemode.OutcomeSucceeded, tool.Definition{OutputSchema: map[string]any{"type": "object"}}); err == nil || !strings.Contains(err.Error(), "retention budget") {
				t.Fatalf("one byte over budget: %v", err)
			}
			if h.retained != limits.RetainedBytes || len(h.handles) != 2 || len(h.replyParts) != 2 {
				t.Fatal("budget failure changed retained state")
			}
			for _, reply := range []codemode.Reply{first, second} {
				parts, err := h.returnedParts(codemode.Output{Reply: true, CallID: reply.CallID})
				if err != nil || len(parts) != 2 || !reflect.DeepEqual(parts[0], image) {
					t.Fatalf("original media lost after failures: %+v %v", parts, err)
				}
			}
		})
	}
}

func TestCodeModeNativeBusinessResult(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, code, wantContent string
		wantErrorCode           string
	}{
		{"read", `
const {data: file} = await Read({path: "input.txt"});
return {content: file.content, done: !file.has_more};`, "1: business-value", ""},
		{"caught_failure", `
let failure;
try {
  await Read({path: "missing.txt"});
} catch (e) {
  failure = {code: e.code, message: e.message};
}
const {data: file} = await Read({path: "input.txt"});
return {content: file.content, done: !file.has_more, failure};`, "1: business-value", "not_found"},
		{"grep_then_read", `
const {data: matches} = await Grep({pattern: "follow-up-value", include: "*.txt"});
if (matches.hits.length !== 1) throw Error("expected one matching line");
const hit = matches.hits[0];
const {data: file} = await Read({path: hit.path, offset: hit.line - 1, limit: 1});
return {content: file.content, done: !file.has_more};`, "2: follow-up-value", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			backend, err := host.New(host.Config{CWD: root})
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()
			read, err := filesystem.NewRead(filesystem.DefaultReadConfig(), backend)
			if err != nil {
				t.Fatal(err)
			}
			grep, err := filesystem.NewSearch(backend)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(root, "input.txt"), []byte("business-value"), 0600); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(root, "other.txt"), []byte("header\nfollow-up-value"), 0600); err != nil {
				t.Fatal(err)
			}
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					raw, _ := json.Marshal(map[string]any{"code": tc.code})
					return []model.ToolCall{{ID: "business", Name: codemode.ToolName, Args: string(raw)}}
				}
				return nil
			}}
			runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{read, grep, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}, nil, nil)
			if len(driver.requests) != 2 {
				t.Fatalf("requests=%d", len(driver.requests))
			}
			visible := errorDeliveryModelResult(t, driver.requests[1], "business")
			raw := errorDeliveryJSON(t, visible.Content)
			if visible.IsError != (tc.wantErrorCode != "") || !bytes.Contains(raw, []byte(`"content":"`+tc.wantContent+`"`)) || !bytes.Contains(raw, []byte(`"done":true`)) {
				t.Fatalf("business result not available: %s", raw)
			}
			if tc.wantErrorCode != "" && !bytes.Contains(raw, []byte(`"code":"`+tc.wantErrorCode+`"`)) {
				t.Fatalf("caught error code not available: %s", raw)
			}
		})
	}
}

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
	t.Parallel()
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
		{"modern_array", `const r=await live.array_value({});if(!Array.isArray(r.data)||r.data[0]!=="array-value")throw Error("missing array data");return r.data;`, "array-value", false},
		{"modern_null", `const r=await live.null_result({});if(r.data!==null)throw Error("missing null data");return {data:r.data};`, "null", false},
		{"modern_scalar", `const r=await live.non_object({});if(r.data!==false)throw Error("missing boolean data");return {data:r.data};`, "false", false},
		{"modern_invalid", `return await live.invalid({});`, "outputSchema", false},
		{"modern_missing", `return await live.missing({});`, "outputSchema requires structuredContent", false},
		{"exact_echo_only", `const r=await live.echo({});if(Object.keys(r.data).length!==0||r.text!=="before\n{ }\nafter")throw Error('changed content: '+r.text);return r;`, "before", false},
		{"failed_schema", `return await live.failed({});`, "business failure", false},
		{"failed_missing_schema_result", `return await live.failed_missing({});`, "business failure", false},
		{"raw_json_collision", `const r=await live.collision({});if(r.data.raw_json!=='business-raw')throw Error('lost business field');return r;`, "business-raw", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
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
			modern := strings.HasPrefix(tc.name, "modern_")
			server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "business", Version: "1"}, nil)
			for _, name := range []string{"value", "fallback", "invalid", "failed", "collision", "no_schema", "missing", "null_result", "non_object", "echo", "failed_missing", "array_value"} {
				def := &mcpsdk.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}
				switch name {
				case "fallback", "no_schema", "non_object":
				case "echo":
					def.OutputSchema = map[string]any{"type": "object"}
				case "array_value":
					if !modern {
						continue
					}
					def.OutputSchema = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
				default:
					def.OutputSchema = map[string]any{"type": "object", "required": []string{"name", "n"}, "properties": map[string]any{"name": map[string]any{"type": "string"}, "n": map[string]any{"type": "integer"}}}
				}
				if modern && (name == "null_result" || name == "missing") {
					def.OutputSchema = map[string]any{"type": "null"}
				}
				server.AddTool(def, func(_ context.Context, request *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
					if modern && request.Params.GetMeta()[mcpsdk.MetaKeyProtocolVersion] != "2026-07-28" {
						t.Error("tools/call did not use MCP 2026-07-28")
					}
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
					case "array_value":
						result.StructuredContent = []string{"array-value"}
						result.Content = nil
					case "non_object":
						if modern {
							result.StructuredContent = false
							result.Content = nil
						} else {
							result.StructuredContent = []any{"not an object"}
						}
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
			endpoint := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, &mcpsdk.StreamableHTTPOptions{Stateless: modern}))
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
			wantError := strings.HasSuffix(tc.name, "_result") || tc.name == "invalid_schema" || tc.name == "failed_schema" || tc.name == "modern_invalid" || tc.name == "modern_missing"
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
				reopened := newLogicalFileStore(t, sessionfile.Config{RootDir: replayRoot})
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

func TestOriginalReturnKeepsOutputAndMediaBudgets(t *testing.T) {
	t.Parallel()
	for _, media := range []bool{false, true} {
		t.Run(map[bool]string{false: "output", true: "media"}[media], func(t *testing.T) {
			limits := codemode.DefaultLimits()
			limits.OutputBytes = 64
			limits.Calls = 1
			h := &codeModeHost{limits: limits, codeModeResults: newCodeModeResults()}
			result := tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: json.RawMessage(`{"large":"` + strings.Repeat("x", 100) + `"}`)}}}
			if media {
				limits.OutputBytes = 4096
				h.limits = limits
				part := tool.Image{Data: []byte("fixture"), MIMEType: "image/png"}
				result.Content = []tool.ContentBlock{part, part}
			}
			if _, err := h.project("original", result, codemode.OutcomeSucceeded, tool.Definition{}); err != nil {
				t.Fatal(err)
			}
			if _, err := h.returnedParts(codemode.Output{Reply: true, CallID: "original"}); err == nil {
				t.Fatal("whole original reply bypassed an existing output/media budget")
			}
			if _, err := h.returnedParts(codemode.Output{Reply: true, CallID: "foreign"}); err == nil {
				t.Fatal("foreign original reply acquired authority")
			}
		})
	}
}

// The provider is a local HTTP fixture; filesystem tools, worker, Runtime and
// next provider request encoding are real. No credential or live model is used.
func TestJSExecInjectedMediaInJSONReachesNextHTTPRequest(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, selection, returned string
		want                      int
	}{
		{"wrapped", `image.media[0]`, `return {selected:body.data};`, 1},
		{"payload", `image.media[0]`, `return body.data;`, 1},
		{"body", `image.media[0]`, `return body;`, 0},
		{"copied", `({...image.media[0]})`, `return body.data;`, 0},
		{"ordinary", `{kind:"media",handle:"ordinary-business-data"}`, `return {selected:body.data};`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, active, root := codeModeStore(t)
			sandbox, err := host.New(host.Config{CWD: root})
			if err != nil {
				t.Fatal(err)
			}
			defer sandbox.Close()
			read, err := filesystem.NewRead(filesystem.DefaultReadConfig(), sandbox)
			if err != nil {
				t.Fatal(err)
			}
			view, err := filesystem.NewViewImage(sandbox)
			if err != nil {
				t.Fatal(err)
			}
			bitmap := image.NewRGBA(image.Rect(0, 0, 96, 64))
			for y := 0; y < 64; y++ {
				for x := 0; x < 96; x++ {
					bitmap.SetRGBA(x, y, color.RGBA{R: 225, G: 245, B: 245, A: 255})
					if x > 20 && x < 76 && y > 12 && y < 52 {
						bitmap.SetRGBA(x, y, color.RGBA{G: 170, B: 170, A: 255})
					}
				}
			}
			var pngBytes bytes.Buffer
			if err = png.Encode(&pngBytes, bitmap); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(root, "fixture.png"), pngBytes.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(root, "input.txt"), []byte(`{"case":"native-read"}`), 0600); err != nil {
				t.Fatal(err)
			}
			code := `const image=await tools.call("ViewImage",{path:"fixture.png"});const body=await tools.call("Read",{path:"input.txt"});body.data.selected=` + tc.selection + `;` + tc.returned
			args, _ := json.Marshal(map[string]any{"code": code})
			var mu sync.Mutex
			var requests [][]byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, readErr := io.ReadAll(r.Body)
				if readErr != nil {
					t.Error(readErr)
					http.Error(w, "read", 500)
					return
				}
				mu.Lock()
				requests = append(requests, raw)
				n := len(requests)
				mu.Unlock()
				output := []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "done"}}}}
				if n == 1 {
					output = []any{map[string]any{"type": "function_call", "name": "js_exec", "call_id": "injected-media", "arguments": string(args)}}
				}
				if n > 2 {
					t.Error("unexpected extra request")
					http.Error(w, "extra", 500)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture-response", "object": "response", "model": "fixture-model", "status": "completed", "output": output, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}})
			}))
			defer server.Close()
			llm, err := providers.New(providers.Config{Provider: "fixture", API: model.APIOpenAIResponses, Model: "fixture-model", BaseURL: server.URL, HTTPClient: server.Client(), Auth: providers.AuthConfig{Type: model.AuthNone}, ImageInput: true, ContextWindowTokens: 200000})
			if err != nil {
				t.Fatal(err)
			}
			runCodeMode(t, store, active, agent.AgentSpec{Model: llm, Tools: []tool.Tool{read, view, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}, Sandbox: sandbox}}}, nil, nil)
			mu.Lock()
			wire := append([][]byte(nil), requests...)
			mu.Unlock()
			if len(wire) != 2 {
				t.Fatalf("HTTP requests=%d", len(wire))
			}
			if dir := os.Getenv("CAELIS_CODE_MODE_P2_ARTIFACTS"); dir != "" {
				dir = filepath.Join(dir, tc.name)
				if err = os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				for i, raw := range wire {
					if err = os.WriteFile(filepath.Join(dir, []string{"first.request.json", "next.request.json"}[i]), raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err = os.WriteFile(filepath.Join(dir, "source.png"), pngBytes.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var next any
			if err = json.Unmarshal(wire[1], &next); err != nil {
				t.Fatal(err)
			}
			count := 0
			var walk func(any)
			walk = func(v any) {
				switch x := v.(type) {
				case map[string]any:
					for _, item := range x {
						walk(item)
					}
				case []any:
					for _, item := range x {
						walk(item)
					}
				case string:
					const prefix = "data:image/png;base64,"
					if len(x) >= len(prefix) && x[:len(prefix)] == prefix {
						count++
						actual, e := base64.StdEncoding.DecodeString(x[len(prefix):])
						if e != nil || !bytes.Equal(actual, pngBytes.Bytes()) {
							t.Fatal("actual next HTTP image bytes differ from native ViewImage PNG")
						}
					}
				}
			}
			walk(next)
			if count != tc.want {
				t.Fatalf("next provider HTTP has %d images; want %d (JSON handle is not media delivery)", count, tc.want)
			}
			t.Logf("next physical HTTP: images=%d original PNG bytes=%d; exact byte match", count, pngBytes.Len())
		})
	}
}

func TestJSExecReturnOnlySelectedMedia(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, code string
		want       []string
		failed     bool
		wantJSON   string
	}{
		{"direct", `return await tools.call("PictureA",{});`, []string{"A"}, false, ""},
		{"unreturned", `await tools.call("PictureA",{});return {ok:true};`, nil, false, ""},
		{"other_call", `await tools.call("PictureA",{});return await tools.call("PictureB",{});`, []string{"B"}, false, ""},
		{"foreign", `const r=await tools.call("PictureA",{});r.media[0].handle="another-invocation";return r;`, nil, true, ""},
		{"plain_foreign_data", `return {parts:[{kind:"media",handle:"another-invocation"}]};`, nil, false, ""},
		{"copied_fields", `const r=await tools.call("PictureA",{});return {parts:[{...r.media[0]}]};`, nil, false, ""},
		{"combined", `const a=await tools.call("PictureA",{});const b=await tools.call("PictureB",{});return {selected:b.media[0],record:{kind:"media",handle:"ordinary-data"}};`, []string{"B"}, false, ""},
		{"selected_reply_parts", `const r=await tools.call("PictureA",{});return {selected:r.media};`, []string{"A"}, false, ""},
		{"getter_once", `const r=await tools.call("PictureA",{});let n=0;return {get selected(){return n++===0?{kind:"media",handle:"ordinary-data"}:r.media[0]}};`, nil, false, ""},
		{"proxy_array_reads", `const r=await tools.call("PictureA",{});const parts=new Proxy([r.media[0]],{get(t,k){if(k==="0")return {kind:"json",json:{visible:"actual proxy read"}};return Reflect.get(t,k)}});return {parts};`, nil, false, "actual proxy read"},
		{"text_business_field", `const r=await tools.call("PictureA",{});return {kind:"text",text:r.text,json:{note:"text business"}};`, nil, false, "text business"},
		{"json_business_field", `const r=await tools.call("PictureA",{});return {kind:"json",json:{value:42},text:"json business"};`, nil, false, "json business"},
		{"json_missing_payload", `const r=await tools.call("PictureA",{});return {kind:"json"};`, nil, false, `kind`},
		{"text_nonstring_payload", `const r=await tools.call("PictureA",{});return {kind:"text",text:{note:"text object"}};`, nil, false, "text object"},
		{"text_handle_field", `const r=await tools.call("PictureA",{});return {kind:"text",text:r.text,handle:"business handle"};`, nil, false, "business handle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, active := newTestSessionService(t, "selected-media")
			var bitmap bytes.Buffer
			if err := png.Encode(&bitmap, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
				t.Fatal(err)
			}
			picture := func(name string) tool.Tool {
				return tool.NamedTool{Def: tool.Definition{Name: "Picture" + name, InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
					return tool.Result{Content: []tool.ContentBlock{tool.Text("caption " + name), tool.StructuredContent{Value: []byte(`{"value":42}`)}, tool.Image{Data: bitmap.Bytes(), MIMEType: "image/png", Name: name}}}, nil
				}}
			}
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					raw, _ := json.Marshal(map[string]any{"code": tc.code})
					return []model.ToolCall{{ID: "return", Name: codemode.ToolName, Args: string(raw)}}
				}
				return nil
			}}
			events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: codeModeImageModel{driver}, Tools: []tool.Tool{picture("A"), picture("B"), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}, nil, nil)
			var got []string
			failed := false
			directText, directJSON := false, false
			for _, e := range events {
				if e.Tool == nil || e.Tool.Name != codemode.ToolName || e.Message == nil {
					continue
				}
				for _, mp := range e.Message.Parts {
					if mp.ToolResult == nil {
						continue
					}
					failed = failed || mp.ToolResult.IsError
					for _, p := range mp.ToolResult.Content {
						if p.Media != nil {
							got = append(got, p.Media.Name)
						}
						if p.Text != nil && p.Text.Text == "caption A" {
							directText = true
						}
						if p.JSON != nil && string(p.JSON.Value) == `{"value":42}` {
							directJSON = true
						}
					}
				}
			}
			var modelMedia []string
			for _, req := range driver.requests {
				for _, message := range req.Messages {
					for _, part := range message.Parts {
						if part.ToolResult != nil {
							for _, content := range part.ToolResult.Content {
								if content.Media != nil {
									modelMedia = append(modelMedia, content.Media.Name)
								}
							}
						}
					}
				}
			}
			if tc.wantJSON != "" {
				if len(driver.requests) != 2 {
					t.Fatalf("next model request missing: %d", len(driver.requests))
				}
				raw, _ := json.Marshal(driver.requests[1].Messages)
				if !bytes.Contains(raw, []byte(tc.wantJSON)) {
					t.Fatalf("business data lost in next request: %s", raw)
				}
			}
			if strings.Join(modelMedia, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("unselected/historical media reached model: %v, want %v", modelMedia, tc.want)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") || failed != tc.failed {
				t.Fatalf("returned media=%v failed=%v", got, failed)
			}
			if tc.name == "direct" && (!directText || !directJSON) {
				t.Fatal("direct text/JSON not delivered as model parts")
			}
		})
	}
}

func TestJSExecFixedFileBytesInContinuousVM(t *testing.T) {
	t.Parallel()
	store, active, root := codeModeStore(t)
	sandbox, err := host.New(host.Config{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.Close()
	read, err := filesystem.NewRead(filesystem.DefaultReadConfig(), sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "input.txt"), []byte("actual input"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "script.js")
	if err = os.WriteFile(path, []byte(`const s=await tools.search({name:"Read"});if(s.name!=="Read"||!s.input_schema)throw Error("native exact search");return await tools.call("Read",{path:"input.txt"});`), 0600); err != nil {
		t.Fatal(err)
	}
	driver := &mcpScriptModel{steps: func(i int, req *model.Request) []model.ToolCall {
		switch i {
		case 0:
			return []model.ToolCall{{ID: "first", Name: codemode.ToolName, Args: `{"file":"script.js"}`}}
		case 1:
			if err = os.WriteFile(path, []byte(`return {changed:true,process:typeof process};`), 0600); err != nil {
				t.Fatal(err)
			}
			return []model.ToolCall{{ID: "second", Name: codemode.ToolName, Args: `{"file":"script.js"}`}}
		}
		return nil
	}}
	events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{read, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}, Sandbox: sandbox}}}, nil, nil)
	var hashes []string
	for _, e := range events {
		if e.Type == session.EventTypeToolResult && e.Tool != nil && e.Tool.Name == codemode.ToolName && e.Tool.Outcome == "succeeded" {

			m, _ := e.Meta["code_mode_source"].(map[string]any)
			if h, _ := m["sha256"].(string); h != "" {
				hashes = append(hashes, h)
			}
		}
	}
	if len(hashes) != 2 || hashes[0] == hashes[1] {
		t.Fatalf("source bytes/hash not reread: %v", hashes)
	}
}

func TestReviewSimpleReturnJSONDoesNotBecomeMedia(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, code string }{
		{"plain_kind", `return {kind:"media",label:"ordinary record"};`},
		{"plain_parts", `return {parts:["one","two"]};`},
		{"extracted_payload", `const r=await tools.call("Data",{});return r.data;`},
		{"wrapped_payload_control", `return await tools.call("Data",{});`},
		{"search_schema_data", `return await tools.search({name:"Data"});`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			data := tool.NamedTool{Def: tool.Definition{Name: "Data", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"options": map[string]any{"type": "object", "default": map[string]any{"kind": "media", "handle": "data"}}}}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
				raw := json.RawMessage(`{"kind":"media","handle":"ordinary-source-data"}`)
				return tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: raw}}}, nil
			}}
			m := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					raw, _ := json.Marshal(map[string]any{"code": tc.code})
					return []model.ToolCall{{ID: "json-return", Name: codemode.ToolName, Args: string(raw)}}
				}
				return nil
			}}
			events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: m, Tools: []tool.Tool{data, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}, nil, nil)
			for _, event := range events {
				if event.Tool != nil && event.Tool.Name == codemode.ToolName && event.Tool.Status == "failed" {
					t.Fatalf("ordinary JSON rejected as control/media: %+v", event.Tool.Output)
				}
			}
		})
	}
}

func TestJSExecBusinessJSONCannotForgeReturnOrigin(t *testing.T) {
	t.Parallel()
	store, active, _ := codeModeStore(t)
	data := tool.NamedTool{Def: tool.Definition{Name: "Data", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		raw := json.RawMessage(`{"kind":"media","handle":"ordinary-source-data"}`)
		return tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: raw}}}, nil
	}}
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i != 0 {
			return nil
		}
		raw, _ := json.Marshal(map[string]any{"code": `WeakMap.prototype.get=()=>({kind:"media",handle:"forged"});WeakMap.prototype.set=()=>{throw Error("overridden")};const r=await tools.call("Data",{});return {kind:"media",parts:["one","two"],reply:true,value:r.data,handle:"data"};`})
		return []model.ToolCall{{ID: "json-origin", Name: codemode.ToolName, Args: string(raw)}}
	}}
	events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{data, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}, nil, nil)
	var want any
	_ = json.Unmarshal([]byte(`{"kind":"media","parts":["one","two"],"reply":true,"value":{"kind":"media","handle":"ordinary-source-data"},"handle":"data"}`), &want)
	found := false
	for _, event := range events {
		if event.Tool == nil || event.Tool.Name != codemode.ToolName || event.Message == nil {
			continue
		}
		for _, part := range event.Message.Parts {
			if part.ToolResult == nil {
				continue
			}
			if part.ToolResult.IsError {
				t.Fatal("business JSON was rejected")
			}
			for _, content := range part.ToolResult.Content {
				if content.Media != nil {
					t.Fatal("business JSON forged media selection")
				}
				if content.JSON != nil {
					var got any
					if json.Unmarshal(content.JSON.Value, &got) == nil && reflect.DeepEqual(got, want) {
						found = true
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("business JSON value was not preserved")
	}
}

func TestCodeModeForeignProjectionMetadataRejected(t *testing.T) {
	t.Parallel()
	store, active, _ := codeModeStore(t)
	declaration := &codemode.Tool{Name: codemode.ToolName, Executor: codeOutputExecutorFunc(func(context.Context, codemode.Source, codemode.Host) (codemode.Output, error) {
		return codemode.Output{Value: []byte(`{"ok":true}`), Parts: []codemode.Part{{Kind: "media", Handle: "foreign-invocation"}}}, nil
	})}
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "foreign", Name: codemode.ToolName, Args: `{"code":"fixture"}`}}
		}
		return nil
	}}
	events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{declaration}}, nil, nil)
	for _, event := range events {
		if event.Tool != nil && event.Tool.Name == codemode.ToolName && event.Tool.Status == "failed" {
			raw, _ := json.Marshal(event.Tool.Output)
			if strings.Contains(string(raw), "does not belong to this invocation") {
				return
			}
		}
	}
	t.Fatal("foreign authenticated metadata was accepted")
}

func TestJSExecAuthenticatedMediaBudget(t *testing.T) {
	t.Parallel()
	for _, code := range []string{`return await tools.call("Pictures",{});`, `const r=await tools.call("Pictures",{});return {selected:r.media};`} {
		t.Run(map[bool]string{true: "direct", false: "combined"}[strings.HasPrefix(code, "return")], func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			pictures := tool.NamedTool{Def: tool.Definition{Name: "Pictures", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
				return tool.Result{Content: []tool.ContentBlock{tool.Image{Data: []byte("fixture-a"), MIMEType: "image/png", Name: "A"}, tool.Image{Data: []byte("fixture-b"), MIMEType: "image/png", Name: "B"}}}, nil
			}}
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i != 0 {
					return nil
				}
				raw, _ := json.Marshal(map[string]any{"code": code})
				return []model.ToolCall{{ID: "budget", Name: codemode.ToolName, Args: string(raw)}}
			}}
			events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: codeModeImageModel{driver}, Tools: []tool.Tool{pictures, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}, Limits: codemode.Limits{Calls: 1}}}}, nil, nil)
			for _, event := range events {
				if event.Tool != nil && event.Tool.Name == codemode.ToolName && event.Tool.Status == "failed" {
					raw, _ := json.Marshal(event.Tool.Output)
					if strings.Contains(string(raw), "returned media budget exceeded") {
						return
					}
				}
			}
			t.Fatal("authenticated media budget was ignored")
		})
	}
}

// Execution completion is an observation, independent of cumulative IsError.
func TestCodeModeScriptCompletionSurvivesCanonicalErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, code, want string
		failed           bool
	}{
		{"normal", `return 42;`, "completed", false},
		{"ordinary_json", `return {metadata:{code_mode_script_completion:"failed"}};`, "completed", false},
		{"caught", `try {await tools.call("Bad",{});}catch(e){}return {recovered:true};`, "completed", true},
		{"uncaught_tool", `await tools.call("Bad",{});`, "failed", true},
		{"uncaught_script", `throw Error("script failure");`, "failed", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			bad := tool.NamedTool{Def: tool.Definition{Name: "Bad", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
				return tool.Result{Content: []tool.ContentBlock{tool.Text("original tool failure")}}, tool.FailedReply("fixture tool failure")
			}}
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i > 0 {
					return nil
				}
				raw, _ := json.Marshal(map[string]any{"code": tc.code})
				return []model.ToolCall{{ID: "completion", Name: codemode.ToolName, Args: string(raw)}}
			}}
			events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{bad, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}, nil, nil)
			seen := false
			for _, event := range events {
				if event.Type != "tool_result" || event.Tool == nil || event.Tool.ID != "completion" || event.Message == nil {
					continue
				}
				seen = true
				if event.Meta[codemode.MetadataScriptCompletion] != tc.want {
					t.Fatalf("completion=%v want %s; fixture meta=%#v", event.Meta[codemode.MetadataScriptCompletion], tc.want, event.Meta)
				}
				for _, part := range event.Message.Parts {
					if part.ToolResult != nil && part.ToolResult.IsError != tc.failed {
						t.Fatal("canonical IsError changed")
					}
				}
			}
			if !seen {
				t.Fatal("missing canonical terminal result")
			}
		})
	}
}

func TestCodeModeTaskResultPreservesScriptCompletion(t *testing.T) {
	t.Parallel()
	for _, completion := range []string{"completed", "failed"} {
		raw, _ := json.Marshal(map[string]any{"metadata": map[string]any{codemode.MetadataScriptCompletion: completion}})
		snapshot := task.Snapshot{Kind: task.KindCodeMode, State: task.StateFailed}
		ctx, observed := toolbinding.Observe(t.Context())
		result, err := taskSnapshotToolResultWithPayload(ctx, snapshot, map[string]any{"tool_result_json": string(raw)})
		if !tool.HasFailedReply(err) || observed.ScriptCompletion != completion {
			t.Fatalf("lost independent Task observation: %+v", result)
		}
	}
}

func TestCodeModeChildJournalRequiresOriginalAdmission(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"missing", "wrong-call", "wrong-name", "wrong-parent", "nil-validator", "valid", "ordinary-parent", "no-parent"} {
		t.Run(name, func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			call := tool.Call{ID: "child", Name: "Write", Input: []byte(`{}`)}
			execution := tool.InvocationContext{SessionID: active.SessionID, TurnID: "turn", ItemID: "step"}
			parent := &tool.ParentCall{Kind: codemode.ToolKind, Name: "arbitrary_script_name", ID: "outer"}
			var effects, validations atomic.Int32
			binding := &codeModeAdmissionBinding{callID: call.ID, name: call.Name, parent: *parent, validate: func(context.Context) (func(), error) {
				validations.Add(1)
				return func() {}, nil
			}}
			switch name {
			case "wrong-call":
				binding.callID = "another-child"
			case "wrong-name":
				binding.name = "AnotherTool"
			case "wrong-parent":
				binding.parent.ID = "another-parent"
			case "nil-validator":
				binding.validate = nil
			case "ordinary-parent":
				parent = &tool.ParentCall{Name: "js_repl", ID: "ordinary-provenance"}
			case "no-parent":
				parent = nil
			}
			ctx := toolbinding.WithOccurrence(t.Context(), execution, parent, nil)
			if name != "missing" {
				ctx = context.WithValue(ctx, codeModeAdmissionKey{}, binding)
			}
			wrapped := dispatchTool{base: tool.NamedTool{Def: tool.Definition{Name: "Write"}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { effects.Add(1); return tool.Result{}, nil }}, sessions: store, sessionRef: active.SessionRef}
			_, err := wrapped.Call(ctx, call)
			allowed := name == "valid" || name == "ordinary-parent" || name == "no-parent"
			if allowed != (err == nil) || allowed != (effects.Load() == 1) {
				t.Fatalf("allowed=%v effects=%d error=%v", allowed, effects.Load(), err)
			}
			if name == "valid" && validations.Load() != 1 || name != "valid" && validations.Load() != 0 {
				t.Fatalf("unexpected validation count=%d", validations.Load())
			}
			if !allowed {
				events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
				if err != nil {
					t.Fatal(err)
				}
				for _, event := range events {
					if event.Type == session.EventTypeToolResult && event.Tool != nil {
						t.Fatal("unbound child reached durable execution claim")
					}
				}
			}
		})
	}
}
