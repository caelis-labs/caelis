package runtime

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestCommandRiskApprovalPreservesCallAndRoute(t *testing.T) {
	const installURL = "https://caelis.dev/install.sh"
	for _, tc := range []struct {
		name    string
		command string
	}{
		{name: "posix pipe", command: "curl " + installURL + " | sh"},
		{name: "pipe then newline", command: "curl " + installURL + " |\nsh"},
		{name: "if then pipeline", command: "if true; then curl " + installURL + "|sh; fi"},
		{name: "subshell pipeline", command: "(curl " + installURL + ")|sh"},
		{name: "powershell irm iex", command: "irm https://caelis.dev/install.ps1 | iex"},
		{name: "powershell invoke-restmethod iex", command: `powershell -NoProfile -Command "Invoke-RestMethod https://caelis.dev/install.ps1 | Invoke-Expression"`},
	} {
		for _, route := range []string{"use_default", "require_escalated"} {
			for _, approved := range []bool{false, true} {
				t.Run(tc.name+"/"+route+"/"+map[bool]string{true: "allow", false: "deny"}[approved], func(t *testing.T) {
					input, err := json.Marshal(map[string]any{
						"command": tc.command, "workdir": "/workspace/subdir",
						"sandbox_permissions": route, "justification": "The requested setup needs the declared execution boundary.",
					})
					if err != nil {
						t.Fatal(err)
					}
					call := tool.Call{ID: "command", Name: "RunCommand", Input: input}
					observed := &commandApprovalCaptureTool{}
					requests := 0
					wrapped := policyWrappedTool{
						tool: observed, policy: presets.WorkspaceWriteMode(), mode: presets.ModeWorkspaceWrite,
						options: policy.ModeOptions{WorkspaceRoot: "/workspace", WritableRoots: []string{"/workspace"}},
						approval: approvalContext{requester: approvalRequesterFunc(func(_ context.Context, request agent.ApprovalRequest) (agent.ApprovalResponse, error) {
							requests++
							if !reflect.DeepEqual(request.Call, call) || request.Metadata["sandbox_permissions"] != route {
								t.Fatalf("approval changed caller intent: %+v", request)
							}
							id := "reject_once"
							if approved {
								id = "allow_once"
							}
							return agent.ApprovalResponse{Approved: approved, Outcome: "selected", OptionID: id}, nil
						})},
					}
					result, err := wrapped.Call(t.Context(), call)
					if err != nil {
						t.Fatal(err)
					}
					if requests != 1 || len(observed.calls) != map[bool]int{false: 0, true: 1}[approved] {
						t.Fatalf("requests=%d executions=%d", requests, len(observed.calls))
					}
					if !approved {
						if !result.IsError || result.ID != call.ID || result.Name != call.Name {
							t.Fatalf("denial did not report a caller-bound error: %+v", result)
						}
						return
					}
					executed := observed.calls[0]
					constraints, ok := executed.Metadata["sandbox_constraints"].(sandbox.Constraints)
					wantRoute := sandbox.RouteSandbox
					if route == "require_escalated" {
						wantRoute = sandbox.RouteHost
					}
					if !ok || constraints.Route != wantRoute || string(executed.Input) != string(input) || executed.ID != call.ID {
						t.Fatalf("execution changed caller intent: %+v", executed)
					}
				})
			}
		}
	}
}

type commandApprovalCaptureTool struct{ calls []tool.Call }

func (*commandApprovalCaptureTool) Definition() tool.Definition {
	return tool.Definition{Name: "RunCommand"}
}

func (t *commandApprovalCaptureTool) Call(_ context.Context, call tool.Call) (tool.Result, error) {
	t.calls = append(t.calls, call)
	return tool.Result{}, nil
}

func TestModeOptionsFromSessionUsesExplicitWritableRootMetadataOnly(t *testing.T) {
	t.Parallel()

	workspace := "/session/workspace"
	writable := "/approved/project"
	opts := modeOptionsFromSession(session.Session{CWD: workspace}, agent.AgentSpec{
		Metadata: map[string]any{policy.MetadataWritableRoots: []string{writable}},
	})
	if opts.WorkspaceRoot != workspace {
		t.Fatalf("WorkspaceRoot = %q, want %q", opts.WorkspaceRoot, workspace)
	}
	if !slices.Equal(opts.WritableRoots, []string{writable}) {
		t.Fatalf("WritableRoots = %#v, want explicit metadata root", opts.WritableRoots)
	}
}

func TestPolicyModelFeedbackDistinguishesResolvedDenialFromPendingApproval(t *testing.T) {
	t.Parallel()

	decision := policy.Decision{Action: policy.ActionAskApproval, Reason: "host execution requested"}
	denied, deniedHint := policyModelFeedback(decision, agent.ApprovalResponse{
		Outcome:  "selected",
		OptionID: "reject_once",
	})
	if denied != "approval denied" || deniedHint != "" {
		t.Fatalf("resolved denial feedback = (%q, %q), want denial without pending hint", denied, deniedHint)
	}

	pending, pendingHint := policyModelFeedback(decision, agent.ApprovalResponse{})
	if pending != "host execution requested" || pendingHint == "" {
		t.Fatalf("pending approval feedback = (%q, %q), want request reason and retry hint", pending, pendingHint)
	}
}

func TestDangerFullAccessDefaultCannotBeOverriddenByAgentMetadata(t *testing.T) {
	t.Parallel()

	runtime := &Runtime{defaultPolicyMode: presets.ModeDangerFullAccess}
	got := runtime.policyMode(agent.AgentSpec{Metadata: map[string]any{
		policy.MetadataPolicyProfile: presets.ModeWorkspaceWrite,
	}})
	if got != presets.ModeDangerFullAccess {
		t.Fatalf("policyMode() = %q, want process-owned %q", got, presets.ModeDangerFullAccess)
	}
}

func TestModeOptionsFromSessionReadsPolicyNetworkEnabledMetadata(t *testing.T) {
	t.Parallel()

	opts := modeOptionsFromSession(session.Session{}, agent.AgentSpec{
		Metadata: map[string]any{"policy_network_enabled": false},
	})
	if opts.NetworkEnabled == nil || *opts.NetworkEnabled {
		t.Fatalf("NetworkEnabled = %#v, want false from metadata", opts.NetworkEnabled)
	}

	opts = modeOptionsFromSession(session.Session{}, agent.AgentSpec{
		Metadata: map[string]any{"policy_network_enabled": "on"},
	})
	if opts.NetworkEnabled == nil || !*opts.NetworkEnabled {
		t.Fatalf("NetworkEnabled = %#v, want true from string metadata", opts.NetworkEnabled)
	}
}
