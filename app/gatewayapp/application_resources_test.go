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
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

type resourceBridgeStore struct {
	resources map[string][]byte
	created   []byte
	operation string
	calls     int
}

func (s *resourceBridgeStore) ReadResource(_ context.Context, _ sessionprofile.Scope, session, id string) (sessionprofile.Resource, []byte, error) {
	s.calls++
	data, ok := s.resources[id]
	if !ok {
		return sessionprofile.Resource{}, nil, sessionprofile.ErrNotFound
	}
	return testResource(id, session, data), bytes.Clone(data), nil
}

func (s *resourceBridgeStore) CreateResource(_ context.Context, _ sessionprofile.Scope, session, op, name, mediaType string, data []byte) (sessionprofile.Resource, error) {
	s.calls++
	s.created = bytes.Clone(data)
	s.operation = op
	resource := testResource("artifact-1", session, data)
	resource.Name = name
	resource.MediaType = mediaType
	return resource, nil
}

func testResource(id, session string, data []byte) sessionprofile.Resource {
	sum := sha256.Sum256(data)
	return sessionprofile.Resource{ID: id, SessionID: session, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
}

func testResourceInput(t *testing.T, input any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func testResourceBridge(workspace string, store *resourceBridgeStore) applicationResourceBridge {
	return applicationResourceBridge{
		store: store, binding: sessionprofile.Binding{Scope: sessionprofile.Scope{PrincipalID: "user-1"}, SessionID: "session-1"}, workspace: workspace,
	}
}

func testResourceInvocation() tool.InvocationContext {
	return tool.InvocationContext{SessionID: "session-1", TurnID: "turn-1", ItemID: "step-1"}
}

func TestApplicationResourceBridgeRoundTrip(t *testing.T) {
	workspace := t.TempDir()
	data := []byte{0, 1, 0xff, 'A', '\n'}
	store := &resourceBridgeStore{resources: map[string][]byte{"resource-1": data}}
	bridge := testResourceBridge(workspace, store)
	call := testResourceInput(t, map[string]string{"resource_id": "resource-1"})
	result, err := bridge.read(t.Context(), call, testResourceInvocation())
	if err != nil {
		t.Fatal(err)
	}
	var delivered struct {
		Resource sessionprofile.Resource `json:"resource"`
		Path     string                  `json:"path"`
	}
	deliveredJSON, ok := result.Structured()
	if !ok {
		t.Fatalf("resource delivery returned no structured result: %+v", result)
	}
	if err := json.Unmarshal(deliveredJSON, &delivered); err != nil {
		t.Fatal(err)
	}
	if delivered.Resource.SHA256 != testResource("resource-1", "session-1", data).SHA256 || !filepath.IsLocal(delivered.Path) || filepath.Dir(delivered.Path) != ".resources" {
		t.Fatalf("invalid delivered snapshot: %+v", delivered)
	}
	got, err := os.ReadFile(filepath.Join(workspace, delivered.Path))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("delivered bytes = %v, %v", got, err)
	}
	if _, err := bridge.read(t.Context(), call, testResourceInvocation()); err != nil {
		t.Fatalf("same resource can be read again: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, delivered.Path), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.read(t.Context(), call, testResourceInvocation()); !errors.Is(err, sessionprofile.ErrConflict) {
		t.Fatalf("changed existing resource must fail: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "output.bin"), data, 0600); err != nil {
		t.Fatal(err)
	}
	result, err = bridge.publish(t.Context(), testResourceInput(t, map[string]string{"path": "output.bin", "name": "Output", "media_type": "application/octet-stream"}), testResourceInvocation())
	if err != nil {
		t.Fatal(err)
	}
	var published struct {
		Resource sessionprofile.Resource `json:"resource"`
	}
	publishedJSON, ok := result.Structured()
	if !ok {
		t.Fatalf("resource publication returned no structured result: %+v", result)
	}
	if err := json.Unmarshal(publishedJSON, &published); err != nil {
		t.Fatal(err)
	}
	if published.Resource.Name != "Output" || published.Resource.MediaType != "application/octet-stream" || published.Resource.SHA256 != delivered.Resource.SHA256 || !bytes.Equal(store.created, data) {
		t.Fatalf("published snapshot does not match artifact: %+v", published)
	}
	if store.operation != resourceOperationID(testResourceInvocation()) {
		t.Fatalf("publish operation not derived from native item: %q", store.operation)
	}
}

func TestApplicationPublishArtifactConfinesPaths(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "outside.txt"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	paths := []string{"../outside.txt", filepath.Join(outside, "outside.txt"), filepath.Join("linkdir", "outside.txt"), "directory", ".", "a/../directory"}
	if err := os.Symlink(filepath.Join(outside, "outside.txt"), filepath.Join(workspace, "link.txt")); err == nil {
		paths = append(paths, "link.txt")
	} else if runtime.GOOS != "windows" {
		t.Fatal(err)
	} else {
		t.Logf("file symlink fixture unavailable; directory junction confinement is still exercised: %v", err)
	}
	applicationDirectoryLink(t, filepath.Join(workspace, "linkdir"), outside)
	if err := os.Mkdir(filepath.Join(workspace, "directory"), 0700); err != nil {
		t.Fatal(err)
	}
	store := &resourceBridgeStore{}
	bridge := testResourceBridge(workspace, store)
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			_, err := bridge.publish(t.Context(), testResourceInput(t, map[string]string{"path": path, "name": "name", "media_type": "text/plain"}), testResourceInvocation())
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
	workspace := t.TempDir()
	outside := t.TempDir()
	store := &resourceBridgeStore{resources: map[string][]byte{"resource-1": []byte("safe")}}
	bridge := testResourceBridge(workspace, store)
	call := testResourceInput(t, map[string]string{"resource_id": "resource-1"})
	applicationDirectoryLink(t, filepath.Join(workspace, ".resources"), outside)
	if _, err := bridge.read(t.Context(), call, testResourceInvocation()); !errors.Is(err, sessionprofile.ErrInvalid) {
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
	if _, err := bridge.read(t.Context(), testResourceInput(t, map[string]string{"resource_id": "../outside"}), testResourceInvocation()); !errors.Is(err, sessionprofile.ErrNotFound) {
		t.Fatalf("unknown resource unexpectedly materialized: %v", err)
	}
	store.resources["resource-1"] = []byte("safe")
	if _, err := bridge.read(t.Context(), call, testResourceInvocation()); err != nil {
		t.Fatalf("valid snapshot not delivered: %v", err)
	}
}

func TestApplicationArtifactChangedDuringSnapshot(t *testing.T) {
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
	if !errors.Is(err, sessionprofile.ErrConflict) {
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
	if !errors.Is(err, sessionprofile.ErrConflict) {
		t.Fatalf("path substitution was not rejected: %v", err)
	}
}

func TestApplicationResourceBridgeLimitsAndNativeIdentity(t *testing.T) {
	workspace := t.TempDir()
	store := &resourceBridgeStore{resources: map[string][]byte{"oversize": bytes.Repeat([]byte("a"), sessionprofile.MaxResourceBytes+1)}}
	bridge := testResourceBridge(workspace, store)
	if _, err := bridge.read(t.Context(), testResourceInput(t, map[string]string{"resource_id": "oversize"}), testResourceInvocation()); !errors.Is(err, sessionprofile.ErrInvalid) {
		t.Fatalf("oversize resource accepted: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "oversize"), bytes.Repeat([]byte("b"), sessionprofile.MaxResourceBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.publish(t.Context(), testResourceInput(t, map[string]string{"path": "oversize", "name": "name", "media_type": "text/plain"}), testResourceInvocation()); !errors.Is(err, sessionprofile.ErrInvalid) {
		t.Fatalf("oversize artifact accepted: %v", err)
	}
	readInput := testResourceInput(t, map[string]string{"resource_id": "oversize"})
	publishInput := testResourceInput(t, map[string]string{"path": "oversize", "name": "name", "media_type": "text/plain"})
	tools := newApplicationResourceTools(store, bridge.binding, workspace)
	for i, call := range []tool.Call{{Name: "ReadResource", Input: readInput}, {Name: "PublishArtifact", Input: publishInput}} {
		if tools[i].Definition().Name != call.Name {
			t.Fatalf("resource tool %d = %s, want %s", i, tools[i].Definition().Name, call.Name)
		}
		if _, err := tools[i].Call(t.Context(), call); !errors.Is(err, sessionprofile.ErrUnauthorized) {
			t.Fatalf("%s accepted absent invocation identity: %v", call.Name, err)
		}
	}
	for _, execution := range []tool.InvocationContext{{SessionID: "other", TurnID: "turn-1", ItemID: "step-1"}, {SessionID: "session-1", ItemID: "step-1"}, {SessionID: "session-1", TurnID: "turn-1"}} {
		if _, err := bridge.read(t.Context(), readInput, execution); !errors.Is(err, sessionprofile.ErrUnauthorized) {
			t.Fatalf("untrusted read identity %+v: %v", execution, err)
		}
		if _, err := bridge.publish(t.Context(), publishInput, execution); !errors.Is(err, sessionprofile.ErrUnauthorized) {
			t.Fatalf("untrusted publish identity %+v: %v", execution, err)
		}
	}
	if store.calls != 1 {
		t.Fatalf("invalid calls reached store: %d", store.calls)
	}
}
