package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

type searchContractRanker func(context.Context, string, []tool.Definition, int, toolsearch.SearchModel) ([]string, error)

func (f searchContractRanker) Rank(ctx context.Context, query string, definitions []tool.Definition, limit int, selected toolsearch.SearchModel) ([]string, error) {
	return f(ctx, query, definitions, limit, selected)
}

func runSearchContractResult(t *testing.T, source tool.Source, extra []tool.Tool, ranker toolsearch.Ranker, code string) (*session.Event, []*session.Event) {
	t.Helper()
	store, active, _ := codeModeStore(t)
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return h1Script(code)
		}
		return nil
	}}
	tools := append([]tool.Tool(nil), extra...)
	tools = append(tools, toolsearch.NewSource(source, ranker), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}})
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

func runSearchContract(t *testing.T, source tool.Source, extra []tool.Tool, ranker toolsearch.Ranker, code string) []*session.Event {
	t.Helper()
	event, events := runSearchContractResult(t, source, extra, ranker, code)
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
	def.Metadata[tool.MetadataMCPServer] = server
	def.Metadata[tool.MetadataMCPTool] = remote
	def.Metadata[tool.MetadataPluginID] = plugin
	def.Metadata[tool.MetadataMCPSourceTrusted] = true
	def.InputSchema = map[string]any{"type": "object", "required": []string{"value"}, "properties": map[string]any{"value": map[string]any{"type": "integer"}}}
	var effects []string
	invoke := func(_ context.Context, call tool.Call) (tool.Result, error) {
		effects = append(effects, call.Name)
		if call.Parent == nil || call.Parent.Kind != codemode.ToolKind {
			t.Errorf("call path lost Runtime parent: %+v", call)
		}
		if call.Name == canonical && (call.TargetBinding == nil || *call.TargetBinding != tool.BindingForDefinition(def)) {
			t.Errorf("call path lost exact binding: %+v", call)
		}
		return tool.Result{Content: []model.Part{model.NewJSONPart(call.Input)}}, nil
	}
	source := &runtimeDeferredSource{}
	source.publish(tool.NamedTool{Def: def, Invoke: invoke})
	date := tool.NamedTool{Def: tool.Definition{Name: "Date", InputSchema: def.InputSchema}, Invoke: invoke}
	metadata, _ := json.Marshal(map[string]string{"name": canonical, "server": server, "tool": remote, "plugin": plugin})
	code := fmt.Sprintf(`
const expected=%s;
if(typeof Date.now!=='function')throw Error('Date global overwritten');
const date=(await tools.search({name:'Date'})).tools[0];
if(date.name!=='Date'||'server' in date||'tool' in date||'plugin' in date)throw Error('Date metadata');
let args={value:1};
const first=await eval(date.call_path);
if(tools.unpack(first).json[0].value!==1)throw Error('Date template result');
const found=await tools.search({server:expected.server,tool:expected.tool,plugin:expected.plugin});
if(found.count!==1)throw Error('exact metadata selector');
const entry=found.tools[0];
for(const key of Object.keys(expected))if(entry[key]!==expected[key])throw Error('metadata fidelity '+key);
if(entry.input_schema.required[0]!=='value')throw Error('incomplete schema');
if(typeof tools.plugins[expected.plugin][expected.server][expected.tool]!=='function')throw Error('original object names lost');
args={value:2};
const second=await eval(entry.call_path);
if(tools.unpack(second).json[0].value!==2)throw Error('quoted template result');
if(typeof Date.now!=='function')throw Error('Date global changed');
return {contract:true};`, metadata)
	runSearchContract(t, source, []tool.Tool{date}, exactMCPRanker{}, code)
	if !reflect.DeepEqual(effects, []string{"Date", canonical}) {
		t.Fatalf("templates executed wrong canonical targets: %q", effects)
	}
}

func TestCodeModeSearchContractNewlyReadyToolUsesSameVMCallPath(t *testing.T) {
	source := &runtimeDeferredSource{}
	def := fixtureMCPDefinition("search-contract-late-v1")
	def.Name = "late__read"
	def.Metadata[tool.MetadataMCPServer] = "late-server"
	def.Metadata[tool.MetadataMCPSourceTrusted] = true
	effects := 0
	late := tool.NamedTool{Def: def, Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
		effects++
		if call.Name != def.Name || call.TargetBinding == nil || *call.TargetBinding != tool.BindingForDefinition(def) {
			t.Errorf("newly ready call lost identity: %+v", call)
		}
		return tool.Result{Content: []model.Part{model.NewJSONPart([]byte(`{"ready":true}`))}}, nil
	}}
	publications := 0
	publish := tool.NamedTool{Def: tool.Definition{Name: "PublishReadyTool", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		publications++
		source.publish(late)
		return tool.Result{}, nil
	}}
	runSearchContract(t, source, []tool.Tool{publish}, exactMCPRanker{}, `
if('late-server' in tools.mcp)throw Error('tool was ready before VM snapshot');
await PublishReadyTool({});
const found=await tools.search({server:'late-server',tool:'read'});
if(found.count!==1||found.tools[0].name!=='late__read')throw Error('new tool not discovered');
if('late-server' in tools.mcp)throw Error('search mutated frozen namespace');
const args={};
const reply=await eval(found.tools[0].call_path);
if(tools.unpack(reply).json[0].ready!==true)throw Error('new tool template failed');
if('late-server' in tools.mcp)throw Error('call mutated frozen namespace');
return {contract:true};`)
	if publications != 1 || effects != 1 {
		t.Fatalf("same-VM publication=%d effects=%d, want one each", publications, effects)
	}
}

