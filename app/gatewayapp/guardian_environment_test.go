package gatewayapp

import (
	"runtime"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

func TestGuardianEnvironmentMatchesPlatformCapabilities(t *testing.T) {
	for _, goos := range []string{"windows", "linux", "darwin"} {
		for _, network := range []sandbox.Network{sandbox.NetworkEnabled, sandbox.NetworkDisabled} {
			t.Run(goos+"/"+string(network), func(t *testing.T) {
				text := guardianEnvironmentContextForOS(goos, network)
				if goos == "windows" {
					for _, want := range []string{"Windows PowerShell", "$env:TMPDIR", "<network>enabled; Windows does not enforce network restrictions</network>"} {
						if !strings.Contains(text, want) {
							t.Fatalf("environment missing %q: %s", want, text)
						}
					}
				} else {
					if strings.Contains(text, "Windows") || strings.Contains(text, "PowerShell") || strings.Contains(text, "$env:") {
						t.Fatalf("Windows capabilities leaked into %s: %s", goos, text)
					}
					if !strings.Contains(text, "<network>"+string(network)+"</network>") {
						t.Fatalf("network intent changed: %s", text)
					}
				}
			})
		}
	}
	if strings.Contains(guardianPolicyPrompt(), "PowerShell") || strings.Contains((guardianQueryTool{name: "RunCommand"}).Definition().Description, "PowerShell") {
		t.Fatal("platform capability escaped the environment context")
	}
}

func TestGuardianEnvironmentReachesModelWithoutOpeningSandbox(t *testing.T) {
	service, active := newApprovalReviewerTestSession(t, t.Context())
	llm := &approvalReviewerFakeModel{responses: []string{`{"option_id":"allow_once"}`}}
	reviewer := newGuardianApprovalApprover(service)
	reviewer.queryNetwork = sandbox.NetworkDisabled
	result, err := reviewer.Decide(t.Context(), approvalReviewerTestRequest(active, llm, "inspect", nil))
	if err != nil || !result.Approved {
		t.Fatalf("decision=%+v error=%v", result, err)
	}
	requests := llm.Requests()
	if len(requests) != 1 {
		t.Fatalf("provider calls=%d", len(requests))
	}
	text := model.NewMessage(model.RoleSystem, requests[0].Instructions...).TextContent()
	if !strings.Contains(text, guardianEnvironmentContextForOS(runtime.GOOS, sandbox.NetworkDisabled)) {
		t.Fatalf("model instructions omitted environment: %s", text)
	}
}
