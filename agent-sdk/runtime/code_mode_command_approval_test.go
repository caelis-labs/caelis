package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

func TestCodeModeCommandApprovalUsesOriginalOwner(t *testing.T) {
	for _, mode := range []string{"allow", "deny", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			commandText := "printf once > effect.txt"
			if goruntime.GOOS == "windows" {
				commandText = "Set-Content -Path effect.txt -Value once -NoNewline"
			}
			scriptArgs, _ := json.Marshal(map[string]any{"code": "return await RunCommand({command:" + stringJSONForCommandTest(commandText) + "});", "yield_time_ms": 0})
			backend, err := host.New(host.Config{CWD: root})
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()
			command, err := shell.NewRunCommand(shell.RunCommandConfig{Runtime: backend})
			if err != nil {
				t.Fatal(err)
			}
			asked, release := make(chan string, 1), make(chan struct{})
			registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "mcp-test", Decide: func(_ context.Context, in policy.ToolContext) (policy.Decision, error) {
				if in.Tool.Name == "RunCommand" {
					return policy.Decision{Action: policy.ActionAskApproval, Approval: &session.ProtocolApproval{}}, nil
				}
				return policy.Decision{Action: policy.ActionAllow}, nil
			}}}
			requester := approvalRequesterFunc(func(ctx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
				if req.Tool.Name != "RunCommand" || req.Call.Parent == nil || req.Call.Parent.ID != "script" {
					t.Error("approval lost real command target/direct parent")
				}
				asked <- req.OperationTaskID
				select {
				case <-release:
				case <-ctx.Done():
				}
				option := "allow_once"
				if mode == "deny" {
					option = "cancel"
				}
				return agent.ApprovalResponse{Outcome: "selected", OptionID: option, Approved: mode != "deny"}, nil
			})
			var commandHandle string
			driver := &mcpScriptModel{steps: func(i int, r *model.Request) []model.ToolCall {
				if i == 0 {
					return []model.ToolCall{{ID: "script", Name: codemode.ToolName, Args: string(scriptArgs)}}
				}
				if i == 1 {
					select {
					case commandID := <-asked:
						if mode == "cancel" {
							entries, err := sessionfile.NewTaskStore(store).ListSession(t.Context(), active.SessionRef)
							if err != nil {
								t.Fatal(err)
							}
							for _, entry := range entries {
								if entry.TaskID == commandID {
									commandHandle = entry.Handle
								}
							}
							if commandHandle == "" {
								t.Fatal("pending approval lost its original command Task")
							}
						}
					case <-time.After(5 * time.Second):
						t.Error("command never reached original approval")
						return nil
					}
					if _, err := os.Stat(filepath.Join(root, "effect.txt")); !os.IsNotExist(err) {
						t.Error("command executed before approval")
					}
					action := "wait"
					if mode == "cancel" {
						action = "cancel"
					} else {
						close(release)
					}
					handle := jsExecTaskHandle(r)
					if mode == "cancel" {
						handle = commandHandle
					}
					raw, _ := json.Marshal(map[string]any{"action": action, "handle": handle})
					return []model.ToolCall{{ID: "observe", Name: "Task", Args: string(raw)}}
				}
				return nil
			}}
			_, rt := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{command, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}, Sandbox: backend}}}, registry, requester)
			raw, err := os.ReadFile(filepath.Join(root, "effect.txt"))
			if mode == "allow" {
				if err != nil || string(raw) != "once" {
					t.Fatalf("actual command effect=%q %v", raw, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("rejected/cancelled command effect=%q %v", raw, err)
			}
			entries, err := rt.tasks.store.ListSession(t.Context(), active.SessionRef)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Running {
					t.Fatalf("producer did not close: %+v", entry)
				}
			}
		})
	}
}

func stringJSONForCommandTest(s string) string { raw, _ := json.Marshal(s); return string(raw) }
