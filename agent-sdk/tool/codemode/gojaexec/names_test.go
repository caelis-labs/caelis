package gojaexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

type namedTestHost struct {
	testHost
	names   []codemode.ToolRoute
	err     error
	queries int
}

func (h *namedTestHost) ToolNames(context.Context) ([]codemode.ToolRoute, error) {
	h.queries++
	return h.names, h.err
}

func TestOriginalNamesWithoutSearch(t *testing.T) {
	t.Parallel()
	h := &namedTestHost{names: []codemode.ToolRoute{
		{Name: "Read"}, {Name: "odd-native-name"},
		{Name: "mcp_apps", Server: "dtw", Tool: "apps", Plugin: "desktop"},
		{Name: "mcp_docs", Server: "context7", Tool: "query-docs"},
	}}
	var calls []string
	h.call = func(_ context.Context, name string, args json.RawMessage) (codemode.Reply, error) {
		calls = append(calls, name)
		if string(args) != `{"n":1}` {
			t.Errorf("args: %s", args)
		}
		return codemode.Reply{}, nil
	}
	code := `await Read({n:1});await globalThis['odd-native-name']({n:1});await dtw.apps({n:1});await context7['query-docs']({n:1});await tools.native.Read({n:1});await tools.mcp.dtw.apps({n:1});await tools.plugins.desktop.dtw.apps({n:1});await tools.call('mcp_docs',{n:1});return [Object.getPrototypeOf(tools.native)===null,Object.isFrozen(tools.mcp.dtw),Object.isFrozen(tools.plugins.desktop),Object.isFrozen(tools)];`
	out, err := (&Executor{}).Execute(t.Context(), fixed(code), codemode.Limits{}, h)
	if err != nil || string(out.Value) != `[true,true,true,true]` {
		t.Fatalf("%s %v", out.Value, err)
	}
	if h.queries != 1 || strings.Join(calls, ",") != "Read,odd-native-name,mcp_apps,mcp_docs,Read,mcp_apps,mcp_apps,mcp_docs" {
		t.Fatalf("queries=%d calls=%v", h.queries, calls)
	}
}

func TestNameCollisionsPreserveGlobalsAndQualifiedRoutes(t *testing.T) {
	t.Parallel()
	h := &namedTestHost{names: []codemode.ToolRoute{
		{Name: "Object"}, {Name: "tools"}, {Name: "mixed"},
		{Name: "mixed_remote", Server: "mixed", Tool: "run"},
		{Name: "one", Server: "shared", Tool: "run", Plugin: "p1"},
		{Name: "two", Server: "shared", Tool: "run", Plugin: "p2"},
		{Name: "dup"}, {Name: "dup"},
	}}
	var calls []string
	h.call = func(_ context.Context, name string, _ json.RawMessage) (codemode.Reply, error) {
		calls = append(calls, name)
		return codemode.Reply{}, nil
	}
	code := `if(typeof Object!=='function'||typeof tools.call!=='function'||typeof mixed!=='undefined'||typeof dup!=='undefined')throw Error('global collision');let conflicts=0;for(const f of [tools.mcp.shared.run,tools.native.dup]){try{await f({});}catch(e){if(!e.message.includes('ambiguous tool path'))throw e;conflicts++;}}await tools.native.Object({});await tools.native.tools({});await tools.native.mixed({});await tools.mcp.mixed.run({});await tools.plugins.p1.shared.run({});await tools.plugins.p2.shared.run({});return conflicts;`
	out, err := (&Executor{}).Execute(t.Context(), fixed(code), codemode.Limits{}, h)
	if err != nil || string(out.Value) != "2" {
		t.Fatalf("%s %v", out.Value, err)
	}
	if strings.Join(calls, ",") != "Object,tools,mixed,mixed_remote,one,two" {
		t.Fatal(calls)
	}
}

func TestPrototypeSensitiveToolNames(t *testing.T) {
	t.Parallel()
	h := &namedTestHost{names: []codemode.ToolRoute{
		{Name: "__proto__"}, {Name: "constructor"}, {Name: "then"},
		{Name: "proto", Server: "__proto__", Tool: "__proto__", Plugin: "__proto__"},
		{Name: "ctor", Server: "constructor", Tool: "constructor", Plugin: "constructor"},
		{Name: "then_remote", Server: "then-server", Tool: "then", Plugin: "then"},
	}}
	var calls []string
	h.call = func(_ context.Context, name string, _ json.RawMessage) (codemode.Reply, error) {
		calls = append(calls, name)
		return codemode.Reply{}, nil
	}
	code := `await tools.native.__proto__({});await tools.native.constructor({});await tools.native.then({});await tools.mcp.__proto__.__proto__({});await tools.plugins.constructor.constructor.constructor({});await globalThis['then-server'].then({});await tools.plugins.then['then-server'].then({});let rejected=0;for(const ns of [tools.native,tools.mcp['then-server']]){try{await ns;}catch(e){if(!e.message.includes('namespaces cannot be awaited'))throw e;rejected++;}}return rejected;`
	out, err := (&Executor{}).Execute(t.Context(), fixed(code), codemode.Limits{}, h)
	if err != nil || string(out.Value) != "2" {
		t.Fatalf("%s %v", out.Value, err)
	}
	if strings.Join(calls, ",") != "__proto__,constructor,then,proto,ctor,then_remote,then_remote" {
		t.Fatal(calls)
	}
}

