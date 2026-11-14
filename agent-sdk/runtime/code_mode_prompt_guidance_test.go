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
	guidance := codemode.Guidance
	for _, value := range []string{guidance, description} {
		for _, removed := range []string{"tools.describe", "output.text", "output.image", "batch_id", "records()", "native", "MCP", "binding", "approval", "authorization"} {
			if strings.Contains(value, removed) {
				t.Fatalf("model-facing text contains implementation detail %q", removed)
			}
		}
	}
	for _, preference := range []string{
		"Call known tools directly", "schemas from Skills", "Search for unknown tools or arguments",
		"Sequence dependent operations", "independent calls may run concurrently when appropriate",
	} {
		if !strings.Contains(guidance, preference) {
			t.Fatalf("missing usage preference %q", preference)
		}
	}
	for _, protocol := range []string{"tools.search(", "input_schema", "call_path", "tools.unpack", "error.reply", "not_found", "too_many_matches", "stale_revision", "tools.call("} {
		if strings.Contains(guidance, protocol) {
			t.Fatalf("detailed API protocol belongs in the tool description, found %q in Guidance", protocol)
		}
	}
	for _, contract := range []string{
		"async JavaScript function body", "await Read(args)", "await dtw.apps(args)", "await context7['query-docs'](args)",
		"await tools.search(selector)", "{name:string}", "{server:string,tool:string,plugin?:string}", "{query:string}",
		"{server:string,plugin?:string,after?:string}", "Exact misses return not_found", "ambiguous matches require plugin or an exact name",
		"Legacy strings try an exact name first, then capabilities", "call_path:string", "server?:string,tool?:string,plugin?:string",
		"tools.call(\"returned_name\", args)", "Tool calls resolve to {parts:[",
		"tools.unpack(reply)", "arrays {json:[],text:[],media:[]}", "original media parts", "error.reply",
		"unknown outcomes instead of replaying input", "On stale_revision, search again and revise arguments",
	} {
		if !strings.Contains(description, contract) {
			t.Fatalf("missing js_exec description contract %q", contract)
		}
	}
	direct := strings.Index(description, "await Read(args)")
	search := strings.Index(description, "await tools.search(selector)")
	result := strings.Index(description, "call_path:string")
	parts := strings.Index(description, "Tool calls resolve")
	errors := strings.Index(description, "On errors inspect")
	if direct < 0 || search <= direct || result <= search || parts <= result || errors <= parts {
		t.Fatal("description must own direct-call syntax, search results, replies, and error handling in order")
	}
	again, err := rt.codeModeCatalog(t.Context(), active.SessionRef, nil, nil)
	if err != nil || again != text {
		t.Fatal("prefix changed")
	}
}
