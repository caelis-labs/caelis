package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == gojaexec.WorkerArgument {
		os.Exit(gojaexec.WorkerMain())
	}
	os.Exit(m.Run())
}

type jsExecSource struct {
	mu    sync.Mutex
	items []tool.Tool
}

func (s *jsExecSource) Tools() []tool.Tool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]tool.Tool(nil), s.items...)
}

func TestJSExecRealGojaDiscoveryAndStableTools(t *testing.T) {
	store, active, root := codeModeStore(t)
	sandbox, err := host.New(host.Config{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.Close()
	if err = os.WriteFile(filepath.Join(root, "numbers.txt"), []byte("17,25"), 0600); err != nil {
		t.Fatal(err)
	}
	read, err := filesystem.NewRead(filesystem.DefaultReadConfig(), sandbox)
	if err != nil {
		t.Fatal(err)
	}
	source := &jsExecSource{}
	def := fixtureMCPDefinition("v1")
	def.Name = "fixture__exec"
	def.InputSchema = map[string]any{"type": "object", "properties": map[string]any{"script": map[string]any{"type": "string", "enum": []string{"observe"}}}, "required": []string{"script"}, "additionalProperties": false}
	def.Metadata[tool.MetadataMCPSourceTrusted] = true
	def.Metadata[tool.MetadataMCPServer] = "desktop-world"
	def.Metadata[tool.MetadataMCPTool] = "exec"
	var bitmap bytes.Buffer
	if err := png.Encode(&bitmap, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	imagePart := model.NewMediaPart(model.MediaModalityImage, model.MediaSource{Kind: model.MediaSourceInline, Data: base64.StdEncoding.EncodeToString(bitmap.Bytes())}, "image/png", "selected.png")
	var effects int
	source.items = []tool.Tool{tool.NamedTool{Def: def, Invoke: func(_ context.Context, c tool.Call) (tool.Result, error) {
		effects++
		return tool.Result{Content: []model.Part{model.NewJSONPart(json.RawMessage(`{"value":42}`)), imagePart}}, nil
	}}}
	scripts := []string{
		`const s=await tools.search("fixture");if(s.count!==1||!s.tools[0].input_schema.properties.script)throw Error("incomplete search schema");return s;`,
		`return await tools.call("fixture__exec",{script:"observe"});`,
		`const a=await tools.call("Read",{path:"numbers.txt"});const b=await tools.call("Read",{path:"numbers.txt"});if(a.parts[0].json.content!=="1: 17,25"||b.parts[0].json.content!=="1: 17,25")throw Error("Read content shape");return [a,b];`,
	}
	driver := &mcpScriptModel{steps: func(i int, req *model.Request) []model.ToolCall {
		if i >= len(scripts) {
			return nil
		}
		raw, _ := json.Marshal(map[string]any{"code": scripts[i]})
		return []model.ToolCall{{ID: "provider-reused-id", Name: codemode.ToolName, Args: string(raw)}}
	}}
	events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: codeModeImageModel{driver}, Tools: []tool.Tool{read, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}, Sandbox: sandbox}, toolsearch.NewSource(source, toolsearch.NewLexicalRanker())}, DeferredTools: source, Metadata: map[string]any{codemode.MetadataSelectedTools: []string{"fixture__exec"}}}, nil, nil)
	// Provider call IDs may repeat in different model steps. Runtime's original
	// invocation item keeps script Tasks distinct without replaying any invocation.
	tasks, taskErr := sessionfile.NewTaskStore(store).ListSession(t.Context(), active.SessionRef)
	if taskErr != nil || len(tasks) != len(scripts) {
		t.Fatalf("distinct invocation Tasks=%d error=%v", len(tasks), taskErr)
	}
	if effects != 1 {
		for _, event := range events {
			if event.Journal != nil && event.Journal.ToolExecution != nil {
				r := event.Journal.ToolExecution
				if r.Status == session.ToolExecutionSucceeded || r.Error != "" {
					t.Logf("journal %s %s error=%s result=%s", r.ToolName, r.Status, r.Error, r.Result)
				}
			}
		}
		for _, event := range events {
			if event.Tool != nil && event.Type == session.EventTypeToolResult {
				t.Logf("%s %s: %+v", event.Tool.Name, event.Tool.Status, event.Tool.Output)
			}
		}
		t.Fatalf("actual effects=%d", effects)
	}
	selectedImage := false
	for _, req := range driver.requests {
		for _, msg := range req.Messages {
			for _, part := range msg.Parts {
				if part.ToolResult != nil {
					for _, content := range part.ToolResult.Content {
						if content.Media != nil && content.Media.Source.Data == imagePart.Media.Source.Data {
							selectedImage = true
						}
					}
				}
			}
		}
	}
	if !selectedImage {
		t.Fatal("directly returned media did not reach model")
	}
	var declaration, instructions []byte
	for _, req := range driver.requests {
		b, _ := json.Marshal(req.Instructions)
		if instructions == nil {
			instructions = b
		} else if !bytes.Equal(instructions, b) {
			t.Fatal("early instruction prefix mutated after discovery")
		}
		raw, _ := json.Marshal(req.Tools)
		if declaration == nil {
			declaration = raw
		} else if !bytes.Equal(declaration, raw) {
			t.Fatal("model tools mutated after discovery")
		}
		for _, def := range req.Tools {
			if def.Function != nil && (def.Function.Name == tool.ToolSearchToolName || def.Function.Name == tool.MCPCallToolName || def.Function.Name == "fixture__exec") {
				t.Fatalf("dynamic entry exposed: %s", def.Function.Name)
			}
		}
	}
	if dir := os.Getenv("CAELIS_CODE_MODE_OFFLINE_ARTIFACTS"); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string][]byte{"sdk.instructions.json": instructions, "sdk.tools.json": declaration} {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	state, _ := store.SnapshotState(t.Context(), active.SessionRef)
	hint, _ := state[codeModeCatalogKey].(string)
	if !strings.Contains(hint, "fixture__exec") || strings.Contains(hint, `"input_schema":`) {
		t.Fatalf("selected name/description hints missing or schema leaked: %q", hint)
	}
	source.mu.Lock()
	next := tool.CloneDefinition(def)
	next.Description = "refreshed description"
	source.items = []tool.Tool{tool.NamedTool{Def: next}}
	source.mu.Unlock()
	restarted, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := restarted.codeModeCatalog(t.Context(), active.SessionRef, source, nil)
	if err != nil || frozen != hint {
		t.Fatal("catalog refresh/restart rewrote the selected early prefix")
	}
	results := 0
	for _, event := range events {
		if event.Tool != nil && event.Type == session.EventTypeToolResult && event.Tool.Name == codemode.ToolName {
			results++
			if event.Tool.Status != "completed" {
				t.Fatalf("JS failed: %+v", event.Tool.Output)
			}
		}
	}
	if results != len(scripts) {
		t.Fatalf("results=%d", results)
	}
}

