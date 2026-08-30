package cli

import (
	"context"
	"errors"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/workspacetrust"
	tuiapp "github.com/caelis-labs/caelis/surfaces/tui/app"
)

type workspaceTrustConfigurationClient interface {
	SetWorkspaceTrust(context.Context, appserver.WorkspaceTrustRequest) (appserver.CommandResult, error)
}

type workspaceTrustPromptRequest struct {
	Workspace string
	SaveIssue string
}

type workspaceTrustPromptFunc func(context.Context, workspaceTrustPromptRequest) (workspacetrust.Level, bool, error)

func ensureTUIWorkspaceTrust(
	ctx context.Context,
	status appserver.StatusClient,
	configuration workspaceTrustConfigurationClient,
	workspaceKey string,
	workspaceDir string,
	prompt workspaceTrustPromptFunc,
) (bool, error) {
	if status == nil || configuration == nil || prompt == nil {
		return false, errors.New("workspace trust services are unavailable")
	}
	request := appserver.StatusRequest{
		WorkspaceKey: strings.TrimSpace(workspaceKey),
		CWD:          strings.TrimSpace(workspaceDir),
		Surface:      "cli-tui",
	}
	saveIssue := ""
	attemptedDecision := workspacetrust.Unknown
	for {
		snapshot, err := status.SessionStatus(ctx, request)
		if err != nil {
			return false, err
		}
		persistedDecision := snapshot.Configuration.WorkspaceTrust
		if persistedDecision.Decided() {
			if !attemptedDecision.Decided() || persistedDecision == attemptedDecision {
				return true, nil
			}
			saveIssue = "Workspace trust changed while this choice was being saved. Please review the options again."
		}
		decision, selected, err := prompt(ctx, workspaceTrustPromptRequest{
			Workspace: request.CWD,
			SaveIssue: saveIssue,
		})
		if err != nil {
			return false, err
		}
		if !selected {
			return false, nil
		}
		attemptedDecision = decision
		result, saveErr := configuration.SetWorkspaceTrust(ctx, appserver.WorkspaceTrustRequest{
			WriteBase: appserver.WriteBase{
				OperationID:      uuid.NewString(),
				ExpectedRevision: &snapshot.Configuration.Revision,
			},
			WorkspaceKey: request.WorkspaceKey,
			CWD:          request.CWD,
			TrustLevel:   decision,
		})
		if saveErr == nil && result.Outcome == appserver.OutcomeCommitted {
			return true, nil
		}
		saveIssue = workspaceTrustSaveIssue(result, saveErr)
	}
}

func workspaceTrustSaveIssue(result appserver.CommandResult, err error) string {
	if result.Outcome == appserver.OutcomeConflicted {
		return "Settings changed while this choice was being saved. Please review the options again."
	}
	if err != nil {
		return "Caelis could not save this choice. Check that the configuration file is available, then try again."
	}
	return "Caelis could not confirm this choice. Please review the options and try again."
}

func runWorkspaceTrustPrompt(
	ctx context.Context,
	request workspaceTrustPromptRequest,
	options tuiOptions,
	stdin io.Reader,
	stdout io.Writer,
) (workspacetrust.Level, bool, error) {
	responses := make(chan tuiapp.PromptResponse, 1)
	prompt := workspaceTrustPromptMessage(request, responses)
	model := tuiapp.NewModel(tuiapp.Config{
		Context:       ctx,
		AppName:       "CAELIS",
		Workspace:     request.Workspace,
		InitialPrompt: &prompt,
		NoAnimation:   options.NoAnimation,
	})
	program := tea.NewProgram(model, tuiProgramOptions(stdin, stdout, ctx, 0)...)
	selection := make(chan tuiapp.PromptResponse, 1)
	done := make(chan struct{})
	go func() {
		select {
		case response := <-responses:
			selection <- response
			program.Quit()
		case <-done:
		}
	}()
	_, runErr := program.Run()
	close(done)
	if runErr != nil {
		return workspacetrust.Unknown, false, runErr
	}
	select {
	case response := <-selection:
		if response.Err != nil {
			if response.Err.Error() == tuiapp.PromptErrInterrupt || response.Err.Error() == tuiapp.PromptErrEOF {
				return workspacetrust.Unknown, false, nil
			}
			return workspacetrust.Unknown, false, response.Err
		}
		decision := workspacetrust.Level(strings.TrimSpace(response.Line))
		if !decision.Decided() {
			return workspacetrust.Unknown, false, errors.New("workspace trust prompt returned an invalid choice")
		}
		return decision, true, nil
	default:
		if err := ctx.Err(); err != nil {
			return workspacetrust.Unknown, false, err
		}
		return workspacetrust.Unknown, false, nil
	}
}

func workspaceTrustPromptMessage(
	request workspaceTrustPromptRequest,
	responses chan tuiapp.PromptResponse,
) tuiapp.PromptRequestMsg {
	details := []tuiapp.PromptDetail{
		{Label: "Workspace", Value: request.Workspace, Emphasis: true},
		{
			Label: "Project MCP configuration",
			Value: "If trusted, Caelis can start local programs and connect to external services defined by this workspace.",
		},
		{
			Label: "When this takes effect",
			Value: "The choice applies when a Runtime is created. Active Runtimes are not changed.",
		},
	}
	if issue := strings.TrimSpace(request.SaveIssue); issue != "" {
		details = append(details, tuiapp.PromptDetail{Label: "Unable to save", Value: issue})
	}
	return tuiapp.PromptRequestMsg{
		Title:   "Trust this workspace?",
		Details: details,
		Choices: []tuiapp.PromptChoice{
			{
				Label:  "Trust and continue",
				Value:  string(workspacetrust.Trusted),
				Detail: "Allow project MCP configuration for this workspace.",
			},
			{
				Label:  "Continue without trust",
				Value:  string(workspacetrust.Untrusted),
				Detail: "Ignore project MCP configuration for this workspace.",
			},
		},
		DefaultChoice: string(workspacetrust.Untrusted),
		Response:      responses,
	}
}
