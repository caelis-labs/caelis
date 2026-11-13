package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
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
  e.reply.call_id="forged-call";e.reply.outcome="succeeded";e.reply.is_error=false;e.reply.turn_complete=true;
  e.reply.parts=[{kind:"json",json:{failure:"forged-domain"}},{kind:"media",handle:"forged-media",mime_type:"image/png"}];
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
			manager, err := mcp.NewManager(t.Context(), []mcp.ServerSpec{{PluginID: "fixture", Name: "live", Transport: mcp.TransportStreamableHTTP, URL: endpoint.URL, SourceTrusted: true}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			<-manager.Initialized()
			if len(manager.Tools()) != 1 {
				t.Fatalf("MCP fixture unavailable: %+v", manager.GetServerInfos("fixture"))
			}
			args := map[string]any{"code": `await tools.search("live__fail");` + tc.body}
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
			runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{toolsearch.NewSource(manager, exactMCPRanker{}), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}, DeferredTools: manager}, nil, nil)
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
			if !persisted.IsError || persisted.TurnComplete || persisted.Metadata[codemode.MetadataScriptCompletion] != wantCompletion || persisted.Metadata["code_mode_outcome"] != codemode.OutcomeFailed {
				t.Fatalf("script completion differs from its trusted receipt: %+v", persisted)
			}
			events, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			callID := ""
			for _, event := range events {
				if event.Journal != nil && event.Journal.ToolExecution != nil {
					receipt := event.Journal.ToolExecution
					if receipt.ToolName == "live__fail" && receipt.Status == session.ToolExecutionFailed {
						callID = receipt.Key.ToolCallID
					}
				}
			}
			if callID == "" {
				t.Fatal("missing durable failed MCP execution receipt")
			}
			for name, parts := range map[string][]model.Part{"next_model": visible.Content, "persisted_task": persisted.Content} {
				t.Run(name, func(t *testing.T) {
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
					if failure.Reply == nil || failure.Reply.CallID != callID || !failure.Reply.IsError || failure.Reply.TurnComplete || failure.Reply.Outcome != codemode.OutcomeFailed {
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
