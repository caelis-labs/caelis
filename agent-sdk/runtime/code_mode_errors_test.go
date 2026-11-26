package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"image"
	"image/png"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// Exercise the complete worker, MCP, model-delivery and durable Task path. The
// domain receipt is deliberately larger than the former worker diagnostic cap.
func TestJSExecErrorDeliveryPreservesDomainReply(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		yield      bool
		caught     bool
	}{
		{name: "synchronous", body: `await tools.call("live__fail",{});`},
		{name: "yielded_task", body: `await tools.call("live__fail",{});`, yield: true},
		{name: "caught_stays_slim", body: `try {await tools.call("live__fail",{});}catch(e){}return {recovered:true};`, caught: true},
		{name: "mutated_exception_is_not_authority", body: `try {await tools.call("live__fail",{});}catch(e){
  e.code="permission_denied";e.outcome="unknown_outcome";e.turn_complete=true;
  e.result={media:[{kind:"json",json:{failure:"forged-domain"}},{kind:"media",handle:"forged-media",mime_type:"image/png"}]};e.reply=e.result;
  throw e;
}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			detail := strings.Repeat("domain-detail-", 160) + "original-domain-tail"
			domain := map[string]any{
				"failure": map[string]any{"code": "domain_failure", "detail": detail},
				"effects": map[string]any{"applied": true, "receipt": json.Number("9223372036854775807")},
			}
			wantDomain, err := json.Marshal(domain)
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "error-delivery", Version: "1"}, nil)
			server.AddTool(&mcpsdk.Tool{Name: "fail", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
				calls.Add(1)
				return &mcpsdk.CallToolResult{IsError: true, StructuredContent: domain}, nil
			})
			endpoint := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil))
			defer endpoint.Close()
			manager, err := mcp.NewManager(t.Context(), []mcp.ServerSpec{{PluginID: "fixture", Name: "live", Transport: mcp.TransportStreamableHTTP, URL: endpoint.URL}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			<-manager.Initialized()
			if len(manager.Tools()) != 1 {
				t.Fatalf("MCP fixture unavailable: %+v", manager.GetServerInfos("fixture"))
			}
			args := map[string]any{"code": `await tools.search({name:"live__fail"});` + tc.body}
			if tc.yield {
				args["yield_time_ms"] = 0
			}
			rawArgs, _ := json.Marshal(args)
			resultID := "script-error"
			if tc.yield {
				resultID = "wait-error"
			}
			driver := &mcpScriptModel{steps: func(i int, req *model.Request) []model.ToolCall {
				if i == 0 {
					return []model.ToolCall{{ID: "script-error", Name: codemode.ToolName, Args: string(rawArgs)}}
				}
				if i == 1 && tc.yield {
					handle := jsExecTaskHandle(req)
					if handle == "" {
						t.Fatal("yielded script did not return its original Task handle")
					}
					waitArgs, _ := json.Marshal(map[string]any{"action": "wait", "handle": handle})
					return []model.ToolCall{{ID: "wait-error", Name: "Task", Args: string(waitArgs)}}
				}
				return nil
			}}
			runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}, DeferredTools: manager}, nil, nil)
			visible := errorDeliveryModelResult(t, driver.requests[len(driver.requests)-1], resultID)
			if calls.Load() != 1 {
				t.Fatalf("domain effect calls=%d; want exactly one; terminal model output=%s", calls.Load(), errorDeliveryJSON(t, visible.Content))
			}
			if !visible.IsError {
				t.Fatal("failed MCP receipt lost its parent failure disposition")
			}
			reopened := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
			entries, err := sessionfile.NewTaskStore(reopened).ListSession(t.Context(), active.SessionRef)
			if err != nil || len(entries) != 1 {
				t.Fatalf("durable script Tasks=%d, error=%v", len(entries), err)
			}
			entry := entries[0]
			if entry.Running || entry.State != taskapi.StateFailed {
				t.Fatalf("failure or forged disposition changed Task lifecycle: %+v", entry)
			}
			var persisted tool.Result
			if err := json.Unmarshal([]byte(taskStringValue(entry.Result["tool_result_json"])), &persisted); err != nil {
				t.Fatal(err)
			}
			wantCompletion := "failed"
			if tc.caught {
				wantCompletion = "completed"
			}
			if entry.Metadata[codemode.MetadataScriptCompletion] != wantCompletion {
				t.Fatalf("script completion differs from its trusted receipt: %+v", persisted)
			}
			events, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			callID := ""
			for _, event := range events {
				if event.Type == session.EventTypeToolResult && event.Tool != nil {
					receipt := event.Tool
					if receipt.Name == "live__fail" && receipt.Status == "failed" {
						callID = receipt.ID
					}
				}
			}
			if callID == "" {
				t.Fatal("missing durable failed MCP execution receipt")
			}
			for name, parts := range map[string][]model.Part{"next_model": visible.Content, "persisted_task": tool.ModelParts(persisted.Content)} {
				t.Run(name, func(t *testing.T) {
					if name == "next_model" && tc.yield {
						var snapshot struct{ Result json.RawMessage }
						if err := json.Unmarshal(parts[0].JSON.Value, &snapshot); err != nil || !json.Valid(snapshot.Result) {
							t.Fatalf("invalid Task result: %v", err)
						}
						parts = []model.Part{model.NewJSONPart(snapshot.Result)}
					}
					failure := errorDeliveryFailure(t, parts)
					if failure.Tool != "live__fail" || failure.CallID != callID || failure.Outcome != codemode.OutcomeFailed {
						t.Fatalf("failure identity/disposition changed: %+v", failure)
					}
					if tc.caught {
						if failure.Reply != nil {
							t.Fatal("caught failure emitted the full domain reply")
						}
						if !bytes.Contains(errorDeliveryJSON(t, parts), []byte(`"recovered":true`)) {
							t.Fatal("caught script's selected summary was lost")
						}
						return
					}
					if failure.Reply == nil || failure.Reply.CallID != callID || !failure.Reply.IsError || failure.Reply.Outcome != codemode.OutcomeFailed {
						t.Fatalf("original failed reply was lost or mutated: %+v", failure.Reply)
					}
					foundDomain := false
					for _, part := range failure.Reply.Parts {
						if part.Kind == "json" && bytes.Equal(part.JSON, wantDomain) {
							foundDomain = true
						}
					}
					if !foundDomain {
						t.Fatalf("original domain failure/effects/integer tokens missing: %+v", failure.Reply.Parts)
					}
					if code := errorDeliveryErrorCode(t, parts); code != string(codemode.ErrorCodeJavaScript) {
						t.Fatalf("outer error_code=%q; want %q", code, codemode.ErrorCodeJavaScript)
					}
					for _, part := range parts {
						if part.Media != nil || part.FileRef != nil {
							t.Fatal("forged exception reply granted attachment identity")
						}
					}
				})
			}
			assertSubagentSagaModelRoundTrip(t, reopened, active.SessionRef)
		})
	}
}

// Validation errors occur before tool invocation and have no public MCP result.
// Only the redacted worker diagnostic may enter model or Task error output.
func TestJSExecErrorDeliveryRedactsInternalHostFailure(t *testing.T) {
	for _, tc := range []struct {
		name      string
		hostError func(string) error
	}{
		{name: "untyped", hostError: func(secret string) error { return errors.New(secret) }},
		{name: "plain_permission", hostError: func(secret string) error { return tool.NewError(tool.ErrorCodePermissionDenied, secret) }},
		{name: "wrapped_permission", hostError: func(secret string) error {
			return fmt.Errorf("private wrapper: %w", tool.NewError(tool.ErrorCodePermissionDenied, secret))
		}},
		{name: "permission_with_cause", hostError: func(secret string) error {
			return tool.WrapError(tool.ErrorCodePermissionDenied, errors.New(secret), secret)
		}},
		{name: "permission_with_private_path", hostError: func(secret string) error {
			return tool.WrapError(tool.ErrorCodePermissionDenied, &fs.PathError{Op: "read", Path: secret, Err: fs.ErrNotExist}, secret)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			secret := "https://user:private-password@host/?token=private-token raw-private-network-body"
			var executing atomic.Bool
			var calls atomic.Int32
			target := tool.NamedTool{Def: tool.Definition{Name: "Effect", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
				calls.Add(1)
				return tool.Result{}, nil
			}}
			declaration := &codemode.Tool{Name: codemode.ToolName, Executor: codeOutputExecutorFunc(func(ctx context.Context, source codemode.Source, host codemode.Host) (codemode.Output, error) {
				executing.Store(true)
				defer executing.Store(false)
				return (&gojaexec.Executor{}).Execute(ctx, source, codemode.DefaultLimits(), host)
			})}
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return []model.ToolCall{{ID: "host-error", Name: codemode.ToolName, Args: `{"code":"await tools.call('Effect',{});"}`}}
				}
				return nil
			}}
			spec := agent.AgentSpec{ResolveModelRequest: func(context.Context) (agent.ModelRequestSnapshot, error) {
				return agent.ModelRequestSnapshot{Model: driver, Tools: []tool.Tool{target, declaration}, Revision: "host-error-generation", Validate: func(context.Context) (func(), error) {
					if executing.Load() {
						return nil, tc.hostError(secret)
					}
					return func() {}, nil
				}}, nil
			}}
			runCodeMode(t, store, active, spec, nil, nil)
			if calls.Load() != 0 {
				t.Fatal("failed Host validation admitted an effect")
			}
			visible := errorDeliveryModelResult(t, driver.requests[len(driver.requests)-1], "host-error")
			reopened := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
			entries, err := sessionfile.NewTaskStore(reopened).ListSession(t.Context(), active.SessionRef)
			if err != nil || len(entries) != 1 {
				t.Fatalf("durable script Tasks=%d, error=%v", len(entries), err)
			}
			if !visible.IsError || entries[0].State != taskapi.StateFailed {
				t.Fatal("Host validation failure was hidden")
			}
			for name, value := range map[string]any{"next_model": visible, "persisted_task": entries[0].Result} {
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				for _, private := range []string{"private-password", "private-token", "raw-private-network-body"} {
					if bytes.Contains(raw, []byte(private)) {
						t.Fatalf("%s exposed internal Host diagnostic %q: %s", name, private, raw)
					}
				}
			}
			assertSubagentSagaModelRoundTrip(t, reopened, active.SessionRef)
		})
	}
}

func errorDeliveryModelResult(t *testing.T, request *model.Request, callID string) *model.ToolResultPart {
	t.Helper()
	for _, message := range request.Messages {
		for _, part := range message.Parts {
			if part.ToolResult != nil && part.ToolResult.ToolUseID == callID {
				return part.ToolResult
			}
		}
	}
	t.Fatalf("next model request has no result for %q", callID)
	return nil
}

type errorDeliveryFailureRecord struct {
	Tool, Outcome string
	CallID        string `json:"call_id"`
	Reply         *codemode.Reply
}

func errorDeliveryFailure(t *testing.T, parts []model.Part) errorDeliveryFailureRecord {
	t.Helper()
	for _, part := range parts {
		if part.JSON == nil {
			continue
		}
		var summary struct {
			Errors []errorDeliveryFailureRecord
		}
		if json.Unmarshal(part.JSON.Value, &summary) == nil && len(summary.Errors) > 0 {
			if len(summary.Errors) != 1 {
				t.Fatalf("failed calls=%d; want exactly one", len(summary.Errors))
			}
			return summary.Errors[0]
		}
	}
	t.Fatal("missing failed-call summary")
	return errorDeliveryFailureRecord{}
}

func errorDeliveryErrorCode(t *testing.T, parts []model.Part) string {
	t.Helper()
	for _, part := range parts {
		if part.JSON == nil {
			continue
		}
		var payload struct {
			Code string `json:"error_code"`
		}
		if json.Unmarshal(part.JSON.Value, &payload) == nil && payload.Code != "" {
			return payload.Code
		}
	}
	t.Fatal("missing normalized script error payload")
	return ""
}

func errorDeliveryJSON(t *testing.T, parts []model.Part) []byte {
	t.Helper()
	raw, err := json.Marshal(parts)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A recoverable MCP failure may carry a diagnostic screenshot and exact JSON.
// Selecting its original media must deliver the same data as a successful reply.
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
			manager, err := mcp.NewManager(t.Context(), []mcp.ServerSpec{{PluginID: "fixture", Name: "live", Transport: mcp.TransportStreamableHTTP, URL: endpoint.URL}}, nil)
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
			code := `await tools.search({name:"live__diagnose"});
let reply, caught=false;
try { reply=await tools.call("live__diagnose",{}); }
catch(e) { reply=e.reply; caught=true; }
await tools.call("Continue",{});
if (!caught) {
  if (typeof reply.data.receipt !== "number" || reply.text !== "") throw Error("unexpected successful Body");
  return reply;
}
return {caught,selected:reply.media[0],raw:reply.text()};`
			if tc.uncaught {
				code = `await tools.search({name:"live__diagnose"}); await tools.call("live__diagnose",{}); await tools.call("Continue",{});`
			} else if tc.copied {
				code = strings.Replace(code, "selected:reply.media[0]", "selected:{...reply.media[0]}", 1)
			}
			args, _ := json.Marshal(map[string]any{"code": code})
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return []model.ToolCall{{ID: "script", Name: codemode.ToolName, Args: string(args)}}
				}
				return nil
			}}
			runCodeMode(t, store, active, agent.AgentSpec{Model: codeModeImageModel{driver}, Tools: []tool.Tool{next, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}, DeferredTools: manager}, nil, nil)
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
				for name, parts := range map[string][]model.Part{"next_model": visible.Content, "persisted_task": tool.ModelParts(persisted.Content)} {
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
						if !tc.failed {
							// Returning the original Body preserves the raw integer, unlike .data.
							if bytes.Contains(content.JSON.Value, []byte(`"receipt":9223372036854775807`)) {
								summaries++
							}
							continue
						}
						var summary struct {
							Result struct {
								Caught *bool  `json:"caught"`
								Raw    string `json:"raw"`
							}
						}
						if err := json.Unmarshal(content.JSON.Value, &summary); err != nil || summary.Result.Caught == nil {
							continue
						}
						summaries++
						if *summary.Result.Caught != tc.failed || !json.Valid([]byte(summary.Result.Raw)) {
							t.Fatalf("Body diagnostic text/catch disposition changed: %+v", summary)
						}
						if !bytes.Contains([]byte(summary.Result.Raw), []byte(`"receipt":9223372036854775807`)) {
							t.Fatalf("exact integer tokens lost: %s", summary.Result.Raw)
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

func TestRuntimeReviewCaughtReadErrorCanContinue(t *testing.T) {
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
	goodPath := filepath.Join(root, "good.txt")
	if err = os.WriteFile(goodPath, []byte("valid-read-result"), 0600); err != nil {
		t.Fatal(err)
	}
	var firstErr, secondErr error
	declaration := &codemode.Tool{Name: codemode.ToolName, Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
		bad, _ := json.Marshal(map[string]any{"path": root})
		_, firstErr = host.Call(ctx, "Read", bad)
		good, _ := json.Marshal(map[string]any{"path": goodPath})
		_, secondErr = host.Call(ctx, "Read", good)
		return []byte(`{"caught":true}`), nil
	})}
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "script", Name: codemode.ToolName, Args: `{"code":"fixture"}`}}
		}
		return nil
	}}
	events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{declaration, read}}, nil, nil)
	for _, event := range events {
		if event.Tool != nil && event.Tool.ID == "script" && event.Tool.Status == "failed" {
			raw, _ := json.Marshal(event.Tool)
			t.Logf("public-tool=%s", raw)
			raw, _ = json.Marshal(event.Message)
			t.Logf("public-message=%s", raw)
		}
	}
	t.Logf("first=%v second=%v", firstErr, secondErr)
	if firstErr == nil {
		t.Fatal("directory read should fail")
	}
	if secondErr != nil {
		t.Fatalf("caught ordinary Read error closed Host admission: %v", secondErr)
	}
}

// This is real JS/Runtime/native Read and real loopback HTTP transport, not a
// live model. Both failed reads must retain their receipts and summary output.
func TestCodeModeCaughtReadsReachNextHTTPRequest(t *testing.T) {
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
	if err = os.WriteFile(filepath.Join(root, "good.txt"), []byte("VALID_READ_RESULT"), 0600); err != nil {
		t.Fatal(err)
	}
	paths, _ := json.Marshal([]string{root, "missing.txt", "good.txt"})
	code := `const errors=[], values=[]; for(const path of ` + string(paths) + `) {try {const r=await tools.call("Read",{path,limit:1});values.push(r.data.content);}catch(e){errors.push(e.message);}} return {errors,values,done:true};`
	args, _ := json.Marshal(map[string]any{"code": code})
	var mu sync.Mutex
	var requests [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, e := io.ReadAll(r.Body)
		if e != nil {
			t.Error(e)
			return
		}
		mu.Lock()
		requests = append(requests, raw)
		n := len(requests)
		mu.Unlock()
		if n > 2 {
			t.Error("unexpected extra request")
			http.Error(w, "extra", 500)
			return
		}
		output := []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "summary received"}}}}
		if n == 1 {
			output = []any{map[string]any{"type": "function_call", "name": "js_exec", "call_id": "caught-reads", "arguments": string(args)}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "object": "response", "model": "fixture", "status": "completed", "output": output, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}})
	}))
	defer server.Close()
	llm, err := providers.New(providers.Config{Provider: "fixture", API: model.APIOpenAIResponses, Model: "fixture", BaseURL: server.URL, HTTPClient: server.Client(), Auth: providers.AuthConfig{Type: model.AuthNone}, ContextWindowTokens: 200000})
	if err != nil {
		t.Fatal(err)
	}
	runCodeMode(t, store, active, agent.AgentSpec{Model: llm, Tools: []tool.Tool{read, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}, Sandbox: sandbox}}}, nil, nil)
	mu.Lock()
	wire := append([][]byte(nil), requests...)
	mu.Unlock()
	if len(wire) != 2 {
		t.Fatalf("requests=%d", len(wire))
	}
	for _, text := range []string{"VALID_READ_RESULT", "invalid_input", "not_found", `done`, `true`} {
		if !bytes.Contains(wire[1], []byte(text)) {
			t.Fatalf("missing %q in next HTTP request: %s", text, wire[1])
		}
	}
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	failed, succeeded, parentFailed := 0, 0, false
	for _, ev := range events {
		if ev.Type == session.EventTypeToolResult && ev.Tool != nil {
			j := ev.Tool
			if j.Parent != nil && j.Name == "Read" {
				if j.Status == "failed" {
					failed++
				}
				if j.Status == "completed" {
					succeeded++
				}
			}
		}
		if ev.Message != nil {
			for _, part := range ev.Message.Parts {
				if part.ToolResult != nil && part.ToolResult.Name == codemode.ToolName {
					parentFailed = part.ToolResult.IsError
				}
			}
		}
	}
	if failed != 2 || succeeded != 1 || !parentFailed {
		t.Fatalf("failed=%d succeeded=%d parentError=%v", failed, succeeded, parentFailed)
	}
	if dir := os.Getenv("CAELIS_CODE_MODE_CATCH_ARTIFACTS"); dir != "" {
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for i, raw := range wire {
			if err = os.WriteFile(filepath.Join(dir, []string{"first.request.json", "next.request.json"}[i]), raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
		_ = os.WriteFile(filepath.Join(dir, "script.js"), []byte(code), 0600)
		raw, _ := json.MarshalIndent(events, "", "  ")
		_ = os.WriteFile(filepath.Join(dir, "events.json"), raw, 0600)
	}
}

// Code Mode retains the original Run authority through producer quiescence.

type reviewGenerationStore struct {
	session.Service
	revisionMu *sync.RWMutex
	stale      *atomic.Bool
	updated    atomic.Bool
}

func (s *reviewGenerationStore) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	if req.Event.Type == session.EventTypeToolCall && req.Event.Tool != nil {
		r := req.Event.Tool
		if r.Parent != nil && r.Status == "pending" && s.revisionMu.TryLock() {
			// Model a configuration writer at the first durable dispatch step.
			// A retained Validate guard must exclude this revision replacement.
			s.stale.Store(true)
			s.updated.Store(true)
			s.revisionMu.Unlock()
		}
	}
	return s.Service.AppendEvent(ctx, req)
}

func TestReviewCodeModeGenerationGuardCoversDispatch(t *testing.T) {
	store, active, _ := codeModeStore(t)
	var revisionMu sync.RWMutex
	var stale, staleEffect atomic.Bool
	interleaved := &reviewGenerationStore{Service: store, revisionMu: &revisionMu, stale: &stale}
	write := tool.NamedTool{Def: tool.Definition{Name: "Write", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		staleEffect.Store(stale.Load())
		return tool.Result{}, nil
	}}
	declaration := &codemode.Tool{Name: "js_repl", Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
		_, err := host.Call(ctx, "Write", []byte(`{}`))
		return []byte(`{}`), err
	})}
	llm := codeModeModel(false)
	spec := agent.AgentSpec{ResolveModelRequest: func(context.Context) (agent.ModelRequestSnapshot, error) {
		return agent.ModelRequestSnapshot{Model: llm, Tools: []tool.Tool{declaration, write}, Revision: "review-generation", Validate: func(context.Context) (func(), error) {
			revisionMu.RLock()
			if stale.Load() {
				revisionMu.RUnlock()
				return nil, tool.NewError(tool.ErrorCodeStaleRevision, "generation replaced")
			}
			return revisionMu.RUnlock, nil
		}}, nil
	}}
	runCodeMode(t, interleaved, active, spec, nil, nil)
	if staleEffect.Load() {
		t.Fatalf("generation replaced between final validation and effect: revision_update=%v stale_effect=true", interleaved.updated.Load())
	}
}

func TestReviewCodeModeDrainRetainsSessionFence(t *testing.T) {
	for _, fenced := range []bool{false, true} {
		name := "unfenced-sdk"
		if fenced {
			name = "canonical-fence"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) { testCodeModeDrain(t, fenced) })
		})
	}
}

func testCodeModeDrain(t *testing.T, fenced bool) {
	store, active, _ := codeModeStore(t)
	started, release, hostDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	hostResult := make(chan error, 1)
	var oldEffects, nextEffects atomic.Int32
	write := tool.NamedTool{Def: tool.Definition{Name: "Write", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		close(started)
		<-release // An ordinary Go Tool may ignore cancellation during an effect.
		oldEffects.Add(1)
		return tool.Result{}, nil
	}}
	declaration := &codemode.Tool{Name: "js_repl", Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
		go func() { defer close(hostDone); _, err := host.Call(ctx, "Write", []byte(`{}`)); hostResult <- err }()
		<-ctx.Done() // The adapter itself stops promptly and does no more work.
		return nil, ctx.Err()
	})}
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hook.Set{ToolPolicy: mcpFixturePolicy()}, DefaultPolicyMode: "mcp-test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var firstFence session.SessionFence
	if fenced {
		firstFence, err = store.AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "review-host"})
		if err != nil {
			t.Fatal(err)
		}
		ctx = session.ContextWithRuntimeFence(ctx, firstFence)
	}
	run, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "cancel while native effect is in flight", AgentSpec: agent.AgentSpec{Model: codeModeModel(false), Tools: []tool.Tool{declaration, write}}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	cancel()
	wait, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()
	terminal := run.Handle.WaitCompletion(wait)
	if errors.Is(terminal, context.DeadlineExceeded) {
		// A corrected implementation may retain completion until this
		// producer actually stops. Quiesce it before releasing authority.
		close(release)
		<-hostDone
		_ = run.Handle.WaitCompletion(t.Context())
		if fenced {
			if err := store.ReleaseSessionFence(t.Context(), session.SessionFenceReleaseRequest(firstFence)); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	// This is the existing Host contract: WaitCompletion proves quiescence and
	// authorizes exact fence release. The test does not force or bypass a fence.
	if fenced {
		if err := store.ReleaseSessionFence(wait, session.SessionFenceReleaseRequest(firstFence)); err != nil {
			t.Fatal(err)
		}
	}
	// Observe a fresh canonical Turn while the old producer is still blocked.
	nextDeclaration := &codemode.Tool{Name: "js_repl", Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
		_, err := host.Call(ctx, "NextWrite", []byte(`{}`))
		return []byte(`{}`), err
	})}
	nextWrite := tool.NamedTool{Def: tool.Definition{Name: "NextWrite", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { nextEffects.Add(1); return tool.Result{}, nil }}
	nextCtx := wait
	if fenced {
		nextFence, err := store.AcquireSessionFence(wait, session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "review-host"})
		if err != nil {
			close(release)
			t.Fatal(err)
		}
		defer func() {
			_ = store.ReleaseSessionFence(context.WithoutCancel(wait), session.SessionFenceReleaseRequest(nextFence))
		}()
		nextCtx = session.ContextWithRuntimeFence(nextCtx, nextFence)
	}
	next, nextErr := rt.Run(nextCtx, agent.RunRequest{SessionRef: active.SessionRef, Input: "next turn", AgentSpec: agent.AgentSpec{Model: codeModeModel(false), Tools: []tool.Tool{nextDeclaration, nextWrite}}})
	if nextErr == nil {
		nextErr = next.Handle.WaitCompletion(wait)
	}
	beforeRelease := nextEffects.Load()
	close(release)
	select {
	case <-hostDone:
	case <-wait.Done():
		t.Fatal("old Host did not finish after release")
	}
	t.Logf("late Host result: %v", <-hostResult)
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == session.EventTypeToolResult && event.Tool != nil {
			r := event.Tool
			if r.Parent != nil && r.Name == "Write" {
				t.Logf("original child identity=%s call_id=%s status=%s revision=%d visibility=%s", r.Execution.ItemID, r.ID, r.Status, event.Seq, event.Visibility)
			}
		}
		if session.IsCanonicalHistoryEvent(event) && event.Tool != nil && event.Tool.Parent != nil {
			t.Error("nested recovery contaminated canonical model protocol")
		}
	}
	if beforeRelease != 0 {
		t.Fatalf("Session fence released with old Host producer alive: first_terminal=%v next_terminal=%v next_effects=%d old_effects_after_release=%d", terminal, nextErr, beforeRelease, oldEffects.Load())
	}
}

// Redirect one synthetic sensitive filename to private test bytes. No user file
// is opened, read, or changed. All other filesystem behavior uses the real host.
type reviewMappedFS struct {
	sandbox.FileSystem
	sensitive, fixture string
}

func (f reviewMappedFS) Open(path string) (*os.File, error) {
	if path == f.sensitive {
		return os.Open(f.fixture)
	}
	return f.FileSystem.Open(path)
}

type reviewSourceRuntime struct {
	sandbox.Runtime
	fsys sandbox.FileSystem
}

func (r reviewSourceRuntime) FileSystem() sandbox.FileSystem                       { return r.fsys }
func (r reviewSourceRuntime) FileSystemFor(sandbox.Constraints) sandbox.FileSystem { return r.fsys }

func TestReviewCodeModeFileSourceUsesReadAuthorization(t *testing.T) {
	store, active, root := codeModeStore(t)
	base, err := host.New(host.Config{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	sensitive := filepath.Join(homeDir, ".ssh", "codemode-independent-review-fixture.js")
	fixture := filepath.Join(root, "safe-fixture.js")
	if err := os.WriteFile(fixture, []byte("synthetic sensitive bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	// Verify the existing path policy denies this exact filename for Read.
	readArgs, _ := json.Marshal(map[string]string{"path": sensitive})
	decision, err := presets.WorkspaceWriteMode().DecideTool(t.Context(), policy.ToolContext{Session: active, Tool: tool.Definition{Name: filesystem.ReadToolName}, Call: tool.Call{Name: filesystem.ReadToolName, Input: readArgs}})
	if err != nil || decision.Action != policy.ActionDeny {
		t.Fatalf("Read control: action=%s err=%v", decision.Action, err)
	}
	var executed atomic.Bool
	declaration := &codemode.Tool{Name: "js_repl", Sandbox: reviewSourceRuntime{Runtime: base, fsys: reviewMappedFS{FileSystem: base.FileSystem(), sensitive: sensitive, fixture: fixture}}, Executor: codeExecutorFunc(func(_ context.Context, source codemode.Source, _ codemode.Host) (json.RawMessage, error) {
		executed.Store(source.Code == "synthetic sensitive bytes")
		return []byte(`{}`), nil
	})}
	fileArgs, _ := json.Marshal(map[string]string{"file": sensitive})
	llm := codeModeModel(false)
	llm.steps = func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "script", Name: "js_repl", Args: string(fileArgs)}}
		}
		return nil
	}
	registry, err := presets.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hook.Set{ToolPolicy: registry}, DefaultPolicyMode: policy.ProfileWorkspaceWrite})
	if err != nil {
		t.Fatal(err)
	}
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "read synthetic source", AgentSpec: agent.AgentSpec{Model: llm, Tools: []tool.Tool{declaration}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	if executed.Load() {
		t.Fatal("Code Mode executed file bytes denied by the existing Read path policy")
	}
}
