package gatewayapp_test

import (
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
return await %s(%s);`, target, target+"(args)", string(mustDTWJSON(required)), target, input)
			if !fixture {
				script = fmt.Sprintf(`const r=await tools.search({name:%q});if(r.call_path!==%q||r.input_schema.properties.query.type!=="string")throw Error("apps schema");return await %s(%s);`, target, target+"(args)", target, input)
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

func mustDTWJSON(value any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}
