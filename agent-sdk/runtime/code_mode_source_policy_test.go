package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

type codeModeCountingFS struct {
	sandbox.FileSystem
	opens *atomic.Int32
}

func (f codeModeCountingFS) Open(path string) (*os.File, error) {
	f.opens.Add(1)
	return f.FileSystem.Open(path)
}

type codeModeCountingRuntime struct {
	sandbox.Runtime
	opens *atomic.Int32
}

func (r codeModeCountingRuntime) FileSystemFor(constraints sandbox.Constraints) sandbox.FileSystem {
	provider := r.Runtime.(interface {
		FileSystemFor(sandbox.Constraints) sandbox.FileSystem
	})
	return codeModeCountingFS{FileSystem: provider.FileSystemFor(constraints), opens: r.opens}
}

func TestCodeModeSourceReadPolicyUsesConcreteSemanticsAndExistingGrants(t *testing.T) {
	for _, name := range []string{"relative", "sensitive", "tilde", "symlink", "granted", "inline"} {
		t.Run(name, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			// The complete sensitive tree is a temporary fixture, never a user
			// home. Its existing target also makes the symlink a reachable read.
			home := filepath.Join(root, "home")
			sensitive := filepath.Join(home, ".ssh", "fixture.js")
			if err := os.MkdirAll(filepath.Dir(sensitive), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			base, err := host.New(host.Config{CWD: root})
			if err != nil {
				t.Fatal(err)
			}
			defer base.Close()
			fixture := filepath.Join(root, "safe.js")
			if err := os.WriteFile(fixture, []byte("frozen fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(sensitive, []byte("frozen fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			path := sensitive
			wantOpens := int32(0)
			metadata := map[string]any{}
			switch name {
			case "relative":
				path, wantOpens = "safe.js", 1
			case "tilde":
				path = "~/.ssh/" + filepath.Base(sensitive)
			case "symlink":
				path = filepath.Join(root, "link.js")
				if err := os.Symlink(sensitive, path); err != nil {
					t.Fatal(err)
				}
			case "granted":
				metadata[policy.MetadataExtraReadRoots] = []string{filepath.Dir(sensitive)}
				wantOpens = 1
			}
			input := map[string]string{"file": path}
			if name == "inline" {
				input = map[string]string{"code": "frozen fixture"}
			}
			var opens, executed atomic.Int32
			// An arbitrary name still carries the concrete implementation's read
			// semantics; allowed reads still pass through actual constrained FS.
			declaration := &codemode.Tool{Name: "fixture_script", Sandbox: codeModeCountingRuntime{Runtime: base, opens: &opens}, Executor: codeExecutorFunc(func(_ context.Context, source codemode.Source, _ codemode.Host) (json.RawMessage, error) {
				if source.Code != "frozen fixture" {
					t.Errorf("unexpected source: %q", source.Code)
				}
				executed.Add(1)
				return []byte(`{}`), nil
			})}
			raw, _ := json.Marshal(input)
			llm := codeModeModel(false)
			llm.steps = func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return []model.ToolCall{{ID: "script", Name: declaration.Name, Args: string(raw)}}
				}
				return nil
			}
			registry, err := presets.NewRegistry()
			if err != nil {
				t.Fatal(err)
			}
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, PolicyRegistry: registry, DefaultPolicyMode: presets.ModeWorkspaceWrite})
			if err != nil {
				t.Fatal(err)
			}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "read fixture source", AgentSpec: agent.AgentSpec{Model: llm, Tools: []tool.Tool{declaration}, Metadata: metadata}})
			if err != nil {
				t.Fatal(err)
			}
			if err := run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			wantExecutions := int32(0)
			if wantOpens == 1 || name == "inline" {
				wantExecutions = 1
			}
			if opens.Load() != wantOpens || executed.Load() != wantExecutions {
				t.Fatalf("file opens=%d want=%d executions=%d want=%d", opens.Load(), wantOpens, executed.Load(), wantExecutions)
			}
		})
	}
}
