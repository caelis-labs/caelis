package codemode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestSourceReadsFreshBytesWithExistingFilesystemPolicy(t *testing.T) {
	root := t.TempDir()
	runtime, err := host.New(host.Config{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	hidden := filepath.Join(root, "hidden")
	if err := os.Mkdir(hidden, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hidden, "private.js"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	constrained := sourcePolicyRuntime{Runtime: runtime, selected: policyfs.New(runtime.FileSystem(), func() policy.Policy { return policy.Policy{HiddenRoots: []string{hidden}} })}
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
	link := filepath.Join(root, "link.js")
	if err := os.Symlink(filepath.Join(hidden, "private.js"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := read(`{"file":"link.js"}`); err == nil {
		t.Fatal("symlink escaped existing hidden-path policy")
	}
}
