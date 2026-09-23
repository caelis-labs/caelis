package gatewayapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/application"
)

// applicationResourceStore is the narrow Host-private boundary between native
// execution files and the immutable, session-owned resource store.
type applicationResourceStore interface {
	CreateResource(context.Context, application.Scope, string, string, string, string, []byte) (application.Resource, error)
	ReadResource(context.Context, application.Scope, string, string) (application.Resource, []byte, error)
}

// newApplicationResourceTools admits bytes only from the bound Store or from an
// already isolated execution workspace. The caller adds these tools only to a
// workspace-write application profile, whose workspace has a native sandbox.
func newApplicationResourceTools(store applicationResourceStore, binding application.Binding, workspace string) []tool.Tool {
	resource := tool.NamedTool{Def: tool.Definition{
		Name: "ReadResource", Description: "Copy one immutable uploaded resource into the isolated execution workspace. Returns its relative path and metadata; use the path with workspace tools to inspect bytes.",
		EffectClass: tool.EffectIdempotent,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"resource_id": map[string]any{"type": "string"}}, "required": []string{"resource_id"}, "additionalProperties": false},
	}, Invoke: func(ctx context.Context, call tool.Call) (tool.Result, error) {
		if err := validateResourceCall(call, binding); err != nil {
			return tool.Result{}, err
		}
		var input struct {
			ResourceID string `json:"resource_id"`
		}
		if err := decodeResourceInput(call.Input, &input); err != nil {
			return tool.Result{}, err
		}
		if input.ResourceID == "" || store == nil {
			return tool.Result{}, application.ErrInvalid
		}
		meta, data, err := store.ReadResource(ctx, binding.Scope, binding.SessionID, input.ResourceID)
		if err != nil {
			return tool.Result{}, err
		}
		if err := validateResourceSnapshot(meta, data, binding.SessionID, input.ResourceID); err != nil {
			return tool.Result{}, err
		}
		root, err := os.OpenRoot(workspace)
		if err != nil {
			return tool.Result{}, err
		}
		defer root.Close()
		path, err := materializeResource(root, input.ResourceID, data)
		if err != nil {
			return tool.Result{}, err
		}
		return resourceToolResult(struct {
			Resource application.Resource `json:"resource"`
			Path     string               `json:"path"`
		}{meta, path})
	}}
	artifact := tool.NamedTool{Def: tool.Definition{
		Name: "PublishArtifact", Description: "Snapshot one regular file from the isolated execution workspace as an immutable, application-owned resource readable via the resource HTTP endpoint. Path must be relative to the workspace.",
		EffectClass: tool.EffectIdempotent,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}, "media_type": map[string]any{"type": "string"}}, "required": []string{"path", "name", "media_type"}, "additionalProperties": false},
	}, Invoke: func(ctx context.Context, call tool.Call) (tool.Result, error) {
		if err := validateResourceCall(call, binding); err != nil {
			return tool.Result{}, err
		}
		var input struct {
			Path      string `json:"path"`
			Name      string `json:"name"`
			MediaType string `json:"media_type"`
		}
		if err := decodeResourceInput(call.Input, &input); err != nil {
			return tool.Result{}, err
		}
		if !localArtifactPath(input.Path) || strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.MediaType) == "" || store == nil {
			return tool.Result{}, application.ErrInvalid
		}
		root, err := os.OpenRoot(workspace)
		if err != nil {
			return tool.Result{}, err
		}
		defer root.Close()
		data, err := readStableWorkspaceFile(root, input.Path, nil, nil)
		if err != nil {
			return tool.Result{}, err
		}
		// A native durable ItemID distinguishes two invocations even if the
		// provider reuses its untrusted Call.ID. Retrying the same native item
		// retains exactly one Store operation and conflicts on changed bytes.
		op := resourceOperationID(call.Execution)
		meta, err := store.CreateResource(ctx, binding.Scope, binding.SessionID, op, input.Name, input.MediaType, data)
		if err != nil {
			return tool.Result{}, err
		}
		if err := validateResourceSnapshot(meta, data, binding.SessionID, meta.ID); err != nil {
			return tool.Result{}, err
		}
		return resourceToolResult(struct {
			Resource application.Resource `json:"resource"`
		}{meta})
	}}
	return []tool.Tool{resource, artifact}
}

func validateResourceCall(call tool.Call, binding application.Binding) error {
	id := call.Execution
	if id.SessionID == "" || id.TurnID == "" || id.ItemID == "" || id.SessionID != binding.SessionID || binding.Archived {
		return fmt.Errorf("%w: resource invocation lacks bound native execution identity", application.ErrUnauthorized)
	}
	return nil
}

