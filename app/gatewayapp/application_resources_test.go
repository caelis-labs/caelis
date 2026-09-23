package gatewayapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/application"
)

type resourceBridgeStore struct {
	resources map[string][]byte
	created   []byte
	operation string
	calls     int
}

func (s *resourceBridgeStore) ReadResource(_ context.Context, _ application.Scope, session, id string) (application.Resource, []byte, error) {
	s.calls++
	data, ok := s.resources[id]
	if !ok {
		return application.Resource{}, nil, application.ErrNotFound
	}
	return testResource(id, session, data), bytes.Clone(data), nil
}

func (s *resourceBridgeStore) CreateResource(_ context.Context, _ application.Scope, session, op, name, mediaType string, data []byte) (application.Resource, error) {
	s.calls++
	s.created = bytes.Clone(data)
	s.operation = op
	resource := testResource("artifact-1", session, data)
	resource.Name = name
	resource.MediaType = mediaType
	return resource, nil
}

func testResource(id, session string, data []byte) application.Resource {
	sum := sha256.Sum256(data)
	return application.Resource{ID: id, SessionID: session, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
}

func testResourceCall(t *testing.T, name string, input any) tool.Call {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return tool.Call{Name: name, Input: data, Execution: tool.InvocationContext{SessionID: "session-1", TurnID: "turn-1", ItemID: "step-1"}}
}

func testResourceBridge(t *testing.T, workspace string, store *resourceBridgeStore) (tool.Tool, tool.Tool) {
	t.Helper()
	tools := newApplicationResourceTools(store, application.Binding{Scope: application.Scope{PrincipalID: "user-1", ApplicationID: "app-1", ConnectionID: "connection-1"}, SessionID: "session-1"}, workspace)
	if len(tools) != 2 || tools[0].Definition().Name != "ReadResource" || tools[1].Definition().Name != "PublishArtifact" {
		t.Fatalf("unexpected resource tools: %v", tools)
	}
	return tools[0], tools[1]
}

func TestApplicationResourceBridgeRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native workspace-write artifact reads unavailable on Windows")
	}
	workspace := t.TempDir()
	data := []byte{0, 1, 0xff, 'A', '\n'}
	store := &resourceBridgeStore{resources: map[string][]byte{"resource-1": data}}
	read, publish := testResourceBridge(t, workspace, store)
	call := testResourceCall(t, "ReadResource", map[string]string{"resource_id": "resource-1"})
	result, err := read.Call(t.Context(), call)
	if err != nil {
		t.Fatal(err)
	}
	var delivered struct {
		Resource application.Resource `json:"resource"`
		Path     string               `json:"path"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text.Text), &delivered); err != nil {
		t.Fatal(err)
	}
	if delivered.Resource.SHA256 != testResource("resource-1", "session-1", data).SHA256 || !filepath.IsLocal(delivered.Path) || filepath.Dir(delivered.Path) != ".resources" {
		t.Fatalf("invalid delivered snapshot: %+v", delivered)
	}
	got, err := os.ReadFile(filepath.Join(workspace, delivered.Path))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("delivered bytes = %v, %v", got, err)
	}
	if _, err := read.Call(t.Context(), call); err != nil {
		t.Fatalf("same resource can be read again: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, delivered.Path), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := read.Call(t.Context(), call); !errors.Is(err, application.ErrConflict) {
		t.Fatalf("changed existing resource must fail: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "output.bin"), data, 0600); err != nil {
		t.Fatal(err)
	}
	result, err = publish.Call(t.Context(), testResourceCall(t, "PublishArtifact", map[string]string{"path": "output.bin", "name": "Output", "media_type": "application/octet-stream"}))
	if err != nil {
		t.Fatal(err)
	}
	var published struct {
		Resource application.Resource `json:"resource"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text.Text), &published); err != nil {
		t.Fatal(err)
	}
	if published.Resource.Name != "Output" || published.Resource.MediaType != "application/octet-stream" || published.Resource.SHA256 != delivered.Resource.SHA256 || !bytes.Equal(store.created, data) {
		t.Fatalf("published snapshot does not match artifact: %+v", published)
	}
	if store.operation != resourceOperationID(call.Execution) {
		t.Fatalf("publish operation not derived from native item: %q", store.operation)
	}
}

func TestApplicationPublishArtifactConfinesPaths(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "outside.txt"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "outside.txt"), filepath.Join(workspace, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "linkdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspace, "directory"), 0700); err != nil {
		t.Fatal(err)
	}
	store := &resourceBridgeStore{}
	_, publish := testResourceBridge(t, workspace, store)
	for _, path := range []string{"../outside.txt", filepath.Join(outside, "outside.txt"), "link.txt", filepath.Join("linkdir", "outside.txt"), "directory", ".", "a/../directory"} {
		t.Run(path, func(t *testing.T) {
			_, err := publish.Call(t.Context(), testResourceCall(t, "PublishArtifact", map[string]string{"path": path, "name": "name", "media_type": "text/plain"}))
			if err == nil {
				t.Fatal("unsafe artifact path accepted")
			}
		})
	}
	if store.calls != 0 {
		t.Fatalf("unsafe paths reached Store: %d", store.calls)
	}
}

