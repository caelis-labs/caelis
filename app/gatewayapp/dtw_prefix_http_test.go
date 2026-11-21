package gatewayapp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionprofile"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDTWPrefixMCPHelper(t *testing.T) {
	if !slices.Contains(os.Args, "dtw-prefix-fixture") {
		return
	}
	marker := slices.Index(os.Args, "dtw-prefix-fixture")
	if marker+2 >= len(os.Args) {
		t.Fatal("missing fixture identity and audit path")
	}
	identity, audit := os.Args[marker+1], os.Args[marker+2]
	if len(os.Args) > marker+3 {
		gate := os.Args[marker+3]
		deadline := time.Now().Add(30 * time.Second)
		for {
			if _, err := os.Stat(gate); err == nil {
				break
			} else if !os.IsNotExist(err) || time.Now().After(deadline) {
				t.Fatalf("waiting for catalog gate: %v", err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: identity, Version: "1"}, nil)
	server.AddTool(&mcpsdk.Tool{Name: "apps", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{StructuredContent: map[string]any{"apps": []any{}}}, nil
	})
	server.AddTool(&mcpsdk.Tool{Name: "exec", InputSchema: map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"app", "source"},
		"properties": map[string]any{"app": map[string]any{"type": "string"}, "source": map[string]any{"type": "string"}},
	}}, func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		f, err := os.OpenFile(audit, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		if err := json.NewEncoder(f).Encode(req.Params.Arguments); err != nil {
			return nil, err
		}
		return &mcpsdk.CallToolResult{
			Content:           []mcpsdk.Content{&mcpsdk.TextContent{Text: identity}},
			StructuredContent: json.RawMessage(`{"value":9223372036854775807}`),
		}, nil
	})
	if err := server.Run(t.Context(), &mcpsdk.StdioTransport{}); err != nil {
		t.Fatal(err)
	}
}

