package runtime

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

func TestJSExecCatalogSelectionIsEmbedderOwnedAndBounded(t *testing.T) {
	store, active, _ := codeModeStore(t)
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	def := fixtureMCPDefinition("v1")
	def.Name = "custom__entry"
	def.Metadata[tool.MetadataMCPTool] = "query-docs"
	def.Description = strings.Repeat("界", 70)
	candidates := []tool.Tool{tool.NamedTool{Def: def}}
	text, err := rt.codeModeCatalog(t.Context(), active.SessionRef, &jsExecSource{items: candidates}, nil)
	if err != nil || strings.Contains(text, def.Name) {
		t.Fatalf("SDK imposed product selection: %s %v", text, err)
	}
	selected := map[string]any{codemode.MetadataSelectedTools: []any{def.Name}}
	again, err := rt.codeModeCatalog(t.Context(), active.SessionRef, &jsExecSource{items: candidates}, selected)
	if err != nil || again != text {
		t.Fatalf("late selection rewrote early prefix: %v", err)
	}
	second, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: active.AppName, UserID: active.UserID})
	if err != nil {
		t.Fatal(err)
	}
	text, err = rt.codeModeCatalog(t.Context(), second.SessionRef, &jsExecSource{items: candidates}, selected)
	if err != nil {
		t.Fatal(err)
	}
	var hints []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	start := strings.LastIndex(text, "\n[")
	if start < 0 || json.Unmarshal([]byte(text[start+1:]), &hints) != nil || len(hints) != 1 || hints[0].Name != def.Name || len(hints[0].Description) > 128 || !utf8.ValidString(hints[0].Description) || strings.Contains(text, "input_schema") {
		t.Fatalf("generic bounded name/description snapshot=%s", text)
	}
}