func TestJSExecBlocksNativeProcessAliasesAndKeepsErrors(t *testing.T) {
	store, active, root := codeModeStore(t)
	sandbox, err := host.New(host.Config{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.Close()
	command, err := shell.NewRunCommand(shell.RunCommandConfig{Runtime: sandbox})
	if err != nil {
		t.Fatal(err)
	}
	alias := command.Definition()
	alias.Name = "harmless_alias"
	var effects int
	disguised := tool.NamedTool{Def: alias, Invoke: func(context.Context, tool.Call) (tool.Result, error) { effects++; return tool.Result{}, nil }}
	code := `try {await tools.call("harmless_alias",{command:"echo prohibited"});}catch(e){};return {claimed:"success"};`
	raw, _ := json.Marshal(map[string]any{"code": code})
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "denied", Name: codemode.ToolName, Args: string(raw)}}
		}
		return nil
	}}
	events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{disguised, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}, Sandbox: sandbox}}}, nil, nil)
	if effects != 0 {
		t.Fatal("process alias executed")
	}
	failed := false
	for _, e := range events {
		if e.Tool != nil && e.Tool.ID == "denied" && e.Type == session.EventTypeToolResult {
			failed = e.Tool.Status == "failed"
		}
	}
	if !failed {
		t.Fatal("caught bridge error was presented as success")
	}
}

func jsExecTaskHandle(request *model.Request) string {
	for _, message := range request.Messages {
		for _, part := range message.Parts {
			if part.ToolResult == nil || part.ToolResult.Name != codemode.ToolName {
				continue
			}
			for _, content := range part.ToolResult.Content {
				if content.JSON == nil {
					continue
				}
				var value map[string]any
				if json.Unmarshal(content.JSON.Value, &value) == nil {
					if handle, ok := value["handle"].(string); ok {
						return handle
					}
				}
			}
		}
	}
	return ""
}

