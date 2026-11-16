package gatewayapp

import (
	"fmt"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestSelectedCodeModeToolsAreProductPresentationPolicy(t *testing.T) {
	makeTool := func(name, server, remote string) tool.Tool {
		return tool.NamedTool{Def: tool.Definition{Name: name, Metadata: map[string]any{tool.MetadataToolKind: tool.MetadataToolKindMCP, tool.MetadataMCPServer: server, tool.MetadataMCPTool: remote}}}
	}
	candidates := []tool.Tool{nil, makeTool("docs_alias", "context7", "query-docs"), makeTool("dtw__exec", "dtw", "exec"), makeTool("legacy_exec", "desktop-world", "exec"), makeTool("dtw_alias", "desktop_world", "exec"), makeTool("dtw__apps", "dtw", "apps"), makeTool("other_exec", "other", "exec"), makeTool("unselected", "context7", "other"), tool.NamedTool{Def: tool.Definition{Name: "native", Metadata: map[string]any{tool.MetadataMCPTool: "query-docs"}}}}
	if got := selectedCodeModeTools(candidates); !reflect.DeepEqual(got, []string{"docs_alias", "dtw__exec", "legacy_exec", "dtw_alias"}) {
		t.Fatalf("selected canonical names=%v", got)
	}
}

type codeModeHintSource struct {
	tools        []tool.Tool
	enumerations int
}

func (s *codeModeHintSource) Tools() []tool.Tool { s.enumerations++; return s.tools }

type codeModeHintTool struct {
	tool.Tool
	definitions *int
}

func (t codeModeHintTool) Definition() tool.Definition { *t.definitions++; return t.Tool.Definition() }

func TestCodeModeHintsBoundFirstSnapshotAndSkipLaterTurns(t *testing.T) {
	store := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "fixture", UserID: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	source := &codeModeHintSource{}
	definitions := 0
	for i := range 2000 {
		source.tools = append(source.tools, codeModeHintTool{Tool: tool.NamedTool{Def: tool.Definition{Name: fmt.Sprint(i), Metadata: map[string]any{tool.MetadataToolKind: tool.MetadataToolKindMCP, tool.MetadataMCPTool: "unselected"}}}, definitions: &definitions})
	}
	names, err := codeModeHintsForSession(t.Context(), store, active.SessionRef, source)
	if err != nil || len(names) != 0 || definitions != 256 || source.enumerations != 1 {
		t.Fatalf("first snapshot definitions=%d enumerations=%d names=%v err=%v", definitions, source.enumerations, names, err)
	}
	_, err = store.UpdateState(t.Context(), session.UpdateStateRequest{SessionRef: active.SessionRef, Update: func(state map[string]any) (map[string]any, error) {
		state[codemode.MetadataCatalog] = codemode.Guidance
		return state, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err = codeModeHintsForSession(t.Context(), store, active.SessionRef, source); err != nil {
			t.Fatal(err)
		}
	}
	if source.enumerations != 1 || definitions != 256 {
		t.Fatalf("later Turn traversed directory: definitions=%d enumerations=%d", definitions, source.enumerations)
	}
}
