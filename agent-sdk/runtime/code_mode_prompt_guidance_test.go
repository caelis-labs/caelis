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
	definition := (&codemode.Tool{Name: codemode.ToolName}).Definition()
	description := definition.Description
	guidance := codemode.Guidance
	for _, value := range []string{guidance, description} {
		for _, removed := range []string{"tools.describe", "output.text", "output.image", "batch_id", "records()", "native", "MCP", "approval", "authorization", "parallel_safe"} {
			if strings.Contains(value, removed) {
				t.Fatalf("model-facing text contains implementation detail %q", removed)
			}
		}
	}
	for _, preference := range []string{
		"Call known tools directly", "schemas from Skills", "Search for unknown tools or arguments",
		"Await dependent operations", "express independent work concurrently", "Runtime schedules execution",
		"Submit js_exec scripts sequentially", "observe the original Task to completion before the next script, or use reset:true to abort the old script",
		"Task controls work independently of the VM",
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
		"Top-level await", "Use return for the final output", "omitted return is null",
		"Use block-local let/const", "Top-level bindings usually persist across script calls, but may be cleared",
		"Assign to let, not const", "Redeclare only if both declarations are var/function",
		"{ const body = await Read(args); return body.data.content; }", "await RunCommand(args)", "await dtw.apps(args)", "await context7['query-docs'](args)",
		"await tools.search() returns a plain page", "{query?,after?}", "await tools.search({name}) returns one exact object and records its binding",
		"input_schema", `output_format:"body"`, "output_schema describes the tool call's .data", "call_path",
		"result.data contains only explicitly structured output", "validated against the declared schema", "missing data is undefined, explicit JSON null stays null",
		"result.text joins only text blocks", "Return the original Body for the whole reply with raw JSON tokens", "original result.media references", "omit undefined",
		"Only failed Bodies retain error.result.text()", "error.result.json() (parses that text, not structured output)",
		"Return error.result for the original reply", "original error.result.media references",
		"runtime.state()", "reset:true", "error.message, error.code and error.result",
		"stale_revision, refresh the exact schema and rebuild args", "Check child outcomes before retrying", "never replay unknown outcomes",
	} {
		if !strings.Contains(description, contract) {
			t.Fatalf("missing js_exec contract %q", contract)
		}
	}
	successAPI := strings.SplitN(description, "Inspect error.message", 2)[0]
	for _, reader := range []string{".json()", ".text()"} {
		if strings.Contains(successAPI, reader) {
			t.Fatalf("diagnostic reader advertised as part of the success API: %s", reader)
		}
	}
	for _, legacy := range []string{"tools.unpack", "{parts:", "tools.describe", "output.text", "output.image"} {
		if strings.Contains(description, legacy) {
			t.Fatalf("legacy workflow still advertised: %s", legacy)
		}
	}
	for _, detail := range []string{"console.", "host Session", "resume", "release"} {
		if strings.Contains(description, detail) {
			t.Fatalf("js_exec description exposes unnecessary detail %q", detail)
		}
	}
	if strings.Index(description, "await Read(args)") > strings.Index(description, "tools.search(") {
		t.Fatal("direct entry must precede discovery")
	}
	properties := definition.InputSchema["properties"].(map[string]any)
	for name, contract := range map[string]string{
		"reset":         "without undoing its effects",
		"yield_time_ms": "not an execution timeout. After yield, use Task outside the VM with the original script handle, not a child command handle.",
	} {
		property := properties[name].(map[string]any)
		if !strings.Contains(property["description"].(string), contract) {
			t.Fatalf("missing js_exec %s contract %q", name, contract)
		}
	}

	again, err := rt.codeModeCatalog(t.Context(), active.SessionRef, nil, nil)
	if err != nil || again != text {
		t.Fatal("prefix changed")
	}
}
