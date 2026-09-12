package cli

import (
	"context"
	"io"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/workspacetrust"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/internal/controlprompt/appserveradapter"
	"github.com/caelis-labs/caelis/internal/updater"
	"github.com/caelis-labs/caelis/internal/version"
	tuiapp "github.com/caelis-labs/caelis/surfaces/tui/app"
)

const tuiBackgroundUpdateCheckTimeout = 2 * time.Minute

type tuiOptions struct {
	NoAnimation                bool
	DangerouslySkipPermissions bool
}

func runTUI(
	ctx context.Context,
	clients appserver.AppServerClients,
	sessionID string,
	workspaceKey string,
	workspaceDir string,
	storeDir string,
	modelText string,
	options tuiOptions,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) error {
	proceed, err := ensureTUIWorkspaceTrust(
		ctx,
		clients.Status,
		clients.Configuration,
		workspaceKey,
		workspaceDir,
		func(ctx context.Context, request workspaceTrustPromptRequest) (workspacetrust.Level, bool, error) {
			return runWorkspaceTrustPrompt(ctx, request, options, stdin, stdout)
		},
	)
	if err != nil {
		return err
	}
	if !proceed {
		return nil
	}
	sender := &tuiapp.ProgramSender{}
	typedDriver, err := appserveradapter.NewAppServerAdapter(appserveradapter.AppServerAdapterConfig{
		WorkspaceKey:  strings.TrimSpace(workspaceKey),
		WorkspaceDir:  strings.TrimSpace(workspaceDir),
		Surface:       "cli-tui",
		Sessions:      clients.Sessions,
		Participants:  clients.Participants,
		Status:        clients.Status,
		Configuration: clients.Configuration,
		Agents:        clients.Agents,
		Completion:    clients.Completion,
		Plugins:       clients.Plugins,
	})
	if err != nil {
		return err
	}
	defer typedDriver.Close()
	programCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	updateRequested := false
	tuiCfg := tuiapp.ConfigFromControlService(typedDriver, sender, tuiapp.Config{
		Context:             programCtx,
		InitialSessionID:    strings.TrimSpace(sessionID),
		AppName:             "CAELIS",
		Version:             version.String(),
		Workspace:           workspaceDir,
		ModelAlias:          modelText,
		ShowWelcomeCard:     true,
		FullAccessMode:      options.DangerouslySkipPermissions,
		Commands:            tuiapp.DefaultCommands(),
		Wizards:             tuiapp.DefaultWizards(),
		PromptRouterFactory: controlprompt.New,
		RenderFPS:           envInt("CAELIS_TUI_RENDER_FPS", 0),
		NoAnimation:         options.NoAnimation,
		SubagentInputs:      clients.SubagentInputs, UIPreferences: clients.UIPreferences,
		TaskStreams: clients.Tasks,
		OnStart: func() {
			startTUISandboxRefresh(programCtx, typedDriver)
			startTUIUpdateCheck(programCtx, storeDir, sender)
		},
		OnUpdateRequested: func() {
			updateRequested = true
		},
	})
	model := tuiapp.NewModel(tuiCfg)
	program := tea.NewProgram(model, tuiProgramOptions(stdin, stdout, programCtx, tuiCfg.RenderFPS)...)
	sender.Send = program.Send
	defer sender.Close()
	_, err = program.Run()
	// Stop background completion, status, and stream work before waiting for
	// ProgramSender forwarders. Defers remain as idempotent safety nets.
	cancel()
	sender.Close()
	if err != nil {
		return err
	}
	if updateRequested {
		return runUpdate(ctx, storeDir, false, stdout, stderr)
	}
	return nil
}

type tuiSandboxRefresher interface {
	RefreshSandbox(context.Context) error
}

func startTUISandboxRefresh(ctx context.Context, service tuiSandboxRefresher) {
	if service == nil {
		return
	}
	go runTUISandboxRefresh(ctx, service)
}

// runTUISandboxRefresh is best-effort Host maintenance. Failures stay in Host
// diagnostics; this path must not emit a TUI hint or write stdout/stderr.
func runTUISandboxRefresh(ctx context.Context, service tuiSandboxRefresher) {
	if service == nil {
		return
	}
	refreshCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	_ = service.RefreshSandbox(refreshCtx)
}

func startTUIUpdateCheck(ctx context.Context, storeDir string, sender *tuiapp.ProgramSender) {
	if sender == nil {
		return
	}
	go func() {
		checkCtx, cancel := context.WithTimeout(ctx, tuiBackgroundUpdateCheckTimeout)
		defer cancel()
		cfg := updateConfig(storeDir)
		result, err := checkUpdateOperation(checkCtx, cfg, updater.CheckOptions{Auto: true})
		if err != nil {
			return
		}
		manager := updater.New(cfg)
		sender.SendMsg(tuiapp.UpdateCheckResultMsg{
			LatestVersion: result.LatestVersion,
			Eligible:      manager.HintEligible(result),
		})
	}()
}

func tuiProgramOptions(stdin io.Reader, stdout io.Writer, ctx context.Context, fps int) []tea.ProgramOption {
	options := []tea.ProgramOption{
		tea.WithInput(stdin),
		tea.WithOutput(stdout),
		tea.WithContext(ctx),
	}
	if fps > 0 {
		options = append(options, tea.WithFPS(fps))
	}
	return options
}