func TestJSExecYieldReturnsControlAndContinuesOriginalTask(t *testing.T) {
	store, active, root := codeModeStore(t)
	sandbox, err := host.New(host.Config{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.Close()
	started, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var order []string
	hold := tool.NamedTool{Def: tool.Definition{Name: "Hold", InputSchema: map[string]any{"type": "object"}}, Invoke: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
		close(started)
		select {
		case <-release:
			return tool.Result{}, nil
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		}
	}}
	independent := tool.NamedTool{Def: tool.Definition{Name: "Independent", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		mu.Lock()
		order = append(order, "model-independent")
		mu.Unlock()
		close(release)
		return tool.Result{}, nil
	}}
	after := tool.NamedTool{Def: tool.Definition{Name: "After", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		mu.Lock()
		order = append(order, "script-after-yield")
		mu.Unlock()
		return tool.Result{}, nil
	}}
	var handle string
	driver := &mcpScriptModel{steps: func(i int, request *model.Request) []model.ToolCall {
		switch i {
		case 0:
			return []model.ToolCall{{ID: "async-script", Name: codemode.ToolName, Args: `{"code":"await tools.call('Hold',{});await tools.call('After',{});return {done:true};","yield_time_ms":0}`}}
		case 1:
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Error("script did not reach first Host call")
			}
			handle = jsExecTaskHandle(request)
			if handle == "" {
				t.Error("yield returned no original Task handle")
			}
			return []model.ToolCall{{ID: "independent", Name: "Independent", Args: `{}`}}
		case 2:
			raw, _ := json.Marshal(map[string]any{"action": "wait", "handle": handle})
			return []model.ToolCall{{ID: "observe-original", Name: "Task", Args: string(raw)}}
		}
		return nil
	}}
	executor := &gojaexec.Executor{}
	events, rt := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{hold, independent, after, tasktool.New(), &codemode.Tool{Name: codemode.ToolName, Executor: executor, Sandbox: sandbox}}}, nil, nil)
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(order, []string{"model-independent", "script-after-yield"}) {
		t.Fatalf("not a real yield: %v", order)
	}
	entries, err := rt.tasks.store.ListSession(t.Context(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Handle != handle || entries[0].Kind != taskapi.KindCodeMode || entries[0].State != taskapi.StateCompleted {
		t.Fatalf("not the original completed Task: %+v", entries)
	}
	observed := false
	for _, e := range events {
		if e.Tool != nil && e.Tool.ID == "observe-original" && e.Type == session.EventTypeToolResult {
			observed = e.Tool.Status == "completed"
		}
	}
	if !observed {
		t.Fatal("original Task query did not complete")
	}
	cursor, _ := taskInt64Value(entries[0].Metadata["model_output_cursor"])
	if cursor <= 0 || taskStringValue(entries[0].Result["tool_result_json"]) == "" {
		t.Fatal("canonical delivery lost the original result/frontier")
	}
	restarted, err := New(Config{Sessions: store, TaskStore: rt.tasks.store, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := restarted.tasks.Read(t.Context(), active.SessionRef, taskapi.ControlRequest{TaskID: entries[0].TaskID, Principal: session.ActorKindTool, Source: "agent_tool"})
	if err != nil || snapshot.Result["tool_result_json"] != nil {
		t.Fatalf("restart repeated already-delivered selected output: %+v %v", snapshot, err)
	}

}

func TestJSExecFinalBoundaryCancelsBeforeJoinAndPreservesUnknown(t *testing.T) {
	for _, trustedComplete := range []bool{false, true} {
		t.Run(fmt.Sprint(trustedComplete), func(t *testing.T) {
			store, active, root := codeModeStore(t)
			sandbox, err := host.New(host.Config{CWD: root})
			if err != nil {
				t.Fatal(err)
			}
			defer sandbox.Close()
			started := make(chan struct{})
			var after atomic.Int32
			hold := tool.NamedTool{Def: tool.Definition{Name: "Hold", InputSchema: map[string]any{"type": "object"}}, Invoke: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
				close(started)
				<-ctx.Done()
				return tool.Result{}, ctx.Err()
			}}
			later := tool.NamedTool{Def: tool.Definition{Name: "After", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { after.Add(1); return tool.Result{}, nil }}
			complete := tool.NamedTool{Def: tool.Definition{Name: "Complete", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { return tool.Result{TurnComplete: true}, nil }}
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return []model.ToolCall{{ID: "async-script", Name: codemode.ToolName, Args: `{"code":"await tools.call('Hold',{});await tools.call('After',{});return {};","yield_time_ms":0}`}}
				}
				if i == 1 {
					select {
					case <-started:
					case <-time.After(3 * time.Second):
						t.Error("Host call did not start")
					}
					if trustedComplete {
						return []model.ToolCall{{ID: "end", Name: "Complete", Args: `{}`}}
					}
				}
				return nil
			}}
			_, rt := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{hold, later, complete, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}, Sandbox: sandbox}}}, nil, nil)
			if after.Load() != 0 {
				t.Fatal("new Host call dispatched after final boundary")
			}
			entries, _ := rt.tasks.store.ListSession(t.Context(), active.SessionRef)
			if len(entries) != 1 || entries[0].Running || entries[0].State != taskapi.StateUnknownOutcome {
				t.Fatalf("in-flight unknown lost: %+v", entries)
			}
		})
	}
}

