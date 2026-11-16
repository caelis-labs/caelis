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
	for _, contract := range []string{"async JavaScript function body", "await Read(args)", "await RunCommand(args)", "await dtw.apps(args)", "await context7['query-docs'](args)", "tools.search(", "input_schema", "output_schema", "call_path", "business objects", "original text/JSON/media", "raw_json", "omit undefined", "omitted return is null", "message, code and result", "stale_revision", "unknown outcomes"} {
		if !strings.Contains(description, contract) {
			t.Fatalf("missing js_exec contract %q", contract)
		}
	}
	for _, legacy := range []string{"tools.unpack", "{parts:", "tools.describe", "output.text", "output.image"} {
		if strings.Contains(description, legacy) {
			t.Fatalf("legacy workflow still advertised: %s", legacy)
		}
	}
	if strings.Index(description, "await Read(args)") > strings.Index(description, "tools.search(") {
		t.Fatal("direct entry must precede discovery")
	}

	again, err := rt.codeModeCatalog(t.Context(), active.SessionRef, nil, nil)
	if err != nil || again != text {
		t.Fatal("prefix changed")
	}
}
