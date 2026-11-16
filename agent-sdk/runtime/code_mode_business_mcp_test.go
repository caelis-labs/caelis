package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

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
		{"schema", `const s=await tools.search({server:'live',tool:'value'});if(s.tools[0].output_format!=='structured'||!s.tools[0].output_schema.properties.name)throw Error('output contract missing');return await live.value({});`, "9223372036854775807", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			var bitmap bytes.Buffer
			if err := png.Encode(&bitmap, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
				t.Fatal(err)
			}
			var effects atomic.Int32
			server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "business", Version: "1"}, nil)
			for _, name := range []string{"value", "fallback"} {
				def := &mcpsdk.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}
				if name == "value" {
					def.OutputSchema = map[string]any{"type": "object", "required": []string{"name", "n"}, "properties": map[string]any{"name": map[string]any{"type": "string"}, "n": map[string]any{"type": "integer"}}}
				}
				server.AddTool(def, func(_ context.Context, _ *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
					effects.Add(1)
					return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: `{"name":"not-business"}`}, &mcpsdk.ImageContent{Data: bitmap.Bytes(), MIMEType: "image/png"}}, StructuredContent: map[string]any{"name": "business", "n": json.Number("9223372036854775807")}}, nil
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
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					raw, _ := json.Marshal(map[string]any{"code": tc.code})
					return []model.ToolCall{{ID: "business", Name: codemode.ToolName, Args: string(raw)}}
				}
				return nil
			}}
			runCodeMode(t, store, active, agent.AgentSpec{Model: codeModeImageModel{driver}, Tools: []tool.Tool{toolsearch.NewSource(manager, exactMCPRanker{}), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}, DeferredTools: manager}, nil, nil)
			if effects.Load() != 1 || len(driver.requests) != 2 {
				t.Fatalf("effects=%d requests=%d", effects.Load(), len(driver.requests))
			}
			visible := errorDeliveryModelResult(t, driver.requests[1], "business")
			if visible.IsError {
				t.Fatalf("script failed: %s", errorDeliveryJSON(t, visible.Content))
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
			if (images == 1) != tc.image {
				t.Fatalf("images=%d expected=%v: %s", images, tc.image, raw)
			}
		})
	}
}
