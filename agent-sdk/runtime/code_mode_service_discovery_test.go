package runtime

import (
	"context"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

func TestCodeModeConcurrentSearchFailuresRetainEachAttempt(t *testing.T) {
	entered, release := make(chan struct{}, 2), make(chan struct{})
	host := &codeModeHost{ctx: t.Context(), owner: runtimeCodeModeTool{lookup: func(context.Context, string) (tool.Tool, bool, error) {
		entered <- struct{}{}
		<-release
		return nil, false, tool.NewError(tool.ErrorCodeNotFound, "directory unavailable")
	}}}
	done := make(chan error, 2)
	for _, query := range []string{"first", "second"} {
		go func() {
			_, err := host.Search(t.Context(), codemode.SearchRequest{Name: query})
			done <- err
		}()
	}
	<-entered
	<-entered
	close(release)
	for range 2 {
		if err := <-done; err == nil {
			t.Fatal("search failure became success")
		}
	}
	if len(host.failures) != 2 {
		t.Fatalf("concurrent discovery lost a failure: %+v", host.failures)
	}
}

func TestCodeModeServiceFiltersEligibilityBeforeLimit(t *testing.T) {
	store, active, _ := codeModeStore(t)
	source := h1Directory(20)
	for i := 0; i < 16; i++ {
		one := source.items[i].(h1Tool)
		if i < 8 {
			one.def.RequiredModelCapabilities = model.Capabilities{ImageInput: true}
		} else {
			one.def.ExecutionRequirements = &tool.ExecutionRequirements{Sandbox: sandbox.CapabilitySet{CommandExec: true}}
		}
		source.items[i] = one
	}
	m := &h1Model{s: source, main: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return h1Script(`const s=(await tools.search()).json();if(s.tools.length!==12||s.truncated||s.next||s.omitted_count)throw Error("eligibility after limit: "+JSON.stringify(s));return await tools.call("z__target",{value:1});`)
		}
		return nil
	}}
	runCodeMode(t, store, active, agent.AgentSpec{Model: m, Tools: []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}, DeferredTools: source}, nil, nil)
	if source.snapshot().Effects != 1 {
		t.Fatalf("later eligible tools were hidden: %+v", source.snapshot())
	}
}

func TestCodeModeServiceFiltersCurrentRuntimeSourceBeforeLimit(t *testing.T) {
	store, active, _ := codeModeStore(t)
	source := h1Directory(20)
	// Discovery's catalog is broader than the assembled execution source. The
	// current Runtime lookup, rather than this catalog, owns source eligibility.
	source.items = source.items[16:]
	m := &h1Model{s: source, main: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return h1Script(`const s=(await tools.search()).json();if(s.tools.length!==4||s.truncated||s.next)throw Error("source scope applied after limit: "+JSON.stringify(s));return await tools.call("z__target",{value:1});`)
		}
		return nil
	}}
	runCodeMode(t, store, active, agent.AgentSpec{Model: m, Tools: []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}, DeferredTools: source}, nil, nil)
	if source.snapshot().Effects != 1 {
		t.Fatalf("current source tools were hidden: %+v", source.snapshot())
	}
}
