package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
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
	for _, tc := range []struct {
		name, code, want string
		image            bool
	}{
		{"direct", `return await live.value({});`, "9223372036854775807", true},
		{"dependency", `const r=await live.value({});return {selected:r.name};`, "selected", false},
		{"nested", `return {record:await live.value({})};`, "record", true},
		{"selected_media", `const r=await live.value({});return {image:r.parts.find(p=>p.kind==='media')};`, "image", true},
		{"copied_media", `const r=await live.value({});return {...r.parts.find(p=>p.kind==='media')};`, "handle", false},
		{"fallback", `const r=await live.fallback({});if(r.content[0].type!=='text'||typeof r.content[0].text!=='string'||r.name!==undefined)throw Error('content contract');return r;`, "not-business", true},
		{"delete_content", `const r=await live.fallback({});delete r.content;return r;`, "structuredContent", false},
		{"nested_delete_content", `const r=await live.fallback({});delete r.content;return {record:r};`, "record", false},
		{"copy_delete_content", `const r=await live.fallback({});delete r.content;return {...r};`, "structuredContent", false},
		{"copied_fallback_handle", `const r=await live.fallback({});r.content=[r.content[0],{...r.content[1]}];return r;`, "content", false},
		{"nested_copied_fallback_handle", `const r=await live.fallback({});r.content=[r.content[0],{...r.content[1]}];return {record:r};`, "record", false},
		{"copied_fallback_references", `return {...await live.fallback({})};`, "content", true},
		{"selected_after_delete", `const r=await live.fallback({});const image=r.content[1];delete r.content;return {record:r,image};`, "record", true},
		{"schema", `const s=await tools.search({server:'live',tool:'value'});if(s.tools[0].output_format!=='structured'||!s.tools[0].output_schema.properties.name)throw Error('output contract missing');return await live.value({});`, "9223372036854775807", true},
		{"invalid_schema", `try{await live.invalid({});throw Error('resolved invalid output');}catch(e){if(!e.result||e.result!==e.reply||e.result.name!==undefined||e.result.structuredContent.n!=='unverified'||!e.result.content.some(p=>p.type==='text'&&p.text.includes('outputSchema')))throw Error('invalid diagnostic contract');return {caught:true,diagnostic:e.result.structuredContent};}`, "unverified", false},
		{"failed_schema", `try{await live.failed({});throw Error('resolved failed output');}catch(e){if(!e.result||e.result.name!==undefined||e.result.structuredContent.name!=='business')throw Error('failed diagnostic contract');return {caught:true,diagnostic:e.result.structuredContent};}`, "caught", false},
		{"raw_json_collision", `const r=await live.collision({});if(r.raw_json!=='business-raw'||!Object.keys(r).includes('raw_json'))throw Error('shadowed business field');return r;`, "business-raw", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			var bitmap bytes.Buffer
			if err := png.Encode(&bitmap, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
				t.Fatal(err)
			}
			var effects atomic.Int32
			server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "business", Version: "1"}, nil)
			for _, name := range []string{"value", "fallback", "invalid", "failed", "collision"} {
				def := &mcpsdk.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}
				if name != "fallback" {
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
					return &mcpsdk.CallToolResult{IsError: name == "failed", Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: `{"name":"not-business"}`}, &mcpsdk.ImageContent{Data: bitmap.Bytes(), MIMEType: "image/png"}}, StructuredContent: structured}, nil
				})
			}
			endpoint := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil))
			defer endpoint.Close()
			manager, err := mcp.NewManager(t.Context(), []mcp.ServerSpec{{PluginID: "fixture", Name: "live", Transport: mcp.TransportStreamableHTTP, URL: endpoint.URL, SourceTrusted: true}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			<-manager.Initialized()
			args, _ := json.Marshal(map[string]any{"code": tc.code})
			var mu sync.Mutex
			var requests [][]byte
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
				if n > 2 {
					t.Error("unexpected extra model request")
					http.Error(w, "extra", 500)
					return
				}
				output := []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "done"}}}}
				if n == 1 {
					output = []any{map[string]any{"type": "function_call", "name": codemode.ToolName, "call_id": "business", "arguments": string(args)}}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "object": "response", "model": "fixture", "status": "completed", "output": output, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}})
			}))
			defer provider.Close()
			factory := providers.NewFactory()
			if err := factory.Register(providers.Config{Alias: "fixture/business", Provider: "fixture", API: providers.APIOpenAIResponses, Model: "fixture", BaseURL: provider.URL, HTTPClient: provider.Client(), Auth: providers.AuthConfig{Type: providers.AuthNone}, ImageInput: true, ContextWindowTokens: 200000}); err != nil {
				t.Fatal(err)
			}
			llm, err := factory.NewByAlias("fixture/business")
			if err != nil {
				t.Fatal(err)
			}
			driver := &codeModeBusinessModel{LLM: llm}
			runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{toolsearch.NewSource(manager, exactMCPRanker{}), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}, DeferredTools: manager}, nil, nil)
			if effects.Load() != 1 || len(driver.requests) != 2 {
				t.Fatalf("effects=%d requests=%d", effects.Load(), len(driver.requests))
			}
			visible := errorDeliveryModelResult(t, driver.requests[1], "business")
			if visible.IsError != (tc.name == "invalid_schema" || tc.name == "failed_schema") {
				t.Fatalf("unexpected script disposition: %s", errorDeliveryJSON(t, visible.Content))
			}
			raw, _ := json.Marshal(visible.Content)
			if !strings.Contains(string(raw), tc.want) {
				t.Fatalf("missing %s: %s", tc.want, raw)
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
			if len(wire) != 2 {
				t.Fatalf("provider HTTP requests=%d", len(wire))
			}
			var next struct {
				Input []struct {
					Type   string          `json:"type"`
					CallID string          `json:"call_id"`
					Output json.RawMessage `json:"output"`
				} `json:"input"`
			}
			if err := json.Unmarshal(wire[1], &next); err != nil {
				t.Fatal(err)
			}
			httpImages := 0
			found := false
			for _, input := range next.Input {
				if input.Type != "function_call_output" || input.CallID != "business" {
					continue
				}
				found = true
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
				for i, name := range []string{"first.request.json", "next.request.json"} {
					if err := os.WriteFile(filepath.Join(dir, name), wire[i], 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			t.Logf("next provider HTTP: images=%d, original PNG bytes=%d", httpImages, bitmap.Len())
		})
	}
}
