package cli

import (
	"context"
	"io"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
	"github.com/OnslaughtSnail/caelis/internal/version"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/app"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/gatewaydriver/local"
)

func runTUI(ctx context.Context, stack *gatewayapp.Stack, sessionID string, modelText string, stdin io.Reader, stdout io.Writer) error {
	initialSandboxStatus := stack.SandboxStatus()
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
		InitialLogs:     initialSandboxStatusLogs(initialSandboxStatus),
		Commands:        tuiapp.DefaultCommands(),
		Wizards:         tuiapp.DefaultWizards(),
		RenderFPS:       envInt("CAELIS_TUI_RENDER_FPS", 0),
		OnStart: func() {
			startWorkspaceSandboxPreflight(programCtx, stack, sender, initialSandboxStatus)
		},
	})
	model := tuiapp.NewModel(cfg)
	program := tea.NewProgram(model, tuiProgramOptions(stdin, stdout, programCtx, cfg.RenderFPS)...)
	sender.Send = program.Send
	defer sender.Close()
	_, err = program.Run()
	return err
}

func startWorkspaceSandboxPreflight(ctx context.Context, stack *gatewayapp.Stack, sender *tuiapp.ProgramSender, initial gatewayapp.SandboxStatus) {
	if runtime.GOOS != "windows" || stack == nil || sender == nil {
		return
	}
	backend := strings.TrimSpace(firstNonEmptyString(initial.ResolvedBackend, initial.RequestedBackend))
	if !strings.EqualFold(backend, string(sandbox.BackendWindowsElevated)) {
		return
	}
	if !initial.WorkspaceSetupRequired || initial.GlobalSetupRequired {
		return
	}
	go func() {
		title := "Windows sandbox workspace"
		sendWorkspaceProgress := func(progress sandbox.PrepareProgress) {
			sender.SendMsg(tuiapp.SandboxProgressMsg{
				Title:   title,
				Source:  title,
				Phase:   strings.TrimSpace(progress.Phase),
				Message: strings.TrimSpace(progress.Message),
				Step:    progress.Step,
				Total:   progress.Total,
				Done:    progress.Done,
			})
		}
		sendWorkspaceProgress(sandbox.PrepareProgress{
			Phase:   "workspace",
			Message: "preparing sandbox ACLs for this workspace in the background",
		})
		progressCtx := sandbox.ContextWithPrepareProgress(ctx, sendWorkspaceProgress)
		status, err := stack.PreflightSandbox(progressCtx, true)
		if err != nil {
			sender.SendMsg(tuiapp.LogChunkMsg{Chunk: "Windows sandbox workspace setup failed: " + err.Error() + "\n"})
			sendWorkspaceProgress(sandbox.PrepareProgress{
				Phase:   "workspace",
				Message: "workspace sandbox setup failed",
				Done:    true,
			})
		} else if status.WorkspaceSetupCurrent || !status.WorkspaceSetupRequired {
			sender.SendMsg(tuiapp.LogChunkMsg{Chunk: "Windows sandbox workspace setup complete.\n"})
			sendWorkspaceProgress(sandbox.PrepareProgress{
				Phase:   "complete",
				Message: "workspace sandbox ACLs are ready",
				Step:    3,
				Total:   3,
				Done:    true,
			})
		}
		time.Sleep(1600 * time.Millisecond)
		sender.SendMsg(tuiapp.SandboxProgressMsg{Source: title, Clear: true})
	}()
}

func initialSandboxStatusLogs(status gatewayapp.SandboxStatus) []string {
	var logs []string
	backend := strings.TrimSpace(firstNonEmptyString(status.ResolvedBackend, status.RequestedBackend))
	if status.SetupRequired && strings.EqualFold(backend, "windows-elevated") {
		message := "Windows sandbox setup is not ready. Run /sandbox setup once and approve the UAC prompt before using sandboxed commands."
		if status.WorkspaceSetupRequired && !status.GlobalSetupRequired {
			message = "Current workspace needs Windows sandbox ACL setup. Preparing it in the background; commands may wait until it completes."
		}
		if reason := strings.TrimSpace(firstNonEmptyString(status.GlobalSetupReason, status.WorkspaceSetupReason, status.SetupMarkerReason)); reason != "" {
			message += " Reason: " + reason + "."
		}
		if setupErr := strings.TrimSpace(status.SetupError); setupErr != "" {
			message += " Last error: " + setupErr + "."
		}
		logs = append(logs, message)
	}
	if sandboxHostFallback(status) {
		message := platformSandboxUnavailableMessage(status)
		if message != "" {
			logs = append(logs, message)
		}
	}
	return logs
}

func sandboxHostFallback(status gatewayapp.SandboxStatus) bool {
	if strings.EqualFold(strings.TrimSpace(status.Route), "host") || strings.EqualFold(strings.TrimSpace(status.ResolvedBackend), "host") {
		return !strings.EqualFold(strings.TrimSpace(status.RequestedBackend), "host")
	}
	return false
}

func platformSandboxUnavailableMessage(status gatewayapp.SandboxStatus) string {
	reason := strings.TrimSpace(status.FallbackReason)
	hint := strings.TrimSpace(status.InstallHint)
	parts := []string{"Sandbox isolation is not available; commands will run on the host."}
	switch runtime.GOOS {
	case "linux":
		parts = append(parts, "Install bubblewrap or use a Landlock-capable kernel to restore sandbox isolation.")
	case "darwin":
		parts = append(parts, "macOS seatbelt sandboxing should be available by default; update macOS if sandbox-exec is missing.")
	case "windows":
		parts = append(parts, "Run /sandbox setup once to initialize Windows Elevated sandbox.")
	default:
		parts = append(parts, "Install a supported sandbox backend for this platform.")
	}
	if hint != "" {
		parts = append(parts, hint)
	}
	if reason != "" {
		parts = append(parts, "Reason: "+reason+".")
	}
	parts = append(parts, "Auto-Review remains enabled and can approve host execution; use /approval manual for sensitive work.")
	return strings.Join(parts, " ")
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
