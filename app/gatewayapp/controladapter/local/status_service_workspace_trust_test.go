package local

import (
	"context"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	controladapter "github.com/caelis-labs/caelis/app/gatewayapp/controladapter"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/workspacetrust"
)

func TestStatusServiceProjectsWorkspaceTrustRequirementOnlyWhenRequested(t *testing.T) {
	projectMCPProbes := 0
	service, err := newStatusService(statusServiceDeps{
		hostDeps: &controladapter.StatusAssemblyDeps{
			Session: controladapter.SessionRuntimeDeps{
				Workspace: session.WorkspaceRef{Key: "project", CWD: "/tmp/project"},
			},
			Status: controladapter.StatusRuntimeDeps{
				WorkspaceTrustFn: func(context.Context, string) (workspacetrust.Level, error) {
					return workspacetrust.Unknown, nil
				},
				ProjectMCPConfigurationPresentFn: func(context.Context, string) (bool, error) {
					projectMCPProbes++
					return true, nil
				},
			},
		},
		acquireRuntime: func(context.Context, appserver.Principal, appserver.Action, string, bool) (*gatewayapp.ControlRuntimeLease, error) {
			t.Fatal("host workspace status acquired a Session Runtime")
			return nil, nil
		},
		resolveWorkspace: func(workspace session.WorkspaceRef) (session.WorkspaceRef, error) {
			return workspace, nil
		},
		sandboxStatusForWorkspace: func(session.WorkspaceRef) gatewayapp.SandboxStatus {
			return gatewayapp.SandboxStatus{}
		},
		doctorForWorkspace: func(context.Context, session.WorkspaceRef, gatewayapp.DoctorRequest) (gatewayapp.DoctorReport, error) {
			return gatewayapp.DoctorReport{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	principal := appserver.Principal{ID: "owner"}
	address := appserver.StatusRequest{WorkspaceKey: "project", CWD: "/tmp/project", Surface: "cli-tui"}
	ordinary, err := service.SessionStatus(context.Background(), principal, address)
	if err != nil {
		t.Fatal(err)
	}
	if projectMCPProbes != 0 || ordinary.Configuration.WorkspaceTrustRequired {
		t.Fatalf("ordinary status probes/requirement = %d/%v", projectMCPProbes, ordinary.Configuration.WorkspaceTrustRequired)
	}

	address.IncludeWorkspaceTrustRequirement = true
	preflight, err := service.SessionStatus(context.Background(), principal, address)
	if err != nil {
		t.Fatal(err)
	}
	if projectMCPProbes != 1 || !preflight.Configuration.WorkspaceTrustRequired {
		t.Fatalf("preflight status probes/requirement = %d/%v", projectMCPProbes, preflight.Configuration.WorkspaceTrustRequired)
	}
}
