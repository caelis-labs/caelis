package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/agent-sdk/tool/toolset"
)

func runSearchContractResult(t *testing.T, source toolset.Source, extra []tool.Tool, code string) (*session.Event, []*session.Event) {
	t.Helper()
	store, active, _ := codeModeStore(t)
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return h1Script(code)
		}
		return nil
	}}
	tools := append([]tool.Tool(nil), extra...)
	tools = append(tools, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}})
	runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: tools, DeferredTools: source}, nil, nil)
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type != "tool_result" || event.Tool == nil || event.Tool.Name != codemode.ToolName || event.Message == nil {
			continue
		}
		return event, events
	}
	t.Fatal("contract script produced no terminal result")
	return nil, nil
}

func runSearchContract(t *testing.T, source toolset.Source, extra []tool.Tool, code string) []*session.Event {
	t.Helper()
	event, events := runSearchContractResult(t, source, extra, code)
	if event.Meta[codemode.MetadataScriptCompletion] != "completed" {
		t.Fatalf("contract script did not complete: %+v", event.Tool.Output)
	}
	for _, part := range event.Message.Parts {
		if part.ToolResult == nil {
			continue
		}
		for _, content := range part.ToolResult.Content {
			if content.JSON == nil {
				continue
			}
			var result struct {
				Contract bool `json:"contract"`
			}
			if json.Unmarshal(content.JSON.Value, &result) == nil && result.Contract {
				return events
			}
		}
	}
	t.Fatalf("contract script lost its final result: %+v", event.Tool.Output)
	return nil
}

func TestCodeModeSearchContractCallPathsPreserveOriginalNames(t *testing.T) {
	const canonical = "canonical__read\"quoted\\path"
	const server = "server\"with-punctuation."
	const remote = "read'quoted\"tool-name"
	const plugin = "plugin'quoted\"name"
	def := fixtureMCPDefinition("search-contract-v1")
	def.Name = canonical
	def.Metadata[mcp.MetadataServer] = server
	def.Metadata[mcp.MetadataTool] = remote
	def.Metadata[mcp.MetadataPluginID] = plugin
	def.InputSchema = map[string]any{"type": "object", "required": []string{"value"}, "properties": map[string]any{"value": map[string]any{"type": "integer"}}}
	var effects []string
	invoke := func(ctx context.Context, call tool.Call) (tool.Result, error) {
		effects = append(effects, call.Name)
		if parent := tool.ParentFromContext(ctx); parent == nil || parent.Kind != codemode.ToolKind {
			t.Errorf("call path lost Runtime parent: %+v", call)
		}
		if binding := toolbinding.Target(ctx); call.Name == canonical && (binding == nil || *binding != mcp.BindingForDefinition(def)) {
			t.Errorf("call path lost exact binding: %+v", call)
		}
		return tool.Result{
			Content: []tool.ContentBlock{tool.StructuredContent{Value: call.Input}},
		}, nil
	}
	source := &runtimeDeferredSource{}
	source.publish(tool.NamedTool{Def: def, Invoke: invoke})
	date := tool.NamedTool{Def: tool.Definition{Name: "Date", InputSchema: def.InputSchema}, Invoke: invoke}
	metadata, _ := json.Marshal(map[string]string{"name": canonical, "server": server, "tool": remote, "plugin": plugin})
	code := fmt.Sprintf(`
const expected=%s;
const page=await tools.search();
if(!page.tools.some(t=>t.name===expected.name)||!page.tools.some(t=>t.name==="Date")||page.next!==null)throw Error("directory omitted eligible tools");
if(typeof Date.now!=='function')throw Error('Date global overwritten');
const date=await tools.search({name:'Date'});
if(date.name!=='Date'||'server' in date||'tool' in date||'plugin' in date)throw Error('Date metadata');
if(date.call_path!=='tools.call("Date", args)')throw Error('intrinsic requires fallback');
let args={value:1};
const first=await eval(date.call_path);
if(first.data.value!==1)throw Error('Date template result');
const entry=await tools.search({name:expected.name});
for(const key of Object.keys(expected))if(entry[key]!==expected[key])throw Error('metadata fidelity '+key);
if(entry.input_schema.required[0]!=='value')throw Error('incomplete schema');
if(typeof tools.plugins[expected.plugin][expected.server][expected.tool]!=='function')throw Error('original object names lost');
args={value:2};
const second=await eval(entry.call_path);
if(second.data.value!==2)throw Error('quoted template result');
if(typeof Date.now!=='function')throw Error('Date global changed');
return {contract:true};`, metadata)
	runSearchContract(t, source, []tool.Tool{date}, code)
	if !reflect.DeepEqual(effects, []string{"Date", canonical}) {
		t.Fatalf("templates executed wrong canonical targets: %q", effects)
	}
}

