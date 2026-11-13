package runtime

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

func TestJSExecParallelSafeSameTargetCallsOverlap(t *testing.T) {
	testJSExecConcurrentTargets(t, true)
}

func TestJSExecDistinctDefaultTargetsOverlap(t *testing.T) {
	testJSExecConcurrentTargets(t, false)
}

func testJSExecConcurrentTargets(t *testing.T, sameTarget bool) {
	t.Helper()
	for _, mcp := range []bool{false, true} {
		t.Run(fmt.Sprint("mcp-", mcp), func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			started, release := make(chan struct{}, 2), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			var effects, completed atomic.Int32
			def := tool.Definition{Name: "Read", InputSchema: map[string]any{"type": "object"}}
			if mcp {
				def = fixtureMCPDefinition("parallel-default")
				def.Name = "z__target"
				def.Metadata[tool.MetadataMCPSourceTrusted] = true
			}
			def.Capabilities.ParallelSafe = sameTarget
			target := tool.NamedTool{Def: def, Invoke: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
				effects.Add(1)
				started <- struct{}{}
				select {
				case <-release:
					completed.Add(1)
					return tool.Result{Content: []model.Part{model.NewTextPart("ok")}}, nil
				case <-ctx.Done():
					return tool.Result{}, ctx.Err()
				}
			}}
			observed := make(chan error, 1)
			go func() {
				defer unblock()
				wait, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				for range 2 {
					select {
					case <-started:
					case <-wait.Done():
						observed <- fmt.Errorf("both calls did not enter tool execution: %w", wait.Err())
						return
					}
				}
				observed <- nil
			}()
			other := target
			if !sameTarget {
				other.Def = tool.CloneDefinition(def)
				other.Def.Name += "Other"
			}
			code := fmt.Sprintf(`await tools.search(%q); await tools.search(%q); const replies = await Promise.all([tools.call(%q, {}), tools.call(%q, {})]); return {count: replies.length};`, def.Name, other.Def.Name, def.Name, other.Def.Name)
			targets := []tool.Tool{target}
			if !sameTarget {
				targets = append(targets, other)
			}
			var spec agent.AgentSpec
			if mcp {
				source := &h1Source{items: targets, seen: map[string]bool{}}
				driver := &h1Model{s: source, main: func(i int, _ *model.Request) []model.ToolCall {
					if i == 0 {
						return h1Script(code)
					}
					return nil
				}}
				spec = h1Spec(source, driver)
			} else {
				driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
					if i == 0 {
						return h1Script(code)
					}
					return nil
				}}
				spec = agent.AgentSpec{Model: driver, Tools: append(targets, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}})}
			}
			events, _ := runCodeMode(t, store, active, spec, nil, nil)
			if err := <-observed; err != nil {
				t.Fatal(err)
			}
			if effects.Load() != 2 || completed.Load() != 2 {
				t.Fatalf("effects=%d completed=%d", effects.Load(), completed.Load())
			}
			found := false
			for _, event := range events {
				if event.Tool != nil && event.Tool.Name == codemode.ToolName && event.Type == session.EventTypeToolResult {
					if event.Message != nil {
						for _, part := range event.Message.Parts {
							if part.ToolResult != nil && !part.ToolResult.IsError {
								found = true
							}
						}
					}
				}
			}
			if !found {
				t.Fatal("concurrent script did not produce a successful result")
			}
		})
	}
}
