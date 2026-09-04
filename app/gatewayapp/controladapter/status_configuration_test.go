package controladapter

import (
	"context"
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/workspacetrust"
)

func TestStatusProjectsCanonicalConfigurationRevision(t *testing.T) {
	driver := NewStatusAssemblerForHost(StatusAssemblyDeps{Status: StatusRuntimeDeps{
		ConfigurationRevisionFn: func(context.Context) (uint64, error) { return 42, nil },
	}}, "test", "")
	status, err := driver.LightweightStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Configuration.Revision != 42 {
		t.Fatalf("configuration revision = %d, want 42", status.Configuration.Revision)
	}
}

func TestStatusProjectsWorkspaceTrustForAddressedWorkspace(t *testing.T) {
	var gotWorkspace string
	driver := NewStatusAssemblerForHost(StatusAssemblyDeps{
		Session: SessionRuntimeDeps{Workspace: session.WorkspaceRef{Key: "project", CWD: "/tmp/project"}},
		Status: StatusRuntimeDeps{
			WorkspaceTrustFn: func(_ context.Context, workspace string) (workspacetrust.Level, error) {
				gotWorkspace = workspace
				return workspacetrust.Untrusted, nil
			},
		},
	}, "test", "")
	status, err := driver.LightweightStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotWorkspace != "/tmp/project" || status.Configuration.WorkspaceTrust != workspacetrust.Untrusted {
		t.Fatalf("workspace/trust = %q/%q", gotWorkspace, status.Configuration.WorkspaceTrust)
	}
}

func TestStatusRequiresWorkspaceTrustOnlyForProjectMCP(t *testing.T) {
	var gotWorkspace string
	driver := NewStatusAssemblerForHost(StatusAssemblyDeps{
		Session: SessionRuntimeDeps{Workspace: session.WorkspaceRef{Key: "project", CWD: "/tmp/project"}},
		Status: StatusRuntimeDeps{
			WorkspaceTrustFn: func(context.Context, string) (workspacetrust.Level, error) {
				return workspacetrust.Unknown, nil
			},
			ProjectMCPConfigurationPresentFn: func(_ context.Context, workspace string) (bool, error) {
				gotWorkspace = workspace
				return true, nil
			},
		},
	}, "test", "")
	status, err := driver.LightweightStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotWorkspace != "/tmp/project" || !status.Configuration.WorkspaceTrustRequired {
		t.Fatalf("workspace/required = %q/%v", gotWorkspace, status.Configuration.WorkspaceTrustRequired)
	}
}

func TestStatusSkipsProjectMCPProbeAfterWorkspaceDecision(t *testing.T) {
	driver := NewStatusAssemblerForHost(StatusAssemblyDeps{
		Session: SessionRuntimeDeps{Workspace: session.WorkspaceRef{Key: "project", CWD: "/tmp/project"}},
		Status: StatusRuntimeDeps{
			WorkspaceTrustFn: func(context.Context, string) (workspacetrust.Level, error) {
				return workspacetrust.Trusted, nil
			},
			ProjectMCPConfigurationPresentFn: func(context.Context, string) (bool, error) {
				t.Fatal("project MCP presence was probed after trust was decided")
				return false, nil
			},
		},
	}, "test", "")
	status, err := driver.LightweightStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Configuration.WorkspaceTrustRequired {
		t.Fatal("workspace trust required after decision")
	}
}

func TestStatusPropagatesConfigurationRevisionReadFailure(t *testing.T) {
	fault := errors.New("config read failed")
	driver := NewStatusAssemblerForHost(StatusAssemblyDeps{Status: StatusRuntimeDeps{
		ConfigurationRevisionFn: func(context.Context) (uint64, error) { return 0, fault },
	}}, "test", "")
	if _, err := driver.LightweightStatus(context.Background()); !errors.Is(err, fault) {
		t.Fatalf("LightweightStatus() error = %v, want %v", err, fault)
	}
}

func TestStatusProjectsEffectiveProcessModelWithoutSession(t *testing.T) {
	driver := NewStatusAssemblerForHost(StatusAssemblyDeps{Model: ModelRuntimeDeps{
		EffectiveAliasFn:    func() string { return "openai-codex/gpt-5.6-sol" },
		EffectiveEffortFn:   func() string { return "xhigh" },
		EffectiveFastModeFn: func() bool { return true },
	}}, "test", "")
	status, err := driver.LightweightStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Session.ID != "" || status.ModelStatus.Display != "openai-codex/gpt-5.6-sol [xhigh]" ||
		status.ModelStatus.ReasoningEffort != "xhigh" || !status.ModelStatus.FastMode {
		t.Fatalf("Host effective model status = %#v", status)
	}
}
