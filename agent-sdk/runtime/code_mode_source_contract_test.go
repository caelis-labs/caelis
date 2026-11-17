package runtime

import (
	"bytes"
	"encoding/json"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

func TestCodeModeSourceInputAndUserDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    map[string]any
		want    string
		failure bool
	}{
		{"neither", map[string]any{}, "invalid_input", true},
		{"both", map[string]any{"code": "return 1;", "file": "probe.js"}, "invalid_input", true},
		{"file", map[string]any{"file": "probe.js"}, "probe.js:3", true},
		{"undefined_field", map[string]any{"code": "return {nested:{bad:undefined}};"}, "$.nested.bad", true},
		{"omitted_return", map[string]any{"code": "const value=1;"}, "null", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			backend, err := host.New(host.Config{CWD: root})
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()
			if err = os.WriteFile(filepath.Join(root, "probe.js"), []byte("const a=1;\nconst b=2;\nthrow new Error('user failure');"), 0600); err != nil {
				t.Fatal(err)
			}
			args, _ := json.Marshal(tc.args)
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return []model.ToolCall{{ID: "source", Name: codemode.ToolName, Args: string(args)}}
				}
				return nil
			}}
			if tc.name == "neither" || tc.name == "both" {
				rt, err := New(Config{Sessions: store, TaskStore: sessionfile.NewTaskStore(store), AgentFactory: chat.Factory{}})
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = rt.ReleaseCodeModeEnvironments() }()
				run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}, Sandbox: backend}}}})
				if err != nil {
					t.Fatal(err)
				}
				if err := run.Handle.WaitCompletion(t.Context()); err == nil || !strings.Contains(err.Error(), "oneOf") {
					t.Fatalf("invalid source not rejected during preparation: %v", err)
				}
				events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range events {
					if e.Tool != nil {
						t.Fatal("invalid source committed a partial tool batch")
					}
				}
				return
			}
			runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}, Sandbox: backend}}}, nil, nil)
			result := errorDeliveryModelResult(t, driver.requests[len(driver.requests)-1], "source")
			raw, _ := json.Marshal(result.Content)
			if result.IsError != tc.failure || !bytes.Contains(raw, []byte(tc.want)) {
				t.Fatalf("wrong source error/result: %s", raw)
			}
			if (tc.name == "neither" || tc.name == "both") && bytes.Contains(raw, []byte("unknown_outcome")) {
				t.Fatal("pre-execution input became unknown")
			}
			if tc.name == "file" && bytes.Contains(raw, []byte("bootstrap")) {
				t.Fatal("internal bootstrap displaced user source")
			}
		})
	}
}