func TestCodeModeSearchContractSelectorsKeepExactAndCapabilityModes(t *testing.T) {
	source := &h1Source{}
	for _, name := range []string{"docs__read", "capability__selected"} {
		def := fixtureMCPDefinition("search-contract-selector-v1")
		def.Name = name
		def.Metadata[tool.MetadataMCPTool] = name
		def.Metadata[tool.MetadataMCPSourceTrusted] = true
		source.items = append(source.items, tool.NamedTool{Def: def})
	}
	var ranked []string
	ranker := searchContractRanker(func(_ context.Context, query string, definitions []tool.Definition, _ int, _ toolsearch.SearchModel) ([]string, error) {
		ranked = append(ranked, query)
		if len(definitions) != 2 {
			t.Errorf("capability selector received wrong eligible source: %+v", definitions)
		}
		return []string{"capability__selected"}, nil
	})
	events := runSearchContract(t, source, nil, ranker, `
let caught=false;
try {await tools.search({name:'missing'});}catch(e) {
  if(e.code!=='not_found'||!e.reply)throw Error('exact miss lost typed failure reply');
  caught=true;
}
if(!caught)throw Error('exact miss did not reject');
const exact=await tools.search({name:'docs__read'});
if(exact.count!==1||exact.tools[0].name!=='docs__read')throw Error('exact name selected wrong target');
const capability=await tools.search({query:'docs__read'});
if(capability.count!==1||capability.tools[0].name!=='capability__selected')throw Error('explicit query used exact lookup');
const legacy=await tools.search('docs__read');
if(legacy.count!==1||legacy.tools[0].name!=='docs__read')throw Error('legacy string lost exact-first behavior');
return {contract:true};`)
	if !reflect.DeepEqual(ranked, []string{"docs__read"}) {
		t.Fatalf("exact modes invoked ranking or capability mode skipped it: %q", ranked)
	}
	failedSearches := 0
	for _, event := range events {
		if event.Journal == nil || event.Journal.ToolExecution == nil {
			continue
		}
		record := event.Journal.ToolExecution
		if record.ToolName != tool.ToolSearchToolName || record.Status != session.ToolExecutionFailed || record.Parent == nil || record.Parent.Kind != codemode.ToolKind {
			continue
		}
		var input map[string]any
		if err := json.Unmarshal(record.Input, &input); err != nil {
			t.Fatal(err)
		}
		if input["name"] != "missing" || record.Error == "" {
			t.Fatalf("caught miss lost original failed search receipt: %+v", record)
		}
		failedSearches++
	}
	if failedSearches != 1 {
		t.Fatalf("caught exact miss retained %d failed search receipts, want one", failedSearches)
	}
}

func TestCodeModeSearchContractExactMissNeverRanks(t *testing.T) {
	for _, selector := range []string{`{name:'missing'}`, `{server:'docs',tool:'missing'}`} {
		t.Run(selector, func(t *testing.T) {
			source := &runtimeDeferredSource{}
			def := fixtureMCPDefinition("search-contract-miss-v1")
			def.Description = "missing"
			source.publish(tool.NamedTool{Def: def})
			ranked := 0
			ranker := searchContractRanker(func(context.Context, string, []tool.Definition, int, toolsearch.SearchModel) ([]string, error) {
				ranked++
				return []string{def.Name}, nil
			})
			event, _ := runSearchContractResult(t, source, nil, ranker, "return await tools.search("+selector+");")
			if ranked != 0 {
				t.Fatalf("exact miss invoked capability ranking %d times", ranked)
			}
			if event.Tool.Status != "failed" || event.Meta[codemode.MetadataScriptCompletion] != "failed" {
				t.Fatalf("exact miss did not reject: status=%s output=%+v", event.Tool.Status, event.Tool.Output)
			}
		})
	}
}

func TestCodeModeSearchContractAmbiguityCanBeQualified(t *testing.T) {
	source := &h1Source{}
	for _, plugin := range []string{"one", "two"} {
		def := fixtureMCPDefinition("search-contract-ambiguous-v1")
		def.Name = plugin + "__read"
		def.Metadata[tool.MetadataPluginID] = plugin
		source.items = append(source.items, tool.NamedTool{Def: def})
	}
	runSearchContract(t, source, nil, exactMCPRanker{}, `
let caught=false;
try {await tools.search({server:'docs',tool:'read'});}catch(e) {
  if(e.code!=='too_many_matches'||!e.reply)throw Error('ambiguity lost typed failure');
  caught=true;
}
if(!caught)throw Error('ambiguous selector succeeded');
const found=await tools.search({server:'docs',tool:'read',plugin:'two'});
if(found.count!==1||found.tools[0].name!=='two__read')throw Error('qualified recovery failed');
return {contract:true};`)
}