func TestDTWPrefixProviderAndMCPInvocation(t *testing.T) {
	for _, fixture := range []bool{false, true} {
		name := "embedded-apps"
		if fixture {
			name = "selected-exec"
		}
		t.Run(name, func(t *testing.T) {
			if !builtinComputerUseSupported() {
				t.Skip("this build has no embedded DTW capability")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			root := t.TempDir()
			provider := &nativeModelScript{}
			host := startApplicationHTTPHost(t, filepath.Join(root, "store"), root, provider)
			defer host.close(t)
			connectApplicationChildModel(t, ctx, host, "gpt-4.1")
			client, _ := connectSessionOwnerHTTP(t, ctx, host, "owner", filepath.Join(root, "owner.token"))
			profile := applicationHTTPProfile()
			profile.Tools = nil
			launch := func(name, audit, workdir string) sessionprofile.MCPServer {
				return sessionprofile.MCPServer{Name: name, Transport: "stdio", Command: os.Args[0], WorkDir: workdir,
					Args: []string{"-test.run=^TestDTWPrefixMCPHelper$", "--", "dtw-prefix-fixture", name, audit}}
			}
			foreignAudit := filepath.Join(root, "foreign-audit")
			profile.MCPServers = []sessionprofile.MCPServer{launch("foreign", foreignAudit, root)}
			server, method := "dtw", "apps"
			input := `{"query":"caelis-prefix-fixture-unmatched","limit":1}`
			var required []string
			audit := filepath.Join(root, "selected-audit")
			if fixture {
				server, method, required = "desktop-world", "exec", []string{"app", "source"}
				bundle := nativeProfilePackageHTTP(t, "0.5.1")
				profile.Plugins = []sessionprofile.Plugin{{Root: bundle, MCPServers: []sessionprofile.MCPServer{launch(server, audit, bundle)}}}
				input = `{"app":"fixture.desktop","source":"return 42;"}`
			}
			sessionResult, err := client.CreateSession(ctx, appserver.CreateSessionRequest{
				WriteBase: appserver.WriteBase{OperationID: "create-dtw-prefix"}, Profile: &profile,
			})
			if err != nil {
				t.Fatal(err)
			}
			sid := sessionResult.SessionID
			feed, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: sid})
			if err != nil {
				t.Fatal(err)
			}
			defer feed.Subscription.Close()
			if approvals := settleNativeProfileHTTPExpect(t, ctx, client, sid, "warm-dtw-prefix", "Initialize fixture catalogs", "", nil); approvals != 0 {
				t.Fatalf("unexpected warm-up approvals: %d", approvals)
			}
			for {
				status, err := client.SessionMCPStatus(ctx, sid)
				if err != nil {
					t.Fatal(err)
				}
				if len(status.Servers) == 2 && status.Servers[0].Ready && status.Servers[1].Ready {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatalf("catalogs never became ready: %+v", status)
				case <-time.After(15 * time.Millisecond):
				}
			}
			prefix := "dtw"
			if fixture {
				prefix = "desktop_world"
			}
			target := prefix + "__" + method
			script := fmt.Sprintf(`
const r=await tools.search({names:[%q,"foreign__exec","absent"]});
const [target,foreign,absent]=r.tools;
if(target.entry.call_path!==%q||foreign.entry.server!=="foreign"||absent.error_code!=="not_found")throw Error("search route mismatch");
if(JSON.stringify(target.entry.input_schema.required||[])!==%q)throw Error("input schema mismatch");
return await %s(%s);`, target, "globalThis."+target+"(args)", string(mustDTWJSON(required)), target, input)
			if !fixture {
				script = fmt.Sprintf(`const r=await tools.search({name:%q});if(r.call_path!==%q||r.input_schema.properties.query.type!=="string")throw Error("apps schema");return await %s(%s);`, target, "globalThis."+target+"(args)", target, input)
			}
			provider.set(nativeModelTool{Name: target, Input: input}, nativeModelTool{
				Name: "js_exec", Input: string(mustDTWJSON(map[string]any{"code": script})),
			})
			wantApprovals := 0
			if fixture {
				wantApprovals = 1
			}
			approvals := settleNativeProfileHTTPExpect(t, ctx, client, sid, "call-dtw-prefix", "Compare both routes", target, func() {
				if _, err := os.Stat(audit); !os.IsNotExist(err) {
					t.Fatalf("exec ran before app approval: %v", err)
				}
			})
			if approvals != wantApprovals {
				t.Fatalf("app approvals = %d, want %d", approvals, wantApprovals)
			}
			provider.mu.Lock()
			last := append([]byte(nil), provider.seen[len(provider.seen)-1]...)
			provider.mu.Unlock()
			var request struct {
				Tools []struct {
					Function struct {
						Name       string
						Parameters struct{ Required []string }
					}
				}
			}
			if err := json.Unmarshal(last, &request); err != nil {
				t.Fatal(err)
			}
			counts := map[string]int{}
			for _, entry := range request.Tools {
				def := entry.Function
				counts[def.Name]++
				if def.Name == target && !slices.Equal(def.Parameters.Required, required) {
					t.Fatalf("final provider required arguments = %v, want %v", def.Parameters.Required, required)
				}
			}
			if counts[prefix+"__apps"] != 1 || counts[prefix+"__exec"] != 1 || counts["js_exec"] != 1 ||
				counts["foreign__apps"] != 0 || counts["foreign__exec"] != 0 || counts["tool_search"] != 0 || counts["mcp_call"] != 0 {
				t.Fatalf("final provider tools = %v", counts)
			}
			events, err := host.stack.Sessions().Events(ctx, session.EventsRequest{SessionRef: session.SessionRef{SessionID: sid}, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			var top, child, scriptResult *session.EventTool
			for _, event := range events {
				if event.Type != session.EventTypeToolResult || event.Tool == nil {
					continue
				}
				switch {
				case event.Tool.Name == target && event.Tool.Parent == nil:
					top = event.Tool
				case event.Tool.Name == target && event.Tool.Parent != nil:
					child = event.Tool
				case event.Tool.Name == "js_exec":
					scriptResult = event.Tool
				}
			}
			if top == nil || child == nil || scriptResult == nil || child.Parent.CallID != scriptResult.ID ||
				top.Binding == nil || child.Binding == nil || *top.Binding != *child.Binding || top.ModelCall != nil {
				t.Fatalf("canonical MCP occurrences lost source or relation: top=%+v child=%+v script=%+v", top, child, scriptResult)
			}
			for _, fact := range []*session.EventTool{top, child, scriptResult} {
				if fact.Outcome != session.ToolOutcomeSucceeded || fact.Result == nil {
					t.Fatalf("tool %s did not settle: %+v", fact.Name, fact)
				}
			}
			if !reflect.DeepEqual(top.Result, child.Result) || !reflect.DeepEqual(top.Result, scriptResult.Result) {
				t.Fatalf("returning Body changed original tool content: %v / %v / %v", top.Result, child.Result, scriptResult.Result)
			}
			if fixture {
				got := top.Result.Content
				if len(got) != 2 || got[0] != tool.Text(server) {
					t.Fatalf("same method source resolution or text changed: %+v", got)
				}
				expected := input + "\n" + input + "\n"
				raw, err := os.ReadFile(audit)
				if err != nil || string(raw) != expected {
					t.Fatalf("MCP arguments or call count = %q, want %q (%v)", raw, expected, err)
				}
			}
			if _, err := os.Stat(foreignAudit); !os.IsNotExist(err) {
				t.Fatalf("foreign exec dispatched: %v", err)
			}
		})
	}
}

func TestApplicationDTWCallbackPrecedenceAfterReadinessHTTP(t *testing.T) {
	if !builtinComputerUseSupported() {
		t.Skip("direct DTW tools are only selected on supported native targets")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	provider := &nativeModelScript{}
	host := startApplicationHTTPHost(t, filepath.Join(root, "store"), root, provider)
	defer host.close(t)
	connectApplicationChildModel(t, ctx, host, "gpt-4.1")
	client, scope := connectSessionOwnerHTTP(t, ctx, host, "owner", filepath.Join(root, "owner.token"))

	const serverName = "desktop-world"
	const target = "desktop_world__exec"
	const sibling = "desktop_world__apps"
	gate, audit := filepath.Join(root, "ready"), filepath.Join(root, "audit")
	bundle := nativeProfilePackageHTTP(t, "0.5.1")
	profile := applicationHTTPProfile()
	profile.Tools[0].Name = target
	profile.Tools[0].ResultFormat = sessionprofile.ResultFormatContentV1
	profile.Plugins = []sessionprofile.Plugin{{Root: bundle, MCPServers: []sessionprofile.MCPServer{{
		Name: serverName, Transport: "stdio", Command: os.Args[0], WorkDir: bundle,
		Args: []string{"-test.run=^TestDTWPrefixMCPHelper$", "--", "dtw-prefix-fixture", serverName, audit, gate},
	}}}}
	created, err := client.CreateSession(ctx, appserver.CreateSessionRequest{
		WriteBase: appserver.WriteBase{OperationID: "create-collision"}, Profile: &profile,
	})
	if err != nil || created.Outcome != appserver.OutcomeCommitted || created.SessionID == "" {
		t.Fatalf("CreateSession: %+v, %v", created, err)
	}
	sid := created.SessionID
	feed, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	// Keep the same activation resident across the two prompts.
	defer feed.Subscription.Close()
	prompt := func(operation string) {
		t.Helper()
		result, err := client.Prompt(ctx, appserver.PromptRequest{
			WriteBase: appserver.WriteBase{SessionID: sid, OperationID: operation}, Input: operation, SourceKind: "user",
		})
		if err != nil || (result.Outcome != appserver.OutcomeCommitted && result.Outcome != appserver.OutcomeAccepted) {
			t.Fatalf("Prompt %s: %+v, %v", operation, result, err)
		}
	}
	assertDeclarations := func(requests [][]byte, wantApps int) {
		t.Helper()
		if len(requests) == 0 {
			t.Fatal("prompt did not reach the provider")
		}
		for _, raw := range requests {
			var request struct {
				Tools []struct {
					Function struct {
						Name        string
						Description string
					}
				}
			}
			if err := json.Unmarshal(raw, &request); err != nil {
				t.Fatal(err)
			}
			counts := map[string]int{}
			for _, entry := range request.Tools {
				def := entry.Function
				counts[def.Name]++
				if def.Name == target && def.Description != profile.Tools[0].Description {
					t.Fatalf("provider saw the MCP definition instead of the callback: %+v", def)
				}
			}
			if counts[target] != 1 || counts[sibling] != wantApps {
				t.Fatalf("provider declarations: %v, want one %s and %d %s", counts, target, wantApps, sibling)
			}
		}
	}
	prompt("before-readiness")
	waitApplicationHTTPIdle(t, ctx, client, sid)
	provider.mu.Lock()
	beforeRequests := slices.Clone(provider.seen)
	provider.mu.Unlock()
	assertDeclarations(beforeRequests, 0)
	status, err := client.SessionMCPStatus(ctx, sid)
	if err != nil || len(status.Servers) != 1 || status.Servers[0].Name != serverName || status.Servers[0].Ready {
		t.Fatalf("expected the selected catalog to be gated: %+v, %v", status, err)
	}
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for !status.Servers[0].Ready {
		select {
		case <-ctx.Done():
			t.Fatalf("selected catalog never became ready: %+v", status)
		case <-time.After(15 * time.Millisecond):
		}
		status, err = client.SessionMCPStatus(ctx, sid)
		if err != nil || len(status.Servers) != 1 || status.Servers[0].Name != serverName {
			t.Fatalf("selected catalog disappeared: %+v, %v", status, err)
		}
	}

	provider.set(nativeModelTool{Name: target, Input: `{"key":"native"}`}, nativeModelTool{
		Name: "js_exec", Input: string(mustDTWJSON(map[string]string{"code": `
const entry = await tools.search({name:"desktop_world__exec"});
if (entry.server || entry.input_schema.properties.key.type !== "string") throw new Error("callback schema shadowed");
const result = await tools.call("desktop_world__exec", {key:"js"});
if (result.data.owner !== "application") throw new Error("callback result lost");
return {verified:"COLLISION_JS_VERIFIED"};`})),
	})
	prompt("after-readiness")
	var turnID string
	for _, input := range []string{`{"key":"native"}`, `{"key":"js"}`} {
		calls, err := client.WaitSessionToolCalls(ctx, sid)
		if err != nil || len(calls) != 1 {
			t.Fatalf("callback for %s: %+v, %v", input, calls, err)
		}
		call := calls[0]
		if call.Name != target || string(call.Arguments) != input || call.Scope != scope || call.SessionID != sid ||
			call.ConfigurationRevision != 1 || call.ToolsVersion != profile.ToolsVersion || call.TurnID == "" || (turnID != "" && call.TurnID != turnID) {
			t.Fatalf("callback identity for %s: %+v", input, call)
		}
		turnID = call.TurnID
		if _, err := client.ClaimSessionToolCall(ctx, sid, call.ID); err != nil {
			t.Fatal(err)
		}
		if err := client.CompleteSessionToolCall(ctx, sid, call.ID, sessionprofile.CallResult{
			Outcome: "succeeded", ResultFormat: sessionprofile.ResultFormatContentV1,
			Content: json.RawMessage(`[{"type":"text","text":"application callback"}]`), StructuredContent: map[string]any{"owner": "application"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	waitApplicationHTTPIdle(t, ctx, client, sid)
	provider.mu.Lock()
	after := slices.Clone(provider.seen[len(beforeRequests):])
	provider.mu.Unlock()
	assertDeclarations(after, 1)
	if !bytes.Contains(after[len(after)-1], []byte("COLLISION_JS_VERIFIED")) {
		t.Fatal("JavaScript did not verify the callback result")
	}
	if _, err := os.Stat(audit); !os.IsNotExist(err) {
		t.Fatalf("colliding MCP exec must not dispatch: %v", err)
	}
}

func mustDTWJSON(value any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}
