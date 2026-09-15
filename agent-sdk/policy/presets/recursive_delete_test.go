package presets

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/policy"
)

func TestPowerShellDeleteAliasesBlockHomeRemoval(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"Remove-Item", "ri", "rm", "del", "erase", "rd", "rmdir"} {
		t.Run(name, func(t *testing.T) {
			for _, command := range []string{
				name + " -Recurse -Force $HOME",
				name + " $HOME -Force -Recurse",
				strings.ToUpper(name) + " -RECURSE -FORCE $HOME",
				"powershell -NoProfile -Command '" + name + " -Recurse -Force $HOME'",
				"pwsh -Command '" + name + " -Recurse -Force $HOME'",
			} {
				for _, escalated := range []bool{false, true} {
					decision, err := WorkspaceWriteMode().DecideTool(context.Background(), commandCtx(command, escalated))
					if err != nil {
						t.Fatal(err)
					}
					if decision.Action != policy.ActionDeny || !strings.Contains(decision.Reason, "system or home root") {
						t.Fatalf("command=%q escalated=%v: %#v, want catastrophic delete denial", command, escalated, decision)
					}
				}
			}
		})
	}
}

func TestPowerShellDeleteAliasesPreserveApprovalRoute(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"Remove-Item", "ri", "rm", "del", "erase", "rd", "rmdir"} {
		t.Run(name, func(t *testing.T) {
			command := name + " -Recurse -Force $HOME/caelis-delete-policy-outside"
			for _, escalated := range []bool{false, true} {
				input := commandCtx(command, escalated)
				decision, err := WorkspaceWriteMode().DecideTool(context.Background(), input)
				if err != nil {
					t.Fatal(err)
				}
				want := workspaceWriteConstraints(input.Options)
				if escalated {
					want = hostExecutionConstraints()
				}
				if decision.Action != policy.ActionAskApproval || !reflect.DeepEqual(decision.Constraints, want) || decision.Metadata["risk_class"] != riskClassPathEscape {
					t.Fatalf("escalated=%v: %#v, want path approval with original execution route", escalated, decision)
				}
			}
		})
	}
}

func TestPowerShellDeleteAliasesAllowWorkspaceCleanup(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"del", "erase", "rd", "rmdir"} {
		t.Run(name, func(t *testing.T) {
			for _, command := range []string{
				name + " -Recurse -Force ./build",
				name + " -Force $HOME/caelis-delete-policy-outside",
				name + " -Force ./build; Write-Output -Recurse",
			} {
				decision, err := WorkspaceWriteMode().DecideTool(context.Background(), commandCtx(command, false))
				if err != nil {
					t.Fatal(err)
				}
				if decision.Action != policy.ActionAllow {
					t.Fatalf("command=%q: %#v, want allow", command, decision)
				}
			}
		})
	}
}
