package gatewayapp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/application"
)

// TestApplicationNativeResourceStoreRoundTrip checks the same Store/bridge
// combination used by the HTTP Host without requiring a native Seatbelt launch.
func TestApplicationNativeResourceStoreRoundTrip(t *testing.T) {
	workspace := t.TempDir()
	store, err := application.Open(filepath.Join(t.TempDir(), "application.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	connection, err := store.Register(t.Context(), "owner", application.Registration{OperationID: "enroll", Name: "synthetic", Credential: "app-client-" + strings.Repeat("aa", 32)})
	if err != nil {
		t.Fatal(err)
	}
	binding := application.Binding{Scope: connection.Scope, SessionID: "session-native", Profile: application.Profile{Version: "native/1", Model: "model", ToolsVersion: "native/1", Execution: "workspace-write"}, CreationDigest: "create"}
	if err := store.PutBinding(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	data := []byte("input-resource-bytes\n")
	resource, err := store.CreateResource(t.Context(), binding.Scope, binding.SessionID, "upload", "input.txt", "text/plain", data)
	if err != nil {
		t.Fatal(err)
	}
	tools := newApplicationResourceTools(store, binding, workspace)
	input, _ := json.Marshal(map[string]string{"resource_id": resource.ID})
	read, err := tools[0].Call(t.Context(), tool.Call{Name: "ReadResource", Input: input, Execution: tool.InvocationContext{SessionID: binding.SessionID, TurnID: "turn", ItemID: "step-read"}})
	if err != nil {
		t.Fatal(err)
	}
	var delivery struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(read.Content[0].Text.Text), &delivery); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(workspace, delivery.Path))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("resource delivery = %q, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "result.txt"), got, 0600); err != nil {
		t.Fatal(err)
	}
	input, _ = json.Marshal(map[string]string{"path": "result.txt", "name": "result.txt", "media_type": "text/plain"})
	published, err := tools[1].Call(t.Context(), tool.Call{Name: "PublishArtifact", Input: input, Execution: tool.InvocationContext{SessionID: binding.SessionID, TurnID: "turn", ItemID: "step-publish"}})
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Resource application.Resource `json:"resource"`
	}
	if err := json.Unmarshal([]byte(published.Content[0].Text.Text), &output); err != nil {
		t.Fatal(err)
	}
	_, retained, err := store.ReadResource(t.Context(), binding.Scope, binding.SessionID, output.Resource.ID)
	if err != nil || !bytes.Equal(retained, data) {
		t.Fatalf("published Store bytes = %q, %v", retained, err)
	}
}
