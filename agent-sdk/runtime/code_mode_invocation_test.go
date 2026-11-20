package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

func TestCodeModeGojaUsesFrozenOccurrence(t *testing.T) {
	for _, approve := range []bool{false, true} {
		t.Run(map[bool]string{false: "allow", true: "approval"}[approve], func(t *testing.T) {
			files, active, root := codeModeStore(t)
			var actualParent *tool.ParentCall
			var policyExecution, approvalExecution tool.InvocationContext
			rawFull := json.RawMessage(`{"n":9007199254740993123456789,"body":"` + strings.Repeat("x", 45000) + `"}`)
			completeFull := tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: rawFull}, tool.Image{Data: []byte("abc"), MIMEType: "image/png", Name: "fixture"}}}
			target := tool.NamedTool{Def: tool.Definition{Name: "ReadFact", InputSchema: map[string]any{"type": "object"}}, Invoke: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
				actualParent = tool.ParentFromContext(ctx)
				return completeFull, nil
			}}
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return []model.ToolCall{{ID: "script", Name: codemode.ToolName, Args: `{"code":"const b=await tools.call('ReadFact',{});if(b.data.body.length!==45000)throw Error('truncated full Body');return {length:b.data.body.length};"}`}}
				}
				return nil
			}}
			registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "allow", Decide: func(ctx context.Context, input policy.ToolContext) (policy.Decision, error) {
				if input.Tool.Name == codemode.ToolName {
					policyExecution = tool.InvocationFromContext(ctx)
					if approve {
						return policy.Decision{Action: policy.ActionAskApproval, Approval: &session.ProtocolApproval{}}, nil
					}
				}
				return policy.Decision{Action: policy.ActionAllow}, nil
			}}}
			rt, err := New(Config{Sessions: files, TaskStore: sessionfile.NewTaskStore(files), AgentFactory: chat.Factory{}, PolicyRegistry: registry, DefaultPolicyMode: "allow"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = rt.ReleaseCodeModeEnvironments() })
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, ApprovalRequester: approvalRequesterFunc(func(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
				approvalExecution = req.Execution
				return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true}, nil
			}), AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{target, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}})
			if err != nil {
				t.Fatal(err)
			}
			if err = run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			reopened := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
			events, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			pairs, err := session.PairToolFacts(events)
			if err != nil {
				t.Fatal(err)
			}
			var outer, nested *session.Event
			for _, pair := range pairs {
				if pair.Call.Tool.Name == codemode.ToolName {
					outer = pair.Call
				}
				if pair.Call.Tool.Name == "ReadFact" {
					nested = pair.Call
				}
			}

			for _, pair := range pairs {
				if pair.Call.Tool.Name == "ReadFact" {
					if pair.Result == nil || !reflect.DeepEqual(pair.Result.Tool.Result, &completeFull) {
						t.Fatal("full nested result lost structured content, media or numeric tokens on reopen")
					}
				}
			}

			if outer == nil || nested == nil || actualParent == nil {
				t.Fatalf("missing actual chain: outer=%v nested=%v actual=%+v", outer, nested, actualParent)
			}
			t.Logf("frozen parent=%+v; policy occurrence=%+v; dispatched parent=%+v; persisted child parent=%+v", outer.Tool.Execution, policyExecution, actualParent.ItemID, nested.Tool.Parent.Execution)
			for _, pair := range pairs {
				if pair.Call.Tool.Name == codemode.ToolName && (pair.Result == nil || pair.Result.Tool.Execution != outer.Tool.Execution) {
					t.Fatal("outer result changed frozen occurrence")
				}
			}
			if approve && approvalExecution != outer.Tool.Execution {
				t.Fatal("approval changed frozen occurrence")
			}
			for _, e := range events {
				if e.Approval != nil && e.Approval.ToolName == codemode.ToolName && e.Approval.ItemID != outer.Tool.Execution.ItemID {
					t.Fatal("persisted approval changed frozen occurrence")
				}
			}
			expectedTask, err := commandTaskID(active.SessionRef, "code-mode/"+outer.Tool.Execution.TurnID+"/"+outer.Tool.Execution.ItemID+"/"+outer.Tool.ID)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := sessionfile.NewTaskStore(reopened).ListSession(t.Context(), active.SessionRef)
			if err != nil || len(entries) != 1 || entries[0].TaskID != expectedTask {
				t.Fatalf("Task identity differs from canonical occurrence: %+v %v", entries, err)
			}
			assertSubagentSagaModelRoundTrip(t, reopened, active.SessionRef)
			if outer.Tool.Execution != policyExecution {
				t.Error("Policy used an occurrence different from the accepted ToolCall")
			}
			if outer.Tool.Execution.ItemID != actualParent.ItemID || outer.Tool.Execution != nested.Tool.Parent.Execution {
				t.Fatal("nested Goja call parent points to an occurrence never accepted in Session")
			}

		})
	}
}

func TestCodeModeProducerRejectsUnpreparedOrChangedCall(t *testing.T) {
	for _, scenario := range []string{"caller_identity", "changed_identity", "changed_input", "changed_parent", "changed_binding"} {
		t.Run(scenario, func(t *testing.T) {
			called := false
			original := tool.Call{ID: "script", Name: codemode.ToolName, Input: []byte(`{"code":"return 1"}`)}
			execution, parent, target := tool.InvocationContext{ItemID: "frozen"}, tool.ParentCall{ID: "parent"}, tool.Binding{Name: "binding"}
			ctx := toolbinding.WithPrepared(toolbinding.WithOccurrence(t.Context(), execution, &parent, &target), original, nil, nil)
			call := tool.CloneCall(original)
			switch scenario {
			case "caller_identity":
				ctx = toolbinding.WithOccurrence(t.Context(), execution, &parent, &target)
			case "changed_identity":
				execution.ItemID = "forged"
			case "changed_input":
				original.Input[0] = '['
				call.Input = original.Input
			case "changed_parent":
				parent.ID = "forged"
			case "changed_binding":
				target.Name = "forged"
			}
			ctx = toolbinding.WithOccurrence(ctx, execution, &parent, &target)
			producer := codeModeProducerTool{owner: t.Context(), scope: newTaskContinuationScope(), base: tool.NamedTool{Def: tool.Definition{Name: codemode.ToolName}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { called = true; return tool.Result{}, nil }}}
			if _, err := producer.Call(ctx, call); err == nil || called {
				t.Fatalf("unprepared producer dispatched: %v %v", called, err)
			}
		})
	}
}
