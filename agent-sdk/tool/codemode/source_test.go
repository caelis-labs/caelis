package codemode

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/internal/sandboxmeta"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/policyfs"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type sourcePolicyRuntime struct {
	sandbox.Runtime
	selected sandbox.FileSystem
}

func (r sourcePolicyRuntime) FileSystemFor(sandbox.Constraints) sandbox.FileSystem { return r.selected }

type recordingSourceRuntime struct {
	sandbox.Runtime
	calls []sandbox.Constraints
}

func (r *recordingSourceRuntime) FileSystemFor(constraints sandbox.Constraints) sandbox.FileSystem {
	r.calls = append(r.calls, constraints)
	return r.Runtime.FileSystemFor(constraints)
}

func TestSourceUsesNormalizedCallConstraintsAndRejectsMalformedMetadata(t *testing.T) {
	root := t.TempDir()
	base, err := host.New(host.Config{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	if err := os.WriteFile(filepath.Join(root, "script.js"), []byte("return 1"), 0600); err != nil {
		t.Fatal(err)
	}
	rt := &recordingSourceRuntime{Runtime: base}
	declaration := &Tool{Sandbox: rt}
	call := tool.Call{Input: []byte(`{"file":"script.js"}`), Metadata: map[string]any{
		sandboxmeta.ConstraintsKey: sandbox.Constraints{Route: " host ", Network: " enabled "},
	}}
	if source, err := declaration.ReadSource(call, 64); err != nil || source.Code != "return 1" {
		t.Fatalf("read source = %+v, %v", source, err)
	}
	want := []sandbox.Constraints{{Route: sandbox.RouteHost, Network: sandbox.NetworkEnabled}}
	if !reflect.DeepEqual(rt.calls, want) {
		t.Fatalf("filesystem constraints = %+v, want %+v", rt.calls, want)
	}
	call.Metadata[sandboxmeta.ConstraintsKey] = map[string]any{"path_rules": true}
	if _, err := declaration.ReadSource(call, 64); err == nil || len(rt.calls) != 1 {
		t.Fatalf("malformed constraints must fail before filesystem selection: calls = %+v, err = %v", rt.calls, err)
	}
}

func TestSourceReadsFreshBytesWithExistingFilesystemPolicy(t *testing.T) {
	root := t.TempDir()
	sandboxRuntime, err := host.New(host.Config{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	defer sandboxRuntime.Close()
	hidden := filepath.Join(root, "hidden")
	if err := os.Mkdir(hidden, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hidden, "private.js"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	constrained := sourcePolicyRuntime{Runtime: sandboxRuntime, selected: policyfs.New(sandboxRuntime.FileSystem(), func() policy.Policy { return policy.Policy{HiddenRoots: []string{hidden}} })}
	declaration := &Tool{Name: "js_repl", Sandbox: constrained}
	path := filepath.Join(root, "script.js")
	read := func(input string) (Source, error) { return declaration.ReadSource(tool.Call{Input: []byte(input)}, 64) }
	if err := os.WriteFile(path, []byte("version one"), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := read(`{"file":"script.js"}`)
	if err != nil {
		t.Fatal(err)
	}
	inline, err := read(`{"code":"version one"}`)
	if err != nil || inline.Code != first.Code || inline.SHA256 != first.SHA256 || first.Path != path {
		t.Fatalf("inline/file mismatch: %+v %+v %v", inline, first, err)
	}
	if err := os.WriteFile(path, []byte("version two"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := read(`{"file":"script.js"}`)
	if err != nil || second.Code != "version two" || second.SHA256 == first.SHA256 || first.Code != "version one" {
		t.Fatalf("stale/mutable source: %+v %+v %v", first, second, err)
	}
	for _, input := range []string{`{}`, `{"code":"x","file":"script.js"}`, `{"code":"x","file":null}`, `{"code":null,"file":"script.js"}`, `{"code":null}`, `{"code":""}`, `{"code":"x"} {}`, `{"file":"hidden/private.js"}`, `{"file":"hidden/../hidden/private.js"}`, `{"file":"."}`, `{"code":"x","unknown":true}`} {
		if _, err := read(input); err == nil {
			t.Errorf("invalid/hidden source accepted: %s", input)
		}
	}
	encoded, _ := json.Marshal(map[string]string{"code": strings.Repeat("x", 65)})
	if _, err := read(string(encoded)); err == nil {
		t.Fatal("inline source budget ignored")
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 65)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := read(`{"file":"script.js"}`); err == nil {
		t.Fatal("file budget ignored")
	}
	t.Run("hidden symlink", func(t *testing.T) {
		link := filepath.Join(root, "link.js")
		if err := os.Symlink(filepath.Join(hidden, "private.js"), link); err != nil {
			if runtime.GOOS == "windows" && (errors.Is(err, syscall.Errno(1314)) || os.IsPermission(err)) {
				t.Skipf("Windows symlink permission unavailable: %v", err)
			}
			t.Fatal(err)
		}
		if _, err := read(`{"file":"link.js"}`); err == nil {
			t.Fatal("symlink escaped existing hidden-path policy")
		}
	})
}
