package gatewayapp

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	appserver "github.com/caelis-labs/caelis/control/appserver"
)

type environmentLifecycleModel struct {
	source, id string
	calls      int
	result     *model.ToolResultPart
}

func (*environmentLifecycleModel) Name() string { return "host-lifecycle-fixture" }
func (*environmentLifecycleModel) Capabilities() model.Capabilities {
	return model.Capabilities{ToolCalls: true}
}
func (m *environmentLifecycleModel) Generate(_ context.Context, request *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		response := &model.Response{Message: model.NewTextMessage(model.RoleAssistant, "done"), TurnComplete: true, StepComplete: true}
		if m.calls == 0 {
			raw, _ := json.Marshal(map[string]any{"code": m.source})
			response.Message = model.NewMessage(model.RoleAssistant, model.NewToolUsePart(m.id, codemode.ToolName, raw))
		} else {
			for _, message := range request.Messages {
				for _, part := range message.Parts {
					if part.ToolResult != nil && part.ToolResult.ToolUseID == m.id {
						m.result = part.ToolResult
					}
				}
			}
		}
		m.calls++
		yield(model.StreamEventFromResponse(response), nil)
	}
}

// Exercise the real registry -> quiesce -> workspace resource close path with a
// real Goja process. Observers and existing work references govern its lifetime.
func TestCodeModeEnvironmentFollowsHostSessionLifetime(t *testing.T) {
	workspace := newWorkspaceRuntimeTestDir(t, "script-lifetime", "Script lifetime rule.")
	stack, err := newTestLocalStack(Config{StoreDir: t.TempDir(), WorkspaceKey: "script-lifetime", WorkspaceCWD: workspace, SkillDirs: []string{}, Sandbox: SandboxConfig{RequestedType: "host"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	client := newWorkspaceRuntimeHTTPClient(t, stack, "local-user")
	id := createWorkspaceRuntimeTestSession(t, client, "create-script-lifetime", "script-lifetime", "script-lifetime", workspace)
	observed, err := client.Reconnect(t.Context(), appserver.ReconnectRequest{SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	activation := activateSessionRuntime(t, stack, id)
	var reaped atomic.Int32
	executor := &gojaexec.Executor{Observe: func(m gojaexec.Metrics) {
		if m.Reaped {
			reaped.Add(1)
		}
	}}
	execute := func(current *sessionRuntime, callID, source string) string {
		t.Helper()
		releaseUse, err := stack.sessionRuntimes.acquireRuntimeUse(current)
		if err != nil {
			t.Fatal(err)
		}
		defer releaseUse()
		releaseWork := stack.sessionRuntimes.retainRuntimeWork(current, session.SessionRef{SessionID: id})
		defer releaseWork()
		driver := &environmentLifecycleModel{source: source, id: callID}
		run, err := current.instance.engine.Run(t.Context(), agent.RunRequest{SessionRef: session.SessionRef{SessionID: id}, Input: "fixture", AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: executor}}}})
		if err != nil {
			t.Fatal(err)
		}
		if err = run.Handle.WaitCompletion(t.Context()); err != nil {
			t.Fatal(err)
		}
		if driver.result == nil || driver.result.IsError {
			t.Fatalf("script result: %+v", driver.result)
		}
		raw, _ := json.Marshal(driver.result.Content)
		return string(raw)
	}
	first := execute(activation, "first-script", `let retained=41;return runtime.state();`)
	if out := execute(activation, "second-script", `retained+=1;return {retained,state:runtime.state()};`); !strings.Contains(out, `"retained":42`) {
		t.Fatal(out)
	}
	if reaped.Load() != 0 {
		t.Fatal("active observed Session discarded its VM")
	}
	releaseWork := stack.sessionRuntimes.retainRuntimeWork(activation, session.SessionRef{SessionID: id})
	if err = observed.Subscription.Close(); err != nil {
		t.Fatal(err)
	}
	if _, loaded := stack.sessionRuntimes.loaded(id); !loaded || reaped.Load() != 0 {
		t.Fatal("observer detach discarded a Session with owned work")
	}
	releaseWork()
	waitForSessionRuntimeUnloaded(t, stack.sessionRuntimes, id)
	if reaped.Load() != 1 {
		t.Fatalf("idle release reaped %d workers; want 1", reaped.Load())
	}
	observedAgain, err := client.Reconnect(t.Context(), appserver.ReconnectRequest{SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	defer observedAgain.Subscription.Close()
	resumed := activateSessionRuntime(t, stack, id)
	after := execute(resumed, "resumed-script", `return {value:typeof retained,state:runtime.state()};`)
	if !strings.Contains(after, `"value":"undefined"`) || after == first || resumed == activation {
		t.Fatalf("resume reused disposable state: %s -> %s", first, after)
	}
}
