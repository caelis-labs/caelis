package runtime

import (
	"context"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"sync/atomic"
	"testing"
)

func TestCodeModeChildJournalRequiresOriginalAdmission(t *testing.T) {
	for _, name := range []string{"missing", "wrong-call", "wrong-name", "wrong-parent", "nil-validator", "valid", "ordinary-parent", "no-parent"} {
		t.Run(name, func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			call := tool.Call{ID: "child", Name: "Write", Input: []byte(`{}`), Execution: tool.InvocationContext{SessionID: active.SessionID, TurnID: "turn", ItemID: "step"}, Parent: &tool.ParentCall{Kind: codemode.ToolKind, Name: "arbitrary_script_name", ID: "outer"}}
			var effects, validations atomic.Int32
			binding := &codeModeAdmissionBinding{callID: call.ID, name: call.Name, parent: *call.Parent, validate: func(context.Context) (func(), error) {
				validations.Add(1)
				return func() {}, nil
			}}
			switch name {
			case "wrong-call":
				binding.callID = "another-child"
			case "wrong-name":
				binding.name = "AnotherTool"
			case "wrong-parent":
				binding.parent.ID = "another-parent"
			case "nil-validator":
				binding.validate = nil
			case "ordinary-parent":
				call.Parent = &tool.ParentCall{Name: "js_repl", ID: "ordinary-provenance"}
			case "no-parent":
				call.Parent = nil
			}
			ctx := t.Context()
			if name != "missing" {
				ctx = context.WithValue(ctx, codeModeAdmissionKey{}, binding)
			}
			wrapped := journaledTool{base: tool.NamedTool{Def: tool.Definition{Name: "Write"}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { effects.Add(1); return tool.Result{}, nil }}, sessions: store, sessionRef: active.SessionRef, runID: "run", turnID: "turn"}
			_, err := wrapped.Call(ctx, call)
			allowed := name == "valid" || name == "ordinary-parent" || name == "no-parent"
			if allowed != (err == nil) || allowed != (effects.Load() == 1) {
				t.Fatalf("allowed=%v effects=%d error=%v", allowed, effects.Load(), err)
			}
			if name == "valid" && validations.Load() != 1 || name != "valid" && validations.Load() != 0 {
				t.Fatalf("unexpected validation count=%d", validations.Load())
			}
			if !allowed {
				events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
				if err != nil {
					t.Fatal(err)
				}
				for _, event := range events {
					if event.Journal != nil && event.Journal.ToolExecution != nil {
						t.Fatal("unbound child reached durable execution claim")
					}
				}
			}
		})
	}
}
