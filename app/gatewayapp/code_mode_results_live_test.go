package gatewayapp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/control/modelconfig"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Opt-in functional acceptance: a real authorized model writes and executes JS
// against native tools and a real local MCP fixture. No production account or GUI.
func TestCodeModeResultsLive(t *testing.T) {
	root := os.Getenv("CAELIS_CODE_MODE_RESULTS_LIVE")
	if root == "" {
		t.Skip("explicit task-owned live evidence directory required")
	}
	if !filepath.IsAbs(root) {
		t.Fatal("absolute artifact directory required")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	started, err := os.OpenFile(filepath.Join(root, "RUN-STARTED"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal("preserve original run; no replay")
	}
	_ = started.Close()
	cfg, cleanup, err := selectedCodeModeModel(t.Context(), os.Getenv("CAELIS_CODE_MODE_LIVE_CONFIG"), os.Getenv("CAELIS_CODE_MODE_LIVE_CREDENTIAL_ROOT"), authorizedModelID)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if cfg.Timeout > 0 && cfg.Timeout < 600*time.Second || cfg.StreamFirstEventTimeout > 0 && cfg.StreamFirstEventTimeout < 600*time.Second {
		t.Fatal("authorized model has shorter timeout")
	}
	writeLiveJSON(t, filepath.Join(root, "model.json"), fairModelParameters(cfg))
	workspace := filepath.Join(root, "workspace")
	if err = os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(workspace, "request.txt"), []byte("ticket: CM-17\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bitmap := image.NewRGBA(image.Rect(0, 0, 480, 260))
	draw.Draw(bitmap, bitmap.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	for y := 30; y < 200; y++ {
		half := (y - 30) / 2
		for x := 120 - half; x <= 120+half; x++ {
			bitmap.Set(x, y, color.RGBA{255, 128, 0, 255})
		}
	}
	draw.Draw(bitmap, image.Rect(270, 65, 440, 190), image.NewUniform(color.RGBA{0, 145, 55, 255}), image.Point{}, draw.Src)
	var picture bytes.Buffer
	if err = png.Encode(&picture, bitmap); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "picture.png"), picture.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	var lookups, pictures atomic.Int32
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "code-mode-local-fixture", Version: "1"}, nil)
	server.AddTool(&mcpsdk.Tool{Name: "lookup", Description: "Read the synthetic ticket record by ticket. The fixture source is task-local, not external documentation.", InputSchema: map[string]any{"type": "object", "required": []string{"ticket"}, "properties": map[string]any{"ticket": map[string]any{"type": "string"}}}, OutputSchema: map[string]any{"type": "object", "required": []string{"ticket", "summary", "source"}, "properties": map[string]any{"ticket": map[string]any{"type": "string"}, "summary": map[string]any{"type": "string"}, "source": map[string]any{"type": "string"}}}}, func(_ context.Context, r *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		lookups.Add(1)
		var args struct {
			Ticket string `json:"ticket"`
		}
		_ = json.Unmarshal(r.Params.Arguments, &args)
		if args.Ticket != "CM-17" {
			return &mcpsdk.CallToolResult{IsError: true, Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "unknown synthetic ticket"}}}, nil
		}
		return &mcpsdk.CallToolResult{StructuredContent: map[string]any{"ticket": "CM-17", "summary": "The local handoff is ready.", "source": "task://reports/CM-17"}, Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "Synthetic local fixture record."}}}, nil
	})
	server.AddTool(&mcpsdk.Tool{Name: "picture", Description: "Return the fixture image for visual inspection.", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		pictures.Add(1)
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.ImageContent{Data: picture.Bytes(), MIMEType: "image/png"}}}, nil
	})
	endpoint := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil))
	defer endpoint.Close()
	manager, err := mcp.NewManager(t.Context(), []mcp.ServerSpec{{PluginID: "acceptance", Name: "reports", Transport: mcp.TransportStreamableHTTP, URL: endpoint.URL}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	<-manager.Initialized()
	prompts := []struct{ ID, Text string }{
		{"workflow", `Complete this task with actual tools in the task workspace. Use js_exec direct function calls and save a reusable workflow.js, then execute that saved file with js_exec file and yield_time_ms:0. The script must first try absent.txt and catch its ordinary Read error, then Read request.txt, use its ticket in the local reports.lookup tool, and run the short command printf command-ok with RunCommand. Return the actual ticket, summary, original source, command output and caught error code. Search for any arguments you do not know. Query the original yielded Task to get the result before answering. You may use native Write to save the script. No other commands or external writes. Finish with the actual sourced summary and command output.`},
		{"continuous", `Use at least three separate sequential js_exec calls in this same environment. First declare retainedBody from Read request.txt and retainedState from runtime.state(), then return the parsed request data. In the next script, use the already retained Body without reading request.txt again; confirm text() and json() can each be read twice, derive the exact ticket identifier CM-17 from retainedBody.json().content, and use reports.lookup. Read.text() contains the whole JSON tool result. A known settled read-only lookup error may be corrected once; never replay an unknown outcome. Retain that result as retainedReport, and return its actual summary and source. In a third script reuse retainedReport and report the current generation together with retainedState.generation. Observe the original Task first if any script yields. Finally call js_exec reset:true, then a new script reporting typeof retainedBody and the new runtime.state().generation. Report the exact sourced summary, whether the first generation was retained and whether reset cleared the variable and changed generation. No command execution or file writes.`},
		{"image", `Use js_exec to call the local reports.picture tool and directly return its complete result to yourself. Then describe the actual image's shapes and colors. Search for arguments if needed. Do not infer the image from the tool description. Do not read or write files or run commands in this task.`},
	}
	writeLiveJSON(t, filepath.Join(root, "prompts.json"), prompts)
	for _, task := range prompts {
		dir := filepath.Join(root, task.ID)
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.ResponseHeaderTimeout = 0
		localCfg := cfg
		localCfg.HTTPClient = &http.Client{Transport: &fairTransport{base: transport, root: dir}}
		built, e := modelconfig.BuildModel(localCfg, 0, 0)
		if e != nil {
			t.Fatal("authorized model construction failed")
		}
		trace := &fairTrace{LLM: built.Model, root: dir, turnCalls: map[int]int{}}
		backend, e := host.New(host.Config{CWD: workspace})
		if e != nil {
			t.Fatal(e)
		}
		defer backend.Close()
		tools, e := builtin.BuildCoreTools(builtin.CoreToolsConfig{Runtime: backend})
		if e != nil {
			t.Fatal(e)
		}
		for _, one := range tools {
			if js, ok := one.(*codemode.Tool); ok {
				js.Executor = &gojaexec.Executor{Observe: func(metrics gojaexec.Metrics) {
					f, e := os.OpenFile(filepath.Join(dir, "workers.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
					if e == nil {
						_, _ = f.Write(append(fairJSON(metrics), '\n'))
						_ = f.Close()
					}
				}}
			}
		}

		policies, e := policy.NewMemory(policy.NamedMode{ID: "local-acceptance", Decide: func(_ context.Context, in policy.ToolContext) (policy.Decision, error) {
			var args map[string]any
			_ = json.Unmarshal(in.Call.Input, &args)
			for _, key := range []string{"path", "file", "workdir"} {
				if path, ok := args[key].(string); ok {
					if !filepath.IsAbs(path) {
						path = filepath.Join(workspace, path)
					}
					relative, e := filepath.Rel(workspace, path)
					if e != nil {
						return policy.Decision{}, e
					}
					if relative == ".." || strings.HasPrefix(relative, "../") {
						return policy.Decision{Action: policy.ActionDeny, Reason: "outside task workspace"}, nil
					}
				}
			}
			if in.Tool.Name == "RunCommand" {
				if task.ID != "workflow" || args["command"] != "printf command-ok" {
					return policy.Decision{Action: policy.ActionDeny, Reason: "only the authorized short command is in scope"}, nil
				}
			}
			if task.ID == "image" && in.Tool.Name != "js_exec" && in.Tool.Name != "tool_search" && in.Tool.Name != "Task" && !tool.IsMCPDefinition(in.Tool) {
				return policy.Decision{Action: policy.ActionDeny, Reason: "image task only"}, nil
			}
			return policy.Decision{Action: policy.ActionAllow}, nil
		}})
		if e != nil {
			t.Fatal(e)
		}
		store := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(dir, "sessions")})
		active, e := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "caelis", UserID: "code-mode-functional", Workspace: session.WorkspaceRef{Key: dir, CWD: workspace}})
		if e != nil {
			t.Fatal(e)
		}
		rt, e := runtime.New(runtime.Config{Sessions: store, TaskStore: sessionfile.NewTaskStore(store), AgentFactory: chat.Factory{SystemPrompt: "Complete the user's task using actual tools. The reports service and workspace are task-owned synthetic fixtures. Use only this workspace and the authorized command. Do not replay unknown effects."}, PolicyRegistry: policies, DefaultPolicyMode: "local-acceptance"})
		if e != nil {
			t.Fatal(e)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		observer := agent.SourceEventObserverFunc(func(_ context.Context, event agent.SourceEvent) error {
			if event.Canonical == nil {
				return nil
			}
			if event.Canonical.Type == session.EventTypeToolResult && event.Canonical.Tool != nil && event.Canonical.Tool.Outcome == "unknown_outcome" {
				defer cancel()
			}
			f, e := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if e != nil {
				return e
			}
			_, e = f.Write(append(fairJSON(event.Canonical), '\n'))
			return errors.Join(e, f.Close())
		})
		stream := true
		request := agent.RunRequest{SessionRef: active.SessionRef, Input: task.Text, Request: agent.ModelRequestOptions{Stream: &stream}, SourceObserver: observer, ApprovalRequester: liveApprovalFunc(func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error) {
			return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true}, nil
		}), AgentSpec: agent.AgentSpec{Model: fairPreserveInvocationTracking(trace, built.Model), Metadata: map[string]any{"reasoning_effort": built.ReasoningEffort, codemode.MetadataSelectedTools: []string{"reports__lookup", "reports__picture"}}, Tools: tools, DeferredTools: manager}}
		result, runErr := rt.Run(context.WithValue(ctx, fairContextKey{}, fairRequestContext{Turn: 1}), request)
		if runErr == nil {
			runErr = result.Handle.WaitCompletion(ctx)
		}
		_ = rt.ReleaseCodeModeEnvironments()
		writeLiveJSON(t, filepath.Join(dir, "terminal.json"), map[string]any{"error_code": errorcode.CodeOf(runErr), "error_type": fmt.Sprintf("%T", runErr)})
		loaded, loadErr := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
		writeLiveJSON(t, filepath.Join(dir, "session.json"), loaded)
		entries, listErr := sessionfile.NewTaskStore(store).ListSession(t.Context(), active.SessionRef)
		writeLiveJSON(t, filepath.Join(dir, "tasks.json"), entries)
		writeLiveJSON(t, filepath.Join(root, "effects.json"), map[string]any{"lookup": lookups.Load(), "picture": pictures.Load()})
		if runErr != nil || loadErr != nil || listErr != nil {
			t.Fatal("functional acceptance stopped; original traces retained")
		}
		if task.ID == "continuous" && lookups.Load() < 2 {
			t.Error("continuous model task did not call the real lookup")
		}
		if task.ID == "workflow" {
			if _, e = os.Stat(filepath.Join(workspace, "workflow.js")); e != nil {
				t.Error("model did not save workflow.js")
			}
			if lookups.Load() == 0 {
				t.Error("no actual MCP lookup")
			}
		}
		if task.ID == "image" && pictures.Load() == 0 {
			t.Error("no actual image call")
		}
	}
}
