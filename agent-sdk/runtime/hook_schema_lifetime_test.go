package runtime

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestHooksTypedSchemaLateMutationCannotChangeBinding(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(map[bool]string{false: "in-place", true: "after-timeout"}[late], func(t *testing.T) {
			store, active := newTestSessionService(t, "typed-binding")
			schema := map[string]any{"type": "object", "properties": map[string]any{"real": map[string]any{"type": "string"}}, "required": []string{"real"}, "x-groups": map[string][]map[string][]string{"items": {{"required": {"real"}}}}, "x-array": [1]map[string][]string{{"required": {"real"}}}}
			target := tool.NamedTool{Def: tool.Definition{Name: "Read", InputSchema: schema}}
			baseline := tool.CloneDefinition(target.Definition())
			release, finished := make(chan struct{}), make(chan struct{})
			var providers atomic.Int64
			h := hook.Set{ModelPrepare: []hook.ModelPrepare{{Name: "typed-mutation", Timeout: 10 * time.Millisecond, Apply: func(_ context.Context, _ hook.PrepareContext, p hook.ModelPreparation) (hook.ModelPreparation, error) {
				defer close(finished)
				if late {
					<-release
				}
				s := p.Request.Tools[0].Function.Parameters
				s["required"].([]string)[0] = "invented"
				s["x-groups"].(map[string][]map[string][]string)["items"][0]["required"][0] = "invented"
				s["x-array"].([1]map[string][]string)[0]["required"][0] = "invented"
				return p, nil
			}}}}
			llm := scriptedTestModel{generate: func(context.Context, *model.Request) *model.Response {
				providers.Add(1)
				return textResponse("unreachable", model.Usage{})
			}}
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: h})
			if err != nil {
				t.Fatal(err)
			}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "request", AgentSpec: agent.AgentSpec{Model: llm, Tools: []tool.Tool{target}}})
			if err != nil {
				t.Fatal(err)
			}
			if err = run.Handle.WaitCompletion(t.Context()); err == nil {
				t.Fatal("unbound mutation accepted")
			}
			if late {
				close(release)
			}
			<-finished
			if providers.Load() != 0 || !reflect.DeepEqual(target.Definition(), baseline) {
				t.Fatal("detached or late schema mutation changed callable or reached provider")
			}
		})
	}
}