func TestJSExecStaleSchemaRejectsOldArgumentsBeforeEffect(t *testing.T) {
	store, active, root := codeModeStore(t)
	sandbox, err := host.New(host.Config{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.Close()
	source := &jsExecSource{}
	old := fixtureMCPDefinition("v1")
	old.Name = "fixture__changing"
	var effects atomic.Int32
	invoke := func(context.Context, tool.Call) (tool.Result, error) { effects.Add(1); return tool.Result{}, nil }
	source.items = []tool.Tool{tool.NamedTool{Def: old, Invoke: invoke}}
	driver := &mcpScriptModel{steps: func(i int, r *model.Request) []model.ToolCall {
		switch i {
		case 0:
			return []model.ToolCall{{ID: "describe", Name: codemode.ToolName, Args: `{"code":"return await tools.search('fixture__changing');"}`}}
		case 1:
			next := fixtureMCPDefinition("v2")
			next.Name = old.Name
			next.InputSchema = map[string]any{"type": "object", "properties": map[string]any{"new": map[string]any{"type": "integer"}}, "required": []string{"new"}}
			source.mu.Lock()
			source.items = []tool.Tool{tool.NamedTool{Def: next, Invoke: invoke}}
			source.mu.Unlock()
			return []model.ToolCall{{ID: "stale", Name: codemode.ToolName, Args: `{"code":"try {await tools.call('fixture__changing',{script:'old'});}catch(e){return {caught:e.message};}"}`}}
		}
		return nil
	}}
	events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{toolsearch.NewSource(source, toolsearch.NewLexicalRanker()), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}, Sandbox: sandbox}}, DeferredTools: source}, nil, nil)
	if effects.Load() != 0 {
		t.Fatal("old arguments reached a refreshed tool")
	}
	checked := false
	for _, e := range events {
		if e.Tool != nil && e.Tool.ID == "stale" && e.Type == session.EventTypeToolResult {
			raw, _ := json.Marshal(e.Tool.Output)
			if e.Tool.Status != "failed" || !bytes.Contains(raw, []byte("refreshed")) || !bytes.Contains(raw, []byte("tools.search(name)")) {
				t.Fatalf("stale call lacked actionable error: %s", raw)
			}
			checked = true
		}
	}
	if !checked {
		t.Fatal("missing original failed result")
	}
}

func TestJSExecLeavesNativeRunCommandUsable(t *testing.T) {
	store, active, root := codeModeStore(t)
	sandbox, err := host.New(host.Config{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.Close()
	command, err := shell.NewRunCommand(shell.RunCommandConfig{Runtime: sandbox})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "native-effect.txt")
	args, _ := json.Marshal(map[string]any{"command": shellWriteFileForTest(target, "native"), "yield_time_ms": 1000})
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "native", Name: shell.RunCommandToolName, Args: string(args)}}
		}
		return nil
	}}
	runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{command, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}, Sandbox: sandbox}}}, nil, nil)
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "native" {
		t.Fatalf("native shell regressed: %q %v", data, err)
	}
}

func TestJSExecCannotWriteNativeCommandTask(t *testing.T) {
	store, active, _ := codeModeStore(t)
	tasks := sessionfile.NewTaskStore(store)
	_, err := tasks.Put(t.Context(), taskapi.PutRequest{Entry: &taskapi.Entry{TaskID: "native-command-receipt", Handle: "innocent-report", Session: active.SessionRef, Kind: taskapi.KindCommand, State: taskapi.StateCompleted, CreatedAt: time.Now(), UpdatedAt: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "script-stdin", Name: codemode.ToolName, Args: `{"code":"try{await tools.call('Task',{action:'write',handle:'innocent-report',input:'echo prohibited'});}catch(e){};return {claimed:'success'};"}`}}
		}
		return nil
	}}
	events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{tasktool.New(), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}, nil, nil)
	rejected := false
	for _, e := range events {
		if e.Journal != nil && e.Journal.ToolExecution != nil {
			r := e.Journal.ToolExecution
			if r.ToolName == codemode.ToolName && r.Status == session.ToolExecutionFailed && strings.Contains(r.Error, "native command stdin is prohibited") {
				rejected = true
			}
		}
	}
	if !rejected {
		for _, e := range events {
			if e.Journal != nil && e.Journal.ToolExecution != nil {
				r := e.Journal.ToolExecution
				if r.Error != "" {
					t.Logf("%s %s %s", r.ToolName, r.Status, r.Error)
				}
			}
		}
		t.Fatal("actual stored command identity was not rejected before Task.write")
	}
}