func TestApplicationReadResourceRejectsMalformedAndSymlinkDelivery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native workspace-write artifact reads unavailable on Windows")
	}
	workspace := t.TempDir()
	outside := t.TempDir()
	store := &resourceBridgeStore{resources: map[string][]byte{"resource-1": []byte("safe")}}
	read, _ := testResourceBridge(t, workspace, store)
	call := testResourceCall(t, "ReadResource", map[string]string{"resource_id": "resource-1"})
	if err := os.Symlink(outside, filepath.Join(workspace, ".resources")); err != nil {
		t.Fatal(err)
	}
	if _, err := read.Call(t.Context(), call); !errors.Is(err, application.ErrInvalid) {
		t.Fatalf("symlink resource directory accepted: %v", err)
	}
	if files, err := os.ReadDir(outside); err != nil || len(files) != 0 {
		t.Fatalf("resource leaked outside workspace: %v, %v", files, err)
	}
	if err := os.Remove(filepath.Join(workspace, ".resources")); err != nil {
		t.Fatal(err)
	}
	// A provider-supplied ID never becomes a filesystem path; only the Store
	// determines which ID and bytes belong to this Session.
	if _, err := read.Call(t.Context(), testResourceCall(t, "ReadResource", map[string]string{"resource_id": "../outside"})); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("unknown resource unexpectedly materialized: %v", err)
	}
	store.resources["resource-1"] = []byte("safe")
	if _, err := read.Call(t.Context(), call); err != nil {
		t.Fatalf("valid snapshot not delivered: %v", err)
	}
}

func TestApplicationArtifactChangedDuringSnapshot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native workspace-write artifact reads unavailable on Windows")
	}
	workspace := t.TempDir()
	path := filepath.Join(workspace, "output.txt")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	_, err = readStableWorkspaceFile(root, "output.txt", nil, func() {
		if err := os.WriteFile(path, []byte("AFTER!"), 0600); err != nil {
			t.Fatal(err)
		}
	})
	if !errors.Is(err, application.ErrConflict) {
		t.Fatalf("in-place same-length change was not rejected: %v", err)
	}
	_, err = readStableWorkspaceFile(root, "output.txt", nil, func() {
		if err := os.Rename(path, filepath.Join(workspace, "moved.txt")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("AFTER!"), 0600); err != nil {
			t.Fatal(err)
		}
	})
	if !errors.Is(err, application.ErrConflict) {
		t.Fatalf("path substitution was not rejected: %v", err)
	}
}

func TestApplicationResourceBridgeLimitsAndNativeIdentity(t *testing.T) {
	workspace := t.TempDir()
	store := &resourceBridgeStore{resources: map[string][]byte{"oversize": bytes.Repeat([]byte("a"), application.MaxResourceBytes+1)}}
	read, publish := testResourceBridge(t, workspace, store)
	if _, err := read.Call(t.Context(), testResourceCall(t, "ReadResource", map[string]string{"resource_id": "oversize"})); !errors.Is(err, application.ErrInvalid) {
		t.Fatalf("oversize resource accepted: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "oversize"), bytes.Repeat([]byte("b"), application.MaxResourceBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := publish.Call(t.Context(), testResourceCall(t, "PublishArtifact", map[string]string{"path": "oversize", "name": "name", "media_type": "text/plain"})); !errors.Is(err, application.ErrInvalid) {
		t.Fatalf("oversize artifact accepted: %v", err)
	}
	for _, execution := range []tool.InvocationContext{{}, {SessionID: "other", TurnID: "turn-1", ItemID: "step-1"}, {SessionID: "session-1", ItemID: "step-1"}, {SessionID: "session-1", TurnID: "turn-1"}} {
		call := testResourceCall(t, "ReadResource", map[string]string{"resource_id": "oversize"})
		call.Execution = execution
		if _, err := read.Call(t.Context(), call); !errors.Is(err, application.ErrUnauthorized) {
			t.Fatalf("untrusted read identity %+v: %v", execution, err)
		}
		call = testResourceCall(t, "PublishArtifact", map[string]string{"path": "oversize", "name": "name", "media_type": "text/plain"})
		call.Execution = execution
		if _, err := publish.Call(t.Context(), call); !errors.Is(err, application.ErrUnauthorized) {
			t.Fatalf("untrusted publish identity %+v: %v", execution, err)
		}
	}
	if store.calls != 1 {
		t.Fatalf("invalid calls reached store: %d", store.calls)
	}
}
