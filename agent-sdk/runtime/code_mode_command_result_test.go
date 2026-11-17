package runtime

import (
	"bytes"
	"encoding/json"
	goruntime "runtime"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

func TestCodeModeRealRunCommand(t *testing.T) {
	short, yielding, long := "printf command-done", "sleep 0.2; printf command-done", "sleep 30"
	if goruntime.GOOS == "windows" {
		short = "Write-Output command-done"
		yielding = "Start-Sleep -Milliseconds 200; Write-Output command-done"
		long = "Start-Sleep -Seconds 30"
	}
	q := func(s string) string { raw, _ := json.Marshal(s); return string(raw) }
	for _, tc := range []struct {
		name, body string
		yield      bool
		want       string
	}{
		{"short", `return await RunCommand({command:` + q(short) + `});`, false, "command-done"},
		{"inner_yield", `const body=await RunCommand({command:` + q(yielding) + `,yield_time_ms:0});const c=body.json();if(!c.handle)throw Error('missing command handle');return await Task({action:'wait',handle:c.handle});`, false, "command-done"},
		{"outer_yield", `return await RunCommand({command:` + q(yielding) + `});`, true, "command-done"},
		{"cancel_inner", `const body=await RunCommand({command:` + q(long) + `,yield_time_ms:0});const c=body.json();let end;try{end=await Task({action:'cancel',handle:c.handle});}catch(e){end=e.result;}return {state:end.json().state};`, false, "cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			backend, err := host.New(host.Config{CWD: root})
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()
			command, err := shell.NewRunCommand(shell.RunCommandConfig{Runtime: backend})
			if err != nil {
				t.Fatal(err)
			}
			var scriptHandle string
			driver := &mcpScriptModel{steps: func(i int, r *model.Request) []model.ToolCall {
				if i == 0 {
					args := map[string]any{"code": tc.body}
					if tc.yield {
						args["yield_time_ms"] = 0
					}
					raw, _ := json.Marshal(args)
					return []model.ToolCall{{ID: "script", Name: codemode.ToolName, Args: string(raw)}}
				}
				if i == 1 && tc.yield {
					scriptHandle = jsExecTaskHandle(r)
					raw, _ := json.Marshal(map[string]any{"action": "wait", "handle": scriptHandle})
					return []model.ToolCall{{ID: "wait", Name: "Task", Args: string(raw)}}
				}
				return nil
			}}
			_, rt := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{command, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}, nil, nil)
			id := "script"
			if tc.yield {
				id = "wait"
			}
			visible := errorDeliveryModelResult(t, driver.requests[len(driver.requests)-1], id)
			raw, _ := json.Marshal(visible.Content)
			if (visible.IsError && tc.name != "cancel_inner") || !bytes.Contains(raw, []byte(tc.want)) {
				t.Fatalf("missing actual command result: %s", raw)
			}
			entries, err := rt.tasks.store.ListSession(t.Context(), active.SessionRef)
			if err != nil {
				t.Fatal(err)
			}
			codeID, commandID := "", ""
			for _, e := range entries {
				if e.Running {
					t.Fatalf("task still live: %+v", e)
				}
				switch e.Kind {
				case taskapi.KindCodeMode:
					codeID = e.TaskID
				case taskapi.KindCommand:
					commandID = e.TaskID
					process, err := backend.OpenSession(taskStringValue(e.Metadata["session_id"]))
					if err != nil {
						t.Fatal(err)
					}
					status, err := process.Status(t.Context())
					if err != nil || status.Running {
						t.Fatalf("native command process/readers not reaped: %+v %v", status, err)
					}
				}
			}
			if codeID == "" || commandID == "" || codeID == commandID {
				t.Fatalf("task identities code=%s command=%s", codeID, commandID)
			}
			events, _ := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			calls := 0
			for _, e := range events {
				if e.Type == session.EventTypeToolCall && e.Tool != nil && e.Tool.Name == "RunCommand" && e.Tool.Status == "pending" {
					calls++
					if e.Tool.Parent == nil || e.Tool.Parent.Name != codemode.ToolName {
						t.Fatal("lost direct script parent")
					}
				}
			}
			if calls != 1 {
				t.Fatalf("command claims=%d", calls)
			}
			if tc.yield && strings.TrimSpace(scriptHandle) == "" {
				t.Fatalf("outer handle=%q", scriptHandle)
			}
		})
	}
}
