package cli

import (
	"context"
	"errors"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
	"github.com/OnslaughtSnail/caelis/internal/version"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/app"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/gatewaydriver/local"
)

func runTUI(ctx context.Context, stack *gatewayapp.Stack, sessionID string, modelText string, stdin io.Reader, stdout io.Writer) error {
	driver, err := local.NewLocalDriver(ctx, stack, strings.TrimSpace(sessionID), "cli-tui", strings.TrimSpace(modelText))
	if err != nil {
		return err
	}
	programCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sender := &tuiapp.ProgramSender{}
	cfg := tuiapp.ConfigFromDriver(driver, sender, tuiapp.Config{
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
			startTUISandboxPreflight(programCtx, stack, sender)
		},
	})
	model := tuiapp.NewModel(cfg)
	program := tea.NewProgram(model, tuiProgramOptions(stdin, stdout, programCtx, cfg.RenderFPS)...)
	sender.Send = program.Send
	defer sender.Close()
	_, err = program.Run()
	return err
}

func startTUISandboxPreflight(ctx context.Context, stack *gatewayapp.Stack, sender *tuiapp.ProgramSender) {
	if stack == nil || sender == nil {
		return
	}
	status := stack.SandboxStatus()
	if !isWindowsSandboxStatus(status) {
		return
	}
	go func() {
		const source = "sandbox-preflight"
		progressCtx := sandbox.ContextWithPrepareProgress(ctx, func(progress sandbox.PrepareProgress) {
			sender.SendMsg(tuiapp.SandboxProgressMsg{
				Title:   "Windows sandbox",
				Source:  source,
				Phase:   progress.Phase,
				Message: progress.Message,
				Step:    progress.Step,
				Total:   progress.Total,
				Done:    progress.Done,
			})
		})
		next, err := stack.PreflightSandbox(progressCtx, true)
		sender.SendMsg(tuiapp.SandboxProgressMsg{Source: source, Clear: true})
		if err == nil || errors.Is(err, context.Canceled) {
			return
		}
		sender.SendMsg(tuiapp.LogChunkMsg{Chunk: formatTUISandboxPreflightError(next, err) + "\n"})
	}()
}

func isWindowsSandboxStatus(status gatewayapp.SandboxStatus) bool {
	for _, value := range []string{status.ResolvedBackend, status.RequestedBackend} {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "windows", "windows-restricted-token", "windows_restricted_token", "windows_elevated", "windows-elevated", "elevated":
			return true
		}
	}
	return false
}

func formatTUISandboxPreflightError(status gatewayapp.SandboxStatus, err error) string {
	_ = err
	lines := []string{"Windows sandbox ACL repair failed."}
	if root := strings.TrimSpace(status.WorkspaceSetupRoot); root != "" {
		lines = append(lines, "workspace: "+root)
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
