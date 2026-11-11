package runtime

import (
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"strings"
	"testing"
)

func TestJSExecStaticDiscoveredUnionStillRejectsDuplicateRegistrations(t *testing.T) {
	store, active, _ := codeModeStore(t)
	s := h1Directory(2)
	driver := &h1Model{s: s, main: func(i int, _ *model.Request) []model.ToolCall {
		switch i {
		case 0:
			return h1Script(`return await tools.search("a__target");`)
		case 1:
			return h1Script(`await tools.call("a__target",{value:1});`)
		}
		return nil
	}}
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "duplicate registration", AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{s.items[0], s.items[0], h1Search(s), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}})
	if err == nil {
		err = run.Handle.WaitCompletion(t.Context())
	}
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("true duplicate not rejected at assembly: %v", err)
	}
	if s.snapshot().Effects != 0 {
		t.Fatal("duplicate static registrations executed")
	}
}

func TestJSExecStaticAndDeferredUnionKeepsBothBindings(t *testing.T) {
	store, active, _ := codeModeStore(t)
	s := h1Directory(2)
	driver := &h1Model{s: s, main: func(i int, _ *model.Request) []model.ToolCall {
		switch i {
		case 0:
			return h1Script(`return [await tools.search("a__target"),await tools.search("z__target")];`)
		case 1:
			return h1Script(`await tools.call("a__target",{value:1});await tools.call("z__target",{value:1});`)
		}
		return nil
	}}
	spec := h1Spec(s, driver)
	spec.Tools = append([]tool.Tool{s.items[0]}, spec.Tools...)
	runCodeMode(t, store, active, spec, nil, nil)
	if stats := s.snapshot(); stats.Effects != 2 || stats.ExactRequests != 2 || stats.QueryRequests != 0 {
		t.Fatalf("static/deferred union lost binding: %+v", stats)
	}
}
