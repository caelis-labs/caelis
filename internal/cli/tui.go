package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
	"github.com/OnslaughtSnail/caelis/app/gatewayapp/controladapter/local"
	"github.com/OnslaughtSnail/caelis/internal/version"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/app"
)

func runTUI(ctx context.Context, stack *gatewayapp.Stack, sessionID string, modelText string, stdin io.Reader, stdout io.Writer) error {
	driver, err := local.NewLocalAdapter(ctx, stack, strings.TrimSpace(sessionID), "cli-tui", strings.TrimSpace(modelText))
	if err != nil {
		return err
	}
	programCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sender := &tuiapp.ProgramSender{}
	cfg := tuiapp.ConfigFromControlService(driver, sender, tuiapp.Config{
		Context:         programCtx,
		AppName:         "CAELIS",
		Version:         version.String(),
		Workspace:       stack.Workspace.CWD,
		ModelAlias:      modelText,
		ShowWelcomeCard: true,
		Commands:        tuiapp.DefaultCommands(),
		Wizards:         tuiapp.DefaultWizards(),
		RenderFPS:       envInt("CAELIS_TUI_RENDER_FPS", 0),
		OnStart: func() {
			startTUISandboxRefresh(programCtx, stack, sender)
		},
	})
	model := tuiapp.NewModel(cfg)
	program := tea.NewProgram(model, tuiProgramOptions(stdin, stdout, programCtx, cfg.RenderFPS)...)
	sender.Send = program.Send
	defer sender.Close()
	_, err = program.Run()
	return err
}

func startTUISandboxRefresh(ctx context.Context, stack *gatewayapp.Stack, sender *tuiapp.ProgramSender) {
	if stack == nil || sender == nil {
		return
	}
	go func() {
		refreshCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		if err := stack.RefreshSandbox(refreshCtx); err != nil && !errors.Is(err, context.Canceled) {
			sender.SendMsg(tuiapp.LogChunkMsg{Chunk: formatTUISandboxRefreshError(err) + "\n"})
		}
	}()
}

func formatTUISandboxRefreshError(err error) string {
	lines := []string{"Windows sandbox background refresh failed."}
	if errText := strings.TrimSpace(err.Error()); errText != "" {
		lines = append(lines, errText)
	}
	lines = append(lines, "run /doctor fix")
	return strings.Join(lines, "\n")
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
