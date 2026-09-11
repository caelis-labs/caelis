package gatewayapp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/internal/controlassembly"
)

const privateACPDiagnostic = "controlclient: feed replacement crossed output boundary at /private/diagnostic-fixture"

func TestACPChildFailureReachesStoreLogsWithoutEnteringTaskResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	root := t.TempDir()
	_, plane, err := injectACPControlPlane(runtime.Config{Diagnostics: newRuntimeDiagnosticsLogger(root)},
		controlassembly.ResolvedAssembly{Agents: []controlassembly.AgentConfig{{
			Name: "diagnostic-helper", Command: os.Args[0],
			Args: []string{"-test.run=^TestACPDiagnosticsHelperProcess$", "--"},
			Env:  map[string]string{"CAELIS_ACP_DIAGNOSTICS_HELPER": "1"},
		}}}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := plane.Quiesce(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	anchor, _, err := plane.Subagents.Spawn(ctx, subagent.SpawnContext{
		SessionRef: session.SessionRef{SessionID: "parent-diagnostics"},
		TaskID:     "task-diagnostics", ActivityID: "activity-diagnostics", ParentCallID: "call-diagnostics",
		CWD: root, Output: discardDiagnosticOutput{},
	}, delegation.Request{Agent: "diagnostic-helper", Prompt: "prompt body must not be logged"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := plane.Subagents.Wait(ctx, anchor, 5000)
	if err != nil || result.State != delegation.StateUnknownOutcome {
		t.Fatalf("result = %#v, %v", result, err)
	}
	public, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(public), privateACPDiagnostic) || strings.Contains(string(public), "/private/") {
		t.Fatalf("private diagnostics entered Task result: %s", public)
	}
	data, err := os.ReadFile(filepath.Join(root, "logs", runtimeDiagnosticsFilename))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "prompt body must not be logged") {
		t.Fatal("prompt leaked into diagnostics")
	}
	phases := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["component"] != "acp_subagent" {
			continue
		}
		if record["task_id"] != "task-diagnostics" || record["activity_id"] != "activity-diagnostics" ||
			record["session_id"] != "child-diagnostics" || record["parent_session_id"] != "parent-diagnostics" ||
			record["parent_call_id"] != "call-diagnostics" || record["rpc_code"] != float64(-32603) {
			t.Fatalf("missing correlation: %#v", record)
		}
		if !strings.Contains(line, privateACPDiagnostic) {
			t.Fatalf("original error not retained: %s", line)
		}
		phase, _ := record["phase"].(string)
		phases[phase] = true
	}
	if !phases["prompt_response"] || !phases["prompt_settlement"] {
		t.Fatalf("phases = %#v", phases)
	}
}

type discardDiagnosticOutput struct{}

func (discardDiagnosticOutput) ObserveTaskOutput(context.Context, output.Event) error { return nil }

type diagnosticsACPAgent struct{}

func (diagnosticsACPAgent) Initialize(context.Context, acpsdk.InitializeRequest) (acpsdk.InitializeResponse, error) {
	return acpsdk.InitializeResponse{ProtocolVersion: 1}, nil
}
func (diagnosticsACPAgent) NewSession(context.Context, acpsdk.NewSessionRequest) (acpsdk.NewSessionResponse, error) {
	return acpsdk.NewSessionResponse{SessionId: "child-diagnostics"}, nil
}
func (diagnosticsACPAgent) Prompt(context.Context, acpsdk.PromptRequest) (acpsdk.PromptResponse, error) {
	return acpsdk.PromptResponse{}, acpsdk.NewInternalError(map[string]any{"error": privateACPDiagnostic})
}
func (diagnosticsACPAgent) Cancel(context.Context, acpsdk.CancelNotification) error { return nil }

func TestACPDiagnosticsHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_ACP_DIAGNOSTICS_HELPER") != "1" {
		return
	}
	peer := acpsdk.NewAgentSideConnection(diagnosticsACPAgent{}, os.Stdout, os.Stdin)
	_ = peer.Wait(context.Background())
	os.Exit(0)
}
