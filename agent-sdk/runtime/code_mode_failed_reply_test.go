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
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// A recoverable MCP failure may carry a diagnostic screenshot and exact JSON.
// Selecting its original parts must deliver the same data as a successful reply.
func TestJSExecMCPReplyPreservesSelectedMediaAndRawJSON(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failed   bool
		uncaught bool
		copied   bool
	}{
		{name: "success"},
		{name: "recoverable_failure", failed: true},
		{name: "uncaught_failure", failed: true, uncaught: true},
		{name: "copied_failure_media", failed: true, copied: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			var bitmap bytes.Buffer
			if err := png.Encode(&bitmap, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
				t.Fatal(err)
			}
			var effects, continued atomic.Int32
			server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "diagnostic", Version: "1"}, nil)
			server.AddTool(&mcpsdk.Tool{Name: "diagnose", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
				effects.Add(1)
				return &mcpsdk.CallToolResult{
					IsError:           tc.failed,
					Content:           []mcpsdk.Content{&mcpsdk.ImageContent{Data: bitmap.Bytes(), MIMEType: "image/png"}},
					StructuredContent: map[string]any{"receipt": json.Number("9223372036854775807")},
				}, nil
			})
			endpoint := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil))
			defer endpoint.Close()
			manager, err := mcp.NewManager(t.Context(), []mcp.ServerSpec{{PluginID: "fixture", Name: "live", Transport: mcp.TransportStreamableHTTP, URL: endpoint.URL, SourceTrusted: true}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			<-manager.Initialized()
			if len(manager.Tools()) != 1 {
				t.Fatalf("MCP not ready: %+v", manager.GetServerInfos("fixture"))
			}
			next := tool.NamedTool{Def: tool.Definition{Name: "Continue", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
				continued.Add(1)
				return tool.Result{}, nil
			}}
			code := `await tools.search("live__diagnose");
let reply, caught=false;
try { reply=await tools.call("live__diagnose",{}); }
catch(e) { reply=e.reply; caught=true; }
await tools.call("Continue",{});
return {caught,selected:reply.parts[0],raw:reply.raw_json,
  rawEnumerable:Object.keys(reply).includes("raw_json"),
  outcome:reply.outcome||null,isError:reply.is_error||false};`
			if tc.uncaught {
				code = `await tools.search("live__diagnose"); await tools.call("live__diagnose",{}); await tools.call("Continue",{});`
			} else if tc.copied {
				code = strings.Replace(code, "selected:reply.parts[0]", "selected:{...reply.parts[0]}", 1)
			}
			args, _ := json.Marshal(map[string]any{"code": code})
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return []model.ToolCall{{ID: "script", Name: codemode.ToolName, Args: string(args)}}
				}
				return nil
			}}
			runCodeMode(t, store, active, agent.AgentSpec{Model: codeModeImageModel{driver}, Tools: []tool.Tool{next, toolsearch.NewSource(manager, exactMCPRanker{}), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}, DeferredTools: manager}, nil, nil)
			wantContinued := int32(1)
			if tc.uncaught {
				wantContinued = 0
			}
			if effects.Load() != 1 || continued.Load() != wantContinued {
				t.Fatalf("diagnostic calls=%d subsequent calls=%d; want 1 and %d", effects.Load(), continued.Load(), wantContinued)
			}
			if len(driver.requests) != 2 {
				t.Fatalf("model requests=%d; want original and continuation", len(driver.requests))
			}
			if tc.uncaught {
				visible := errorDeliveryModelResult(t, driver.requests[1], "script")
				reopened := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
				entries, err := sessionfile.NewTaskStore(reopened).ListSession(t.Context(), active.SessionRef)
				if err != nil || len(entries) != 1 {
					t.Fatalf("durable script Tasks=%d, error=%v", len(entries), err)
				}
				var persisted tool.Result
				if err := json.Unmarshal([]byte(taskStringValue(entries[0].Result["tool_result_json"])), &persisted); err != nil {
					t.Fatal(err)
				}
				for name, parts := range map[string][]model.Part{"next_model": visible.Content, "persisted_task": persisted.Content} {
					failure := errorDeliveryFailure(t, parts)
					if failure.Reply == nil || failure.Reply.CallID == "" || !failure.Reply.IsError || failure.Reply.Outcome != codemode.OutcomeFailed {
						t.Fatalf("%s lost failed receipt: %+v", name, failure)
					}
					hasJSON, hasMediaHandle := false, false
					for _, part := range failure.Reply.Parts {
						hasJSON = hasJSON || bytes.Contains(part.JSON, []byte(`"receipt":9223372036854775807`))
						hasMediaHandle = hasMediaHandle || part.Kind == "media" && part.Handle != ""
					}
					if !hasJSON || !hasMediaHandle {
						t.Fatalf("%s lost structured receipt or diagnostic media handle: %+v", name, failure.Reply.Parts)
					}
					for _, part := range parts {
						if part.Media != nil || part.FileRef != nil {
							t.Fatalf("%s auto-attached unselected failed media", name)
						}
					}
				}
				assertSubagentSagaModelRoundTrip(t, reopened, active.SessionRef)
				return
			}
			images, summaries := 0, 0
			for _, message := range driver.requests[1].Messages {
				for _, part := range message.Parts {
					if part.ToolResult == nil || part.ToolResult.Name != codemode.ToolName {
						continue
					}
					if part.ToolResult.IsError != tc.failed {
						t.Fatalf("parent failure disposition=%v; want %v", part.ToolResult.IsError, tc.failed)
					}
					for _, content := range part.ToolResult.Content {
						if content.Media != nil {
							images++
							data, err := base64.StdEncoding.DecodeString(content.Media.Source.Data)
							if err != nil || !bytes.Equal(data, bitmap.Bytes()) {
								t.Fatal("selected diagnostic image differs from original MCP image")
							}
						}
						if content.JSON == nil {
							continue
						}
						var summary struct {
							Caught        *bool  `json:"caught"`
							Raw           string `json:"raw"`
							RawEnumerable bool   `json:"rawEnumerable"`
							Outcome       string `json:"outcome"`
							IsError       bool   `json:"isError"`
						}
						if err := json.Unmarshal(content.JSON.Value, &summary); err != nil || summary.Caught == nil {
							continue
						}
						summaries++
						if *summary.Caught != tc.failed || summary.RawEnumerable || summary.IsError != tc.failed {
							t.Fatalf("reply disposition or raw_json enumeration changed: %+v", summary)
						}
						if tc.failed && summary.Outcome != codemode.OutcomeFailed {
							t.Fatalf("failed reply outcome=%q", summary.Outcome)
						}
						var original codemode.Reply
						if err := json.Unmarshal([]byte(summary.Raw), &original); err != nil || original.IsError != tc.failed || original.CallID == "" {
							t.Fatalf("original host reply unavailable: %q (%v)", summary.Raw, err)
						}
						if !bytes.Contains([]byte(summary.Raw), []byte(`"receipt":9223372036854775807`)) {
							t.Fatalf("exact integer tokens lost: %s", summary.Raw)
						}
					}
				}
			}
			wantImages := 1
			if tc.copied {
				wantImages = 0
			}
			if images != wantImages || summaries != 1 {
				t.Fatalf("next model request has images=%d summaries=%d; want %d and 1", images, summaries, wantImages)
			}
		})
	}
}