func resourceOperationID(id tool.InvocationContext) string {
	encoded, _ := json.Marshal([]string{id.SessionID, id.TurnID, id.ItemID, "PublishArtifact"})
	sum := sha256.Sum256(encoded)
	return "artifact-" + hex.EncodeToString(sum[:])
}

func decodeResourceInput(raw json.RawMessage, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("%w: invalid resource tool input: %w", application.ErrInvalid, err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("%w: resource tool requires one JSON object", application.ErrInvalid)
	}
	return nil
}

func resourceToolResult(value any) (tool.Result, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Content: []model.Part{model.NewTextPart(string(data))}}, nil
}

func validateResourceSnapshot(meta application.Resource, data []byte, sessionID, resourceID string) error {
	sum := sha256.Sum256(data)
	if meta.ID == "" || meta.ID != resourceID || meta.SessionID != sessionID || len(data) > application.MaxResourceBytes || meta.Size != int64(len(data)) || meta.SHA256 != hex.EncodeToString(sum[:]) {
		return fmt.Errorf("%w: resource snapshot metadata does not match bytes and owner", application.ErrInvalid)
	}
	return nil
}

func localArtifactPath(path string) bool {
	return path != "." && filepath.IsLocal(path) && filepath.Clean(path) == path
}

// readStableWorkspaceFile rejects nonregular files and checks both the original
// descriptor and directory entry after reading. A second read detects in-place
// changes even when the size or timestamps are preserved. The hooks permit tests
// to inject replacements at either boundary without timing races.
func readStableWorkspaceFile(root *os.Root, path string, beforeOpen, afterRead func()) ([]byte, error) {
	if !localArtifactPath(path) {
		return nil, application.ErrInvalid
	}
	before, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: artifact is not a regular file", application.ErrInvalid)
	}
	if beforeOpen != nil {
		beforeOpen()
	}
	// Never use Root.Open here: a FIFO swapped in after Lstat can block
	// forever before fstat gets a chance to reject it.
	file, err := openConfinedArtifact(root, path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) || opened.Size() > application.MaxResourceBytes {
		return nil, fmt.Errorf("%w: artifact changed or exceeds resource limit", application.ErrInvalid)
	}
	read := func() ([]byte, error) {
		data, err := io.ReadAll(io.LimitReader(file, int64(application.MaxResourceBytes)+1))
		if err != nil {
			return nil, err
		}
		if len(data) > application.MaxResourceBytes {
			return nil, fmt.Errorf("%w: artifact exceeds resource limit", application.ErrInvalid)
		}
		return data, nil
	}
	data, err := read()
	if err != nil {
		return nil, err
	}
	if afterRead != nil {
		afterRead()
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	verified, err := read()
	if err != nil {
		return nil, err
	}
	end, err := file.Stat()
	if err != nil {
		return nil, err
	}
	current, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(data, verified) || !os.SameFile(before, current) || !end.Mode().IsRegular() || end.Size() != int64(len(data)) || opened.Size() != end.Size() || !opened.ModTime().Equal(end.ModTime()) {
		return nil, fmt.Errorf("%w: artifact changed during snapshot", application.ErrConflict)
	}
	return data, nil
}

func materializeResource(root *os.Root, resourceID string, data []byte) (string, error) {
	const dir = ".resources"
	if err := root.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := root.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: resource directory is not a directory", application.ErrInvalid)
	}
	folder, err := root.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer folder.Close()
	pinned, err := folder.Stat(".")
	if err != nil || !os.SameFile(info, pinned) {
		return "", fmt.Errorf("resource directory changed before delivery: %w", errors.Join(application.ErrConflict, err))
	}
	sum := sha256.Sum256([]byte(resourceID))
	name := hex.EncodeToString(sum[:])
	path := filepath.Join(dir, name)
	file, err := folder.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		// Never follow an existing symlink or silently accept modified bytes.
		existing, readErr := readStableWorkspaceFile(folder, name, nil, nil)
		if readErr != nil || !bytes.Equal(existing, data) {
			return "", fmt.Errorf("%w: existing resource file differs", application.ErrConflict)
		}
		current, err := root.Lstat(dir)
		if err != nil || !os.SameFile(info, current) {
			return "", fmt.Errorf("resource directory changed during delivery: %w", errors.Join(application.ErrConflict, err))
		}
		return path, nil
	}
	if err != nil {
		return "", err
	}
	written, writeErr := file.Write(data)
	closeErr := file.Close()
	if written != len(data) && writeErr == nil {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil || closeErr != nil {
		_ = folder.Remove(name)
		return "", errors.Join(writeErr, closeErr)
	}
	current, err := root.Lstat(dir)
	if err != nil || !os.SameFile(info, current) {
		return "", fmt.Errorf("resource directory changed during delivery: %w", errors.Join(application.ErrConflict, err))
	}
	return path, nil
}
