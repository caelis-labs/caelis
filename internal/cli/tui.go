package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	controlpromptrouter "github.com/caelis-labs/caelis/internal/controlpromptrouter"
	"github.com/caelis-labs/caelis/internal/updater"
	"github.com/caelis-labs/caelis/internal/version"
	"github.com/caelis-labs/caelis/surfaces/tui/app"
)

const tuiBackgroundUpdateCheckTimeout = 2 * time.Minute

func runTUI(ctx context.Context, stack *gatewayapp.Stack, sessionID string, appCfg gatewayapp.Config, modelText string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	driver, err := local.NewLocalAdapter(ctx, stack, strings.TrimSpace(sessionID), "cli-tui", strings.TrimSpace(modelText))
	if err != nil {
		return err
	}
	programCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sender := &tuiapp.ProgramSender{}
	updateRequested := false
	tuiCfg := tuiapp.ConfigFromControlService(driver, sender, tuiapp.Config{
		Context:             programCtx,
		AppName:             "CAELIS",
		Version:             version.String(),
		Workspace:           stack.Workspace.CWD,
		ModelAlias:          modelText,
		ShowWelcomeCard:     true,
		Commands:            tuiapp.DefaultCommands(),
		Wizards:             tuiapp.DefaultWizards(),
		PromptRouterFactory: controlpromptrouter.New,
		RenderFPS:           envInt("CAELIS_TUI_RENDER_FPS", 0),
		OnStart: func() {
			startTUISandboxRefresh(programCtx, stack, sender)
			startTUIUpdateCheck(programCtx, appCfg.StoreDir, sender)
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
	if err != nil {
		return err
	}
	if updateRequested {
		return runUpdate(ctx, appCfg.StoreDir, false, stdout, stderr)
	}
	return nil
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
	lines = append(lines, "run /doctor")
	return strings.Join(lines, "\n")
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
