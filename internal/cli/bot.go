package cli

import (
	"context"
	"errors"
	"io"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/internal/controlprompt/appserveradapter"
	"github.com/caelis-labs/caelis/internal/version"
	tuiapp "github.com/caelis-labs/caelis/surfaces/tui/app"
)

// runBot launches the standalone Bot TUI. It shares the managed/attached Host
// AppServer and the ordinary Session path with the coding TUI; only the command
// set, Bot configuration, and hidden workspace chrome differ. Bot conversations
// need no project workspace trust, so no trust prompt runs.
func runBot(
	ctx context.Context,
	product *productClients,
	options tuiOptions,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) error {
	if product == nil {
		return errors.New("cli: product clients are unavailable")
	}
	if product.Clients.Bots == nil {
		return errors.New("cli: Bot client is unavailable on this Host")
	}
	sender := &tuiapp.ProgramSender{}
	typedDriver, err := appserveradapter.NewAppServerAdapter(appserveradapter.AppServerAdapterConfig{
		WorkspaceKey:   product.Workspace.WorkspaceKey,
		WorkspaceDir:   product.Workspace.WorkspaceCWD,
		Surface:        "cli-bot",
		Sessions:       product.Clients.Sessions,
		Participants:   product.Clients.Participants,
		SubagentInputs: product.Clients.SubagentInputs,
		Status:         product.Clients.Status,
		Configuration:  product.Clients.Configuration,
		Agents:         product.Clients.Agents,
		Completion:     product.Clients.Completion,
		Plugins:        product.Clients.Plugins,
		// A Bot conversation must be selected before any input; never allocate
		// an implicit workspace Session for Bot mode.
		RequireExistingSession: true,
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
		AppName:             "CAELIS",
		Version:             version.String(),
		ShowWelcomeCard:     false,
		Commands:            tuiapp.BotCommands(),
		CommandDetails:      tuiapp.BotCommandDetails(),
		Wizards:             tuiapp.DefaultWizards(),
		PromptRouterFactory: controlprompt.New,
		Bot:                 &tuiapp.BotSurface{Client: product.Clients.Bots},
		UIPreferences:       product.Clients.UIPreferences,
		RenderFPS:           envInt("CAELIS_TUI_RENDER_FPS", 0),
		NoAnimation:         options.NoAnimation,
		OnStart: func() {
			startTUIUpdateCheck(programCtx, product.Workspace.StoreDir, sender)
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
	cancel()
	sender.Close()
	if err != nil {
		return err
	}
	if updateRequested {
		return runUpdate(ctx, product.Workspace.StoreDir, false, stdout, stderr)
	}
	return nil
}
