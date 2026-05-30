package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"

	applocal "github.com/OnslaughtSnail/caelis/internal/app/local"
	"github.com/OnslaughtSnail/caelis/internal/version"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/app"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/gatewaydriver"
)

func runTUI(ctx context.Context, stack *applocal.Stack, sessionID string, modelText string, stdin io.Reader, stdout io.Writer) error {
	driver, err := newCoreTUIDriver(ctx, stack, strings.TrimSpace(sessionID), "cli-tui", strings.TrimSpace(modelText))
	if err != nil {
		return err
	}
	programCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sender := &tuiapp.ProgramSender{}
	runtimeCfg := stack.Runtime()
	cfg := tuiapp.ConfigFromDriver(driver, sender, tuiapp.Config{
		Context:         programCtx,
		AppName:         "CAELIS",
		Version:         version.String(),
		Workspace:       runtimeCfg.WorkspaceCWD,
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

func newCoreTUIDriver(ctx context.Context, stack *applocal.Stack, sessionID string, bindingKey string, modelText string) (*gatewaydriver.GatewayDriver, error) {
	if stack == nil {
		return nil, fmt.Errorf("cli: core TUI stack is required")
	}
	driverStack := gatewaydriver.BindAppServices(&gatewaydriver.DriverStack{}, stack.Services())
	return gatewaydriver.NewGatewayDriver(ctx, driverStack, strings.TrimSpace(sessionID), strings.TrimSpace(bindingKey), strings.TrimSpace(modelText))
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