func TestNamedCallsShareValidationErrorsMediaAndBudgets(t *testing.T) {
	t.Parallel()
	h := &namedTestHost{names: []codemode.ToolRoute{{Name: "Read"}}}
	calls := 0
	h.call = func(_ context.Context, _ string, args json.RawMessage) (codemode.Reply, error) {
		calls++
		if string(args) == `{"fail":true}` {
			return codemode.Reply{IsError: true, Parts: []codemode.Part{{Kind: "text", Text: "fixture refusal"}, {Kind: "media", Handle: "failed-media"}}}, nil
		}
		return codemode.Reply{Parts: []codemode.Part{{Kind: "media", Handle: "original-media", MimeType: "image/png"}}}, nil
	}
	code := `const cyclic={};cyclic.self=cyclic;for(const arg of [null,[],{bad:undefined},cyclic,{bad:NaN},{bad:1n},{bad:()=>{}}]){try{await Read(arg);throw Error('accepted invalid args');}catch(e){if(e.message==='accepted invalid args')throw e;}}let failure;try{await Read({fail:true});}catch(e){if(!e.message.includes('fixture refusal'))throw e;failure=e.result.media[0];}const r=await Read({});return {selected:r.media[0],failure};`
	out, err := (&Executor{}).Execute(t.Context(), fixed(code), codemode.Limits{}, h)
	if err != nil || calls != 2 || len(out.Parts) != 2 || out.Parts[0].Handle != "original-media" || out.Parts[1].Handle != "failed-media" {
		t.Fatalf("out=%+v calls=%d err=%v", out, calls, err)
	}
	calls = 0
	_, err = (&Executor{}).Execute(t.Context(), fixed(`await Read({});await Read({});return null;`), codemode.Limits{Calls: 1}, h)
	if err == nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestToolNamesFailBeforeWorkerLaunch(t *testing.T) {
	t.Parallel()
	h := &namedTestHost{err: errors.New("catalog unavailable")}
	e := Executor{Observe: func(Metrics) { t.Error("worker launched for failed name snapshot") }}
	_, err := e.Execute(t.Context(), fixed(`return null;`), codemode.Limits{}, h)
	if err == nil || h.queries != 1 {
		t.Fatalf("queries=%d err=%v", h.queries, err)
	}
}

func TestTightReplyBudgetStillRejectsOversizedToolReply(t *testing.T) {
	t.Parallel()
	h := &namedTestHost{names: []codemode.ToolRoute{{Name: "Read"}}}
	calls := 0
	h.call = func(context.Context, string, json.RawMessage) (codemode.Reply, error) {
		calls++
		return codemode.Reply{Parts: []codemode.Part{{Kind: "text", Text: strings.Repeat("x", 2048)}}}, nil
	}
	var metrics Metrics
	e := Executor{Observe: func(m Metrics) { metrics = m }}
	_, err := e.Execute(t.Context(), fixed(`await Read({});return null;`), codemode.Limits{ReplyBytes: 1024}, h)
	if err == nil || calls != 1 || !metrics.Reaped {
		t.Fatalf("calls=%d err=%v metrics=%+v", calls, err, metrics)
	}
}

func TestLargeNameSnapshotIndependentOfReplyBudget(t *testing.T) {
	t.Parallel()
	h := &namedTestHost{names: []codemode.ToolRoute{{Name: "Read"}}}
	// Escaped JSON and multi-byte UTF-8 cross frame boundaries. The snapshot
	// exceeds even the default reply limit, without imposing a catalog count cap.
	for i := range 2000 {
		remote := fmt.Sprintf("operation_%04d_", i) + strings.Repeat("界\\\"<\n", 32)
		h.names = append(h.names, codemode.ToolRoute{Name: fmt.Sprintf("canonical_%04d", i), Server: "original-server", Tool: remote, Plugin: "original-plugin"})
	}
	raw, err := json.Marshal(h.names)
	if err != nil || len(raw) <= codemode.DefaultLimits().ReplyBytes {
		t.Fatalf("snapshot bytes=%d err=%v", len(raw), err)
	}
	t.Logf("catalog routes=%d actual escaped JSON bytes=%d frame limit=%d", len(h.names), len(raw), namesFrameBytes)
	calls := []string{}
	h.call = func(_ context.Context, name string, _ json.RawMessage) (codemode.Reply, error) {
		calls = append(calls, name)
		return codemode.Reply{}, nil
	}
	first, _ := json.Marshal(h.names[1].Tool)
	last, _ := json.Marshal(h.names[len(h.names)-1].Tool)
	code := fmt.Sprintf(`if(Object.keys(tools.mcp['original-server']).length!==2000)throw Error('incomplete catalog');await Read({});await globalThis['original-server'][%s]({});await tools.plugins['original-plugin']['original-server'][%s]({});await tools.call('canonical_1999',{});return null;`, first, last)
	for _, budget := range []int{codemode.DefaultLimits().ReplyBytes, 1024} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			calls = nil
			var metrics Metrics
			e := Executor{Observe: func(m Metrics) { metrics = m }}
			out, err := e.Execute(t.Context(), fixed(code), codemode.Limits{ReplyBytes: budget}, h)
			if err != nil || string(out.Value) != "null" || strings.Join(calls, ",") != "Read,canonical_0000,canonical_1999,canonical_1999" || !metrics.Reaped {
				t.Fatalf("out=%s calls=%v err=%v metrics=%+v", out.Value, calls, err, metrics)
			}
			out, err = e.Execute(t.Context(), fixed(`return null;`), codemode.Limits{ReplyBytes: budget}, h)
			if err != nil || string(out.Value) != "null" || !metrics.Reaped {
				t.Fatalf("null out=%s err=%v metrics=%+v", out.Value, err, metrics)
			}
		})
	}
}
