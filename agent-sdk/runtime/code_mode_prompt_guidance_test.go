package runtime

import (
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"strings"
	"testing"
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
	for _, value := range []string{text, (&codemode.Tool{Name: codemode.ToolName}).Definition().Description} {
		for _, removed := range []string{"tools.describe", "output.text", "output.image", "batch_id", "records()", "Runtime", "approval"} {
			if strings.Contains(value, removed) {
				t.Fatalf("obsolete model guidance %q", removed)
			}
		}
		if !strings.Contains(value, "tools.search(query)") || !strings.Contains(value, "tools.call(name,args)") {
			t.Fatal("missing minimal API")
		}
	}
	for _, contract := range []string{"without search", "Read(args)", "dtw.apps(args)", "context7['query-docs'](args)", "tools.native[name]", "tools.mcp[server][tool]", "tools.plugins[plugin][server][tool]"} {
		if !strings.Contains(text, contract) {
			t.Fatalf("missing direct-call contract %q", contract)
		}
	}
	again, err := rt.codeModeCatalog(t.Context(), active.SessionRef, nil, nil)
	if err != nil || again != text {
		t.Fatal("prefix changed")
	}
}