func TestCodeModeSearchContractNewlyReadyToolUsesSameVMCallPath(t *testing.T) {
	source := &runtimeDeferredSource{}
	def := fixtureMCPDefinition("search-contract-late-v1")
	def.Name = "late__read"
	def.Metadata[mcp.MetadataServer] = "late-server"
	effects := 0
	late := tool.NamedTool{Def: def, Invoke: func(ctx context.Context, call tool.Call) (tool.Result, error) {
		effects++
		if binding := toolbinding.Target(ctx); call.Name != def.Name || binding == nil || *binding != mcp.BindingForDefinition(def) {
			t.Errorf("newly ready call lost identity: %+v", call)
		}
		raw := json.RawMessage(`{"ready":true}`)
		return tool.Result{
			Content: []tool.ContentBlock{tool.StructuredContent{Value: raw}},
		}, nil
	}}
	publications := 0
	publish := tool.NamedTool{Def: tool.Definition{Name: "PublishReadyTool", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		publications++
		source.publish(late)
		return tool.Result{}, nil
	}}
	runSearchContract(t, source, []tool.Tool{publish}, `
if('late-server' in tools.mcp)throw Error('tool was ready before VM snapshot');
await PublishReadyTool({});
const page=await tools.search({query:'LATE read'});
if(page.tools.length!==1||page.next!==null||page.tools[0].server!=='late-server'||'input_schema' in page.tools[0])throw Error('unexpected directory');
const found=await tools.search({name:page.tools[0].name});
if(found.call_path!=='tools.call("late__read", args)')throw Error('late tool requires fallback');
if('late-server' in tools.mcp)throw Error('search mutated frozen namespace');
const args={};
const reply=await eval(found.call_path);
if(reply.data.ready!==true)throw Error('new tool template failed');
if('late-server' in tools.mcp)throw Error('call mutated frozen namespace');
return {contract:true};`)
	if publications != 1 || effects != 1 {
		t.Fatalf("same-VM publication=%d effects=%d, want one each", publications, effects)
	}
}

func TestCodeModeSearchContractPagination(t *testing.T) {
	source := &runtimeDeferredSource{}
	for index := 0; index < 20; index++ {
		def := fixtureMCPDefinition("v1")
		def.Name = fmt.Sprintf("docs__%02d", index)
		source.ready = append(source.ready, tool.NamedTool{Def: def})
	}
	runSearchContract(t, source, nil, `
 const page=await tools.search({query:"docs"});
 if(page.tools.length!==16||page.next!==page.tools[15].name)throw Error("bad first page");
 const tail=await tools.search({query:"docs",after:page.next});
 if(tail.tools.length!==4||tail.next!==null||tail.tools[0].name!== "docs__16")throw Error("bad tail");
 const empty=await tools.search({query:"no_such_keyword"});
 return {contract:empty.tools.length===0&&empty.next===null};`)
}

func TestCodeModeExactNameDoesNotFallBackToFilter(t *testing.T) {
	source := &runtimeDeferredSource{}
	def := fixtureMCPDefinition("v1")
	source.publish(tool.NamedTool{Def: def})
	event, _ := runSearchContractResult(t, source, nil, `return await tools.search({name:"docs"});`)
	if event.Tool.Status != "failed" || !strings.Contains(fmt.Sprint(event.Tool.Output), "not_found") {
		t.Fatalf("exact miss = %+v", event.Tool.Output)
	}
}

func TestCodeModeSearchRanksServicesAndMethodsAndBatchesExactSchemas(t *testing.T) {
	source := &runtimeDeferredSource{}
	makeRemote := func(name, server, method, description string) tool.NamedTool {
		def := fixtureMCPDefinition("rank-v1")
		def.Name, def.Description = name, description
		def.Metadata[mcp.MetadataServer], def.Metadata[mcp.MetadataTool] = server, method
		def.InputSchema = map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "integer"}}}
		return tool.NamedTool{Def: def}
	}
	target := makeRemote("z__target", "RecordService", "getRecord", "Exact method.")
	source.ready = append(source.ready, target)
	for i := 0; i < 17; i++ {
		name := fmt.Sprintf("a__%02d", i)
		source.ready = append(source.ready, makeRemote(name, "a", name, "Get a record from this service. "+strings.Repeat("中文", 90)))
	}
	source.ready = append(source.ready, makeRemote("not_a_match", "elsewhere", "getOnly", "Get from elsewhere."))
	huge := makeRemote("huge__describe", "huge", "describe", "Oversized exact schema.")
	huge.Def.InputSchema["description"] = strings.Repeat("oversized schema ", 10000)
	source.ready = append(source.ready, huge)
	native := tool.NamedTool{Def: tool.Definition{Name: "GetRecord", InputSchema: target.Def.InputSchema}}
	events := runSearchContract(t, source, []tool.Tool{native}, `
const query="record service get record";
const start=await tools.search({query});
if(start.tools.length!==16 || start.tools[0].name!=="z__target" || start.tools[0].call_path!=="RecordService.getRecord(args)")throw Error("service/method ranking: "+JSON.stringify(start));
const tail=await tools.search({query,after:start.next});
const joined=[...start.tools,...tail.tools];
if(joined.length!==18 || new Set(joined.map(t=>t.name)).size!==18 || tail.next!==null)throw Error("ranked pagination lost results");
if(joined.some(t=>"input_schema" in t || Array.from(t.description).length>161))throw Error("directory was not lightweight");
const native=await tools.search({query:"get record"});
if(native.tools[0].name!=="GetRecord" || native.tools[0].call_path!=="GetRecord(args)")throw Error("prefix missing direct syntax");
const batch=await tools.search({names:["unknown","GetRecord","huge__describe","z__target"]});
const [missing, prefix, huge, remote]=batch.tools;
if(missing.name!=="unknown"||missing.error_code!=="not_found"||huge.error_code!=="resource_exhausted")throw Error("batch status mismatch");
if(prefix.entry.input_schema.properties.value.type!=="integer"||remote.entry.input_schema.properties.value.type!=="integer")throw Error("missing exact schema");
if(prefix.entry.call_path!=="GetRecord(args)"||remote.entry.output_format!=="body")throw Error("schema contract mismatch");
return {contract:true};`)
	bindings := codeModeBindings(events)
	if len(bindings) != 1 || bindings[target.Def.Name] != mcp.BindingForDefinition(target.Def) {
		t.Fatalf("batch bound anything beyond the delivered MCP schema: %v", bindings)
	}
}
