package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

func TestHooksInvokeFailureSanitizesEveryResultSurface(t *testing.T) {
	for _, nested := range []bool{false, true} {
		for _, mode := range []string{"error", "panic", "timeout", "invalid", "unknown", "accepted"} {
			t.Run(fmt.Sprintf("nested=%v/%s", nested, mode), func(t *testing.T) {
				root := t.TempDir()
				store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
				active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "hooks", UserID: "safe-result"})
				if err != nil {
					t.Fatal(err)
				}
				var effects atomic.Int64

				raw := json.RawMessage(`{"value":"RAW-SECRET"}`)
				accepted := json.RawMessage(`{"value":"ACCEPTED"}`)
				target := tool.NamedTool{Def: tool.Definition{Name: "ReadSecret"}, Invoke: func(_ context.Context, c tool.Call) (tool.Result, error) {
					effects.Add(1)
					r := tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: raw}, tool.Resource{URI: "https://example.invalid/RAW-SECRET", MIMEType: "image/png", Name: "fixture"}}}
					if mode == "unknown" {
						return r, errorcode.New(errorcode.UnknownOutcome, "effect unknown")
					}
					return r, nil
				}}
				// The hook also wraps js_exec's full lifecycle; only the deliberate
				// timeout case needs a shorter budget than the normal hook default.
				var timeout time.Duration
				if mode == "timeout" {
					timeout = 500 * time.Millisecond
				}
				hooks := agent.Hooks{ToolInvoke: []agent.ToolInvokeHook{{Name: "redact", Timeout: timeout, Invoke: func(ctx context.Context, e agent.HookContext, next agent.ToolNext) (tool.Result, error) {
					r, err := next(ctx)
					if e.Tool.Name != "ReadSecret" {
						return r, err
					}

					switch mode {
					case "error", "unknown":
						return r, errors.New("redaction failed")
					case "panic":
						panic("redaction panic")
					case "timeout":
						<-ctx.Done()
						return r, nil
					case "invalid":
						r.Content = append(r.Content, tool.StructuredContent{Value: raw})
						return r, nil
					}
					r.Content = []tool.ContentBlock{tool.StructuredContent{Value: accepted}, tool.Resource{URI: "https://example.invalid/accepted", MIMEType: "image/png", Name: "fixture"}}
					return r, err
				}}}}
				hooks.ModelView = []agent.Hook[model.Message, model.Message]{{Name: "summary", Apply: func(_ context.Context, e agent.HookContext, m model.Message) (model.Message, error) {
					if e.Tool.Name == "ReadSecret" && len(m.ToolResults()) > 0 {
						r := m.Parts[0].ToolResult
						r.Content = append([]model.Part(nil), r.Content[:1]...)
					}
					return m, nil
				}}}
				driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
					if i == 0 {
						call := model.ToolCall{ID: "one", Name: "ReadSecret", Args: `{}`}
						if nested {
							call.Name = codemode.ToolName
							call.Args = `{"code":"try{const r=await tools.call('ReadSecret',{});if(r.text.includes('RAW-'+'SECRET'))throw Error('unsafe Body');return r.data;}catch(e){if(JSON.stringify(e.result||{}).includes('RAW-'+'SECRET'))throw Error('unsafe error.result');throw e;}"}`
						}
						return []model.ToolCall{call}
					}
					return nil
				}}
				targets := []tool.Tool{target}
				if nested {
					targets = append(targets, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}})
				}
				rt, err := New(Config{Sessions: store, TaskStore: sessionfile.NewTaskStore(store), AgentFactory: chat.Factory{}, Hooks: hooks, PolicyRegistry: mcpFixturePolicy(), DefaultPolicyMode: "mcp-test"})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = rt.ReleaseCodeModeEnvironments() })
				run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "request", AgentSpec: agent.AgentSpec{Model: driver, Tools: targets}})
				if err != nil {
					t.Fatal(err)
				}
				completion := run.Handle.WaitCompletion(t.Context())
				var hookErr *agent.HookError
				if errors.As(completion, &hookErr) {
					t.Logf("hook %s/%s cause=%v deadline=%v", hookErr.Phase, hookErr.Name, hookErr.Cause, errors.Is(completion, context.DeadlineExceeded))
				}
				if mode == "accepted" && completion != nil {
					t.Fatal(completion)
				}
				if mode == "timeout" && !errors.Is(completion, context.DeadlineExceeded) {
					t.Fatalf("expected deliberate deadline: %v", completion)
				}
				if effects.Load() != 1 {
					events, _ := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
					raw, _ := json.Marshal(events)
					t.Fatalf("effects=%d completion=%v events=%s", effects.Load(), completion, raw)
				}
				live, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
				if err != nil {
					t.Fatal(err)
				}
				reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
				events, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
				if err != nil || !reflect.DeepEqual(live, events) {
					t.Fatalf("full fact replay changed: %v", err)
				}
				encoded, err := json.Marshal(events)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(encoded), "RAW-SECRET") {
					t.Fatalf("raw content/structured/media/Body/error.result/view persisted: %s", encoded)
				}
				pairs, err := session.PairToolFacts(events)
				if err != nil {
					t.Fatal(err)
				}
				found := 0
				for _, p := range pairs {
					if p.Call.Tool.Name != "ReadSecret" {
						continue
					}
					found++
					if p.Result == nil {
						t.Fatal("missing paired fact")
					}
					r := p.Result.Tool
					outcome := session.ToolOutcomeSucceeded
					if mode == "unknown" {
						outcome = session.ToolOutcomeUnknown
					}
					if r.Outcome != outcome {
						t.Fatalf("effect relabeled: %s", r.Outcome)
					}
					if mode == "accepted" {
						structured, ok := r.Result.Structured()
						if r.ProcessingFailure != nil || !ok || string(structured) != string(accepted) || len(r.Result.Content) != 2 {
							t.Fatal("valid result was discarded")
						}
					} else if r.ProcessingFailure == nil || r.Result == nil || len(r.Result.Content) != 1 {
						t.Fatalf("unsafe failed result: %+v", r)
					}
				}
				if found != 1 {
					t.Fatalf("facts=%d", found)
				}
			})
		}
	}
}
