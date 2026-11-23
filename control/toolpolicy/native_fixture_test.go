package toolpolicy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/approval"

	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/control/plugin"
)

// This opt-in acceptance uses two task-owned native fixture apps. Approvals are
// deterministic test responses, not evidence of a human or Guardian decision.
func TestProductPolicyNativeFixtureAutomatedApproval(t *testing.T) {
	root := os.Getenv("CAELIS_POLICY_DTW_FIXTURE_ROOT")
	if root == "" || runtime.GOOS != "darwin" {
		t.Skip("requires task-owned macOS fixture bundles and isolated output")
	}
	a := filepath.Join(root, "PolicyFixtureA.app")
	b := filepath.Join(root, "PolicyFixtureB.app")
	for _, path := range []string{a, b} {
		if _, err := os.Stat(filepath.Join(path, "Contents", "Info.plist")); err != nil {
			t.Fatal(err)
		}
	}
	contribution, err := plugin.ProductContributions(plugin.RuntimePaths{ProductStoreDir: filepath.Join(root, "packages"), StoreDir: filepath.Join(root, "plugin-session")})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := mcp.NewManager(t.Context(), contribution.MCPServerSpecs, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	<-manager.Initialized()
	if len(manager.Tools()) != 15 {
		t.Fatalf("atomic catalog size %d", len(manager.Tools()))
	}
	store := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "fixture", UserID: "fixture", Workspace: session.WorkspaceRef{Key: "fixture", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	var approved, denied atomic.Int32
	requester := requesterFunc(func(_ context.Context, req approval.Request) (approval.Response, error) {
		if req.Authorization == nil {
			t.Error("missing typed authorization")
			return approval.Response{}, nil
		}
		if req.Authorization.Scope.Resource == b {
			denied.Add(1)
			return approval.Response{Outcome: "selected", OptionID: "cancel"}, nil
		}
		if req.Authorization.Scope.Resource != a {
			t.Errorf("unexpected resource %q", req.Authorization.Scope.Resource)
		}
		approved.Add(1)
		return approval.Response{Outcome: "selected", OptionID: "allow_session", Approved: true}, nil
	})
	names := map[string]string{}
	for _, item := range manager.Tools() {
		def := item.Definition()
		remote, _ := def.Metadata[mcp.MetadataTool].(string)
		names[remote] = def.Name
		t.Logf("catalog %s -> %s", remote, def.Name)
	}
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	code := `await tools.dtw__look({app:` + string(aj) + `});await tools.dtw__setValue({app:` + string(aj) + `,selector:{role:"text_field",text:"Fixture Text"},value:"CAELIS-240-OWNED"});await tools.dtw__keypress({app:` + string(aj) + `,keys:["META","S"]});return await tools.dtw__look({app:` + string(bj) + `});`
	for _, remote := range []string{"look", "setValue", "keypress"} {
		name, _ := json.Marshal(names[remote])
		code = strings.ReplaceAll(code, "tools.dtw__"+remote+"(", "tools.call("+string(name)+",")
	}
	runPolicyScript(t, store, active, code, manager.Tools(), requester)
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(events, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "session-events.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(filepath.Join(root, "A", "saved.txt"))
	if err != nil || string(saved) != "CAELIS-240-OWNED" {
		t.Fatalf("native postcondition saved=%q err=%v", saved, err)
	}
	var state struct {
		Saves int `json:"saves"`
	}
	data, err = os.ReadFile(filepath.Join(root, "A", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if state.Saves != 1 || approved.Load() != 1 || denied.Load() != 1 {
		t.Fatalf("saves=%d approved=%d denied=%d", state.Saves, approved.Load(), denied.Load())
	}
	t.Logf("native fixture Session %s: same APP look/setValue/keypress; cross-APP denied; saves=1; automated approval", active.SessionID)
}
