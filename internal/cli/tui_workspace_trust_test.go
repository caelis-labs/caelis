package cli

import (
	"context"
	"strings"
	"testing"

	appserver "github.com/caelis-labs/caelis/control/appserver"
	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/control/workspacetrust"
	tuiapp "github.com/caelis-labs/caelis/surfaces/tui/app"
)

type workspaceTrustStatusProbe struct {
	snapshots []controlstatus.StatusSnapshot
	requests  []appserver.StatusRequest
}

func (p *workspaceTrustStatusProbe) SessionStatus(_ context.Context, request appserver.StatusRequest) (controlstatus.StatusSnapshot, error) {
	p.requests = append(p.requests, request)
	if len(p.snapshots) == 0 {
		return controlstatus.StatusSnapshot{}, nil
	}
	snapshot := p.snapshots[0]
	p.snapshots = p.snapshots[1:]
	return snapshot, nil
}

type workspaceTrustConfigurationProbe struct {
	requests []appserver.WorkspaceTrustRequest
	results  []appserver.CommandResult
}

func (p *workspaceTrustConfigurationProbe) SetWorkspaceTrust(_ context.Context, request appserver.WorkspaceTrustRequest) (appserver.CommandResult, error) {
	p.requests = append(p.requests, request)
	if len(p.results) == 0 {
		return appserver.CommandResult{Outcome: appserver.OutcomeCommitted}, nil
	}
	result := p.results[0]
	p.results = p.results[1:]
	return result, nil
}

func TestEnsureTUIWorkspaceTrustPersistsFirstDecision(t *testing.T) {
	status := &workspaceTrustStatusProbe{snapshots: []controlstatus.StatusSnapshot{{
		Configuration: controlstatus.StatusConfiguration{Revision: 17, WorkspaceTrust: workspacetrust.Unknown},
	}}}
	configuration := &workspaceTrustConfigurationProbe{}
	promptCalls := 0
	proceed, err := ensureTUIWorkspaceTrust(
		context.Background(), status, configuration, "project", "/tmp/project",
		func(_ context.Context, request workspaceTrustPromptRequest) (workspacetrust.Level, bool, error) {
			promptCalls++
			if request.Workspace != "/tmp/project" {
				t.Fatalf("prompt workspace = %q", request.Workspace)
			}
			return workspacetrust.Trusted, true, nil
		},
	)
	if err != nil || !proceed {
		t.Fatalf("ensureTUIWorkspaceTrust() = (%v, %v)", proceed, err)
	}
	if promptCalls != 1 || len(configuration.requests) != 1 {
		t.Fatalf("prompt calls = %d, writes = %d", promptCalls, len(configuration.requests))
	}
	request := configuration.requests[0]
	if request.ExpectedRevision == nil || *request.ExpectedRevision != 17 || request.TrustLevel != workspacetrust.Trusted || request.WorkspaceKey != "project" || request.CWD != "/tmp/project" {
		t.Fatalf("workspace trust request = %#v", request)
	}
}

func TestEnsureTUIWorkspaceTrustUsesPersistedUntrustedDecision(t *testing.T) {
	status := &workspaceTrustStatusProbe{snapshots: []controlstatus.StatusSnapshot{{
		Configuration: controlstatus.StatusConfiguration{Revision: 18, WorkspaceTrust: workspacetrust.Untrusted},
	}}}
	configuration := &workspaceTrustConfigurationProbe{}
	proceed, err := ensureTUIWorkspaceTrust(
		context.Background(), status, configuration, "project", "/tmp/project",
		func(context.Context, workspaceTrustPromptRequest) (workspacetrust.Level, bool, error) {
			t.Fatal("prompt was shown for an explicit untrusted decision")
			return workspacetrust.Unknown, false, nil
		},
	)
	if err != nil || !proceed || len(configuration.requests) != 0 {
		t.Fatalf("ensureTUIWorkspaceTrust() = (%v, %v), writes = %d", proceed, err, len(configuration.requests))
	}
}

func TestEnsureTUIWorkspaceTrustRequiresConfirmationWhenConcurrentDecisionDiffers(t *testing.T) {
	status := &workspaceTrustStatusProbe{snapshots: []controlstatus.StatusSnapshot{
		{Configuration: controlstatus.StatusConfiguration{Revision: 20, WorkspaceTrust: workspacetrust.Unknown}},
		{Configuration: controlstatus.StatusConfiguration{Revision: 21, WorkspaceTrust: workspacetrust.Trusted}},
	}}
	configuration := &workspaceTrustConfigurationProbe{results: []appserver.CommandResult{
		{Outcome: appserver.OutcomeConflicted},
		{Outcome: appserver.OutcomeCommitted},
	}}
	promptCalls := 0
	proceed, err := ensureTUIWorkspaceTrust(
		context.Background(), status, configuration, "project", "/tmp/project",
		func(_ context.Context, request workspaceTrustPromptRequest) (workspacetrust.Level, bool, error) {
			promptCalls++
			if promptCalls == 2 {
				if !strings.Contains(request.SaveIssue, "Workspace trust changed") {
					t.Fatalf("second prompt issue = %q", request.SaveIssue)
				}
				return workspacetrust.Trusted, true, nil
			}
			return workspacetrust.Untrusted, true, nil
		},
	)
	if err != nil || !proceed || promptCalls != 2 || len(status.requests) != 2 || len(configuration.requests) != 2 {
		t.Fatalf("ensureTUIWorkspaceTrust() = (%v, %v), prompts = %d, reads = %d", proceed, err, promptCalls, len(status.requests))
	}
	if configuration.requests[1].TrustLevel != workspacetrust.Trusted || configuration.requests[1].ExpectedRevision == nil || *configuration.requests[1].ExpectedRevision != 21 {
		t.Fatalf("second workspace trust request = %#v", configuration.requests[1])
	}
}

func TestWorkspaceTrustPromptUsesProductCopy(t *testing.T) {
	prompt := workspaceTrustPromptMessage(workspaceTrustPromptRequest{
		Workspace: "/tmp/project",
		SaveIssue: "Caelis could not save this choice. Please try again.",
	}, make(chan tuiapp.PromptResponse, 1))
	if prompt.Title != "Trust this workspace?" || prompt.DefaultChoice != string(workspacetrust.Untrusted) {
		t.Fatalf("prompt heading/default = %q/%q", prompt.Title, prompt.DefaultChoice)
	}
	var copy strings.Builder
	for _, detail := range prompt.Details {
		copy.WriteString(detail.Label)
		copy.WriteString(" ")
		copy.WriteString(detail.Value)
		copy.WriteString(" ")
	}
	for _, choice := range prompt.Choices {
		copy.WriteString(choice.Label)
		copy.WriteString(" ")
		copy.WriteString(choice.Detail)
		copy.WriteString(" ")
	}
	for _, want := range []string{"Project MCP configuration", "start local programs", "Runtime is created", "Trust and continue", "Continue without trust", "Unable to save"} {
		if !strings.Contains(copy.String(), want) {
			t.Fatalf("prompt copy = %q, want %q", copy.String(), want)
		}
	}
}
