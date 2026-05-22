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
	initialSandboxStatus := stack.SandboxStartupStatus()
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
	go func() {
		title := "Windows sandbox workspace"
		progressVisible := false
		sendWorkspaceProgress := func(progress sandbox.PrepareProgress) {
			progressVisible = true
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
		progressCtx := sandbox.ContextWithPrepareProgress(ctx, sendWorkspaceProgress)
		status, err := stack.PreflightSandbox(progressCtx, true)
		nextGlobalSetup, _ := status.Setup.Check("global")
		nextWorkspaceSetup, _ := status.Setup.Check("workspace")
		globalRequired := status.GlobalSetupRequired || nextGlobalSetup.Required
		workspaceRequired := status.WorkspaceSetupRequired || nextWorkspaceSetup.Required
		workspaceCurrent := status.WorkspaceSetupCurrent || nextWorkspaceSetup.Current
		if err != nil {
			sender.SendMsg(tuiapp.LogChunkMsg{Chunk: "Windows sandbox workspace setup failed: " + err.Error() + "\n"})
			progressVisible = true
			sender.SendMsg(tuiapp.SandboxProgressMsg{
				Title:   title,
				Source:  title,
				Phase:   "workspace",
				Message: "workspace sandbox setup failed",
				Done:    true,
			})
		} else if globalRequired {
			sender.SendMsg(tuiapp.LogChunkMsg{Chunk: "Windows sandbox setup is not ready. Run /sandbox setup once and approve the UAC prompt before using sandboxed commands.\n"})
			progressVisible = true
			sender.SendMsg(tuiapp.SandboxProgressMsg{
				Title:   title,
				Source:  title,
				Phase:   "setup",
				Message: "global Windows sandbox setup requires /sandbox setup",
				Done:    true,
			})
		} else if workspaceRequired && !workspaceCurrent {
			sender.SendMsg(tuiapp.LogChunkMsg{Chunk: "Current workspace still needs Windows sandbox ACL setup. Run /sandbox setup if commands cannot start in the sandbox.\n"})
			progressVisible = true
			sender.SendMsg(tuiapp.SandboxProgressMsg{
				Title:   title,
				Source:  title,
				Phase:   "workspace",
				Message: "workspace sandbox ACL setup is still required",
				Done:    true,
			})
		} else if progressVisible {
			sender.SendMsg(tuiapp.SandboxProgressMsg{
				Title:   title,
				Source:  title,
				Phase:   "complete",
				Message: "workspace sandbox ACLs are ready",
				Step:    3,
				Total:   3,
				Done:    true,
			})
		}
		if progressVisible {
			time.Sleep(1600 * time.Millisecond)
			sender.SendMsg(tuiapp.SandboxProgressMsg{Source: title, Clear: true})
		}
	}()
}

func initialSandboxStatusLogs(status gatewayapp.SandboxStatus) []string {
	var logs []string
	backend := strings.TrimSpace(firstNonEmptyString(status.ResolvedBackend, status.RequestedBackend))
	globalSetup, _ := status.Setup.Check("global")
	workspaceSetup, _ := status.Setup.Check("workspace")
	setupRequired := status.Setup.Required || status.SetupRequired
	globalRequired := globalSetup.Required || status.GlobalSetupRequired
	workspaceRequired := workspaceSetup.Required || status.WorkspaceSetupRequired
	if setupRequired && strings.EqualFold(backend, "windows-elevated") {
		message := "Windows sandbox setup is not ready. Run /sandbox setup once and approve the UAC prompt before using sandboxed commands."
		if workspaceRequired && !globalRequired {
			message = "Current workspace needs Windows sandbox ACL setup. Preparing it in the background; commands may wait until it completes."
		}
		if reason := strings.TrimSpace(firstNonEmptyString(globalSetup.Reason, workspaceSetup.Reason, status.GlobalSetupReason, status.WorkspaceSetupReason, status.SetupMarkerReason)); reason != "" {
			message += " Reason: " + reason + "."
		}
		if setupErr := strings.TrimSpace(firstNonEmptyString(status.Setup.Error, globalSetup.Error, workspaceSetup.Error, status.SetupError)); setupErr != "" {
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
