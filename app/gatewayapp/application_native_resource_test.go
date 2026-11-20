package gatewayapp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

// TestApplicationNativeResourceStoreRoundTrip checks the same Store/bridge
// combination used by the HTTP Host without requiring a native Seatbelt launch.
func TestApplicationNativeResourceStoreRoundTrip(t *testing.T) {
	workspace := t.TempDir()
	store, err := sessionprofile.Open(filepath.Join(t.TempDir(), "application.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	connection := struct{ Scope sessionprofile.Scope }{sessionprofile.PrincipalScope("owner")}
	binding := sessionprofile.Binding{Scope: connection.Scope, SessionID: "session-native", Profile: sessionprofile.Profile{Version: "native/1", Model: "model", ToolsVersion: "native/1", Execution: "workspace-write"}, CreationDigest: "create"}
	if err := store.PutBinding(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	data := []byte("input-resource-bytes\n")
	resource, err := store.CreateResource(t.Context(), binding.Scope, binding.SessionID, "upload", "input.txt", "text/plain", data)
	if err != nil {
		t.Fatal(err)
	}
	bridge := applicationResourceBridge{store: store, binding: binding, workspace: workspace}
	input, _ := json.Marshal(map[string]string{"resource_id": resource.ID})
	read, err := bridge.read(t.Context(), input, tool.InvocationContext{SessionID: binding.SessionID, TurnID: "turn", ItemID: "step-read"})
	if err != nil {
		t.Fatal(err)
	}
	var delivery struct {
		Path string `json:"path"`
	}
	deliveryJSON, ok := read.Structured()
	if !ok {
		t.Fatalf("resource delivery returned no structured result: %+v", read)
	}
	if err := json.Unmarshal(deliveryJSON, &delivery); err != nil {
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
	published, err := bridge.publish(t.Context(), input, tool.InvocationContext{SessionID: binding.SessionID, TurnID: "turn", ItemID: "step-publish"})
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Resource sessionprofile.Resource `json:"resource"`
	}
	outputJSON, ok := published.Structured()
	if !ok {
		t.Fatalf("resource publication returned no structured result: %+v", published)
	}
	if err := json.Unmarshal(outputJSON, &output); err != nil {
		t.Fatal(err)
	}
	_, retained, err := store.ReadResource(t.Context(), binding.Scope, binding.SessionID, output.Resource.ID)
	if err != nil || !bytes.Equal(retained, data) {
		t.Fatalf("published Store bytes = %q, %v", retained, err)
	}
}
