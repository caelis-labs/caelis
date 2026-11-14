package runtime

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

func TestCodeModeMinimalModelInterface(t *testing.T) {
	store, active, _ := codeModeStore(t)
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	text, err := rt.codeModeCatalog(t.Context(), active.SessionRef, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	description := (&codemode.Tool{Name: codemode.ToolName}).Definition().Description
	for _, value := range []string{text, description} {
		for _, removed := range []string{"tools.describe", "output.text", "output.image", "batch_id", "records()", "Runtime", "approval"} {
			if strings.Contains(value, removed) {
				t.Fatalf("obsolete model guidance %q", removed)
			}
		}
	}
	for _, contract := range []string{
		"Read(args)", "dtw.apps(args)", "context7['query-docs'](args)",
		"Call known schemas directly", "Skills", "Sequence dependent operations", "concurrently when tool semantics permit",
		"original tool reply/part references", "tools.unpack(reply)", "{json:[],text:[],media:[]}", "reply.raw_json",
		"tools.search(query)", "tools.search({server,plugin?,after?})", "next as after", "same server/plugin",
		"{tools:[{name,description,input_schema}],count,truncated,omitted_count?,next?}",
		"tools.native[name](args)", "tools.mcp[server][tool](args)", "tools.plugins[plugin][server][tool](args)",
		"native commands outside js_exec", "error.reply", "error.code", "Caught failures remain recorded",
		"failed or unknown outcomes", "never blindly replay", "stale bindings with tools.search(name)",
	} {
		if !strings.Contains(text, contract) {
			t.Fatalf("missing SDK guidance contract %q", contract)
		}
	}
	direct := strings.Index(text, "Read(args)")
	output := strings.Index(text, "Return JSON")
	search := strings.Index(text, "tools.search(query)")
	compatibility := strings.Index(text, "tools.call(name,args)")
	if direct >= output || output >= search || search >= compatibility || strings.Count(text, "tools.call(") != 1 {
		t.Fatal("guidance must lead with direct calls and output, with one later compatibility note")
	}
	for _, contract := range []string{"async JavaScript function body", "code or file", "fresh VM", "native functions", "MCP object methods", "original reply/media references", "Task handle"} {
		if !strings.Contains(description, contract) {
			t.Fatalf("missing entry summary %q", contract)
		}
	}
	if strings.Contains(description, "tools.call(") {
		t.Fatal("entry summary duplicates SDK compatibility guidance")
	}
	again, err := rt.codeModeCatalog(t.Context(), active.SessionRef, nil, nil)
	if err != nil || again != text {
		t.Fatal("prefix changed")
	}
}
