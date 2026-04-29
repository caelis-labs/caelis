package main

import (
	"context"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
	tuiadapterruntime "github.com/OnslaughtSnail/caelis/gateway/adapter/tui/runtime"
	tuiapp "github.com/OnslaughtSnail/caelis/tui/tuiapp"
)

func runTUI(ctx context.Context, stack *gatewayapp.Stack, sessionID string, modelText string, stdin io.Reader, stdout io.Writer) error {
	driver, err := tuiadapterruntime.NewGatewayDriver(ctx, stack, strings.TrimSpace(sessionID), "cli-tui", strings.TrimSpace(modelText))
	if err != nil {
		return err
	}
	programCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sender := &tuiapp.ProgramSender{}
	cfg := tuiapp.ConfigFromDriver(driver, sender, tuiapp.Config{
		Context:         programCtx,
		AppName:         "CAELIS",
		Version:         envOr("CAELIS_VERSION", "dev"),
		Workspace:       stack.Workspace.CWD,
		ModelAlias:      modelText,
		ShowWelcomeCard: true,
		Commands:        tuiapp.DefaultCommands(),
		Wizards:         tuiapp.DefaultWizards(),
		RenderFPS:       envInt("CAELIS_TUI_RENDER_FPS", 0),
	})
	model := tuiapp.NewModel(cfg)
	program := tea.NewProgram(model, tuiProgramOptions(stdin, stdout, programCtx, cfg.RenderFPS)...)
	sender.Send = program.Send
	defer sender.Close()
	_, err = program.Run()
	return err
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
