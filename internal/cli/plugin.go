package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/controlprompt/appserveradapter"
)

// pluginCommandOutput is the versioned headless installer result. A failed
// mutation may include a receipt: its outcome, rather than the process exit,
// determines whether the Host committed the change.
type pluginCommandOutput struct {
	SchemaVersion string                    `json:"schema_version"`
	Action        string                    `json:"action"`
	ID            string                    `json:"id,omitempty"`
	Installed     bool                      `json:"installed"`
	Plugin        *appserver.PluginSnapshot `json:"plugin,omitempty"`
	Error         string                    `json:"error,omitempty"`
	Receipt       *appserver.CommandResult  `json:"receipt,omitempty"`
}

func runPluginSubcommand(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer, openClients productClientOpener) (runErr error) {
	fs := flag.NewFlagSet("caelis plugin", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOutput := false
	for _, arg := range args {
		if arg == "--json" || arg == "-json" {
			jsonOutput = true
		}
	}
	fs.BoolVar(&jsonOutput, "json", jsonOutput, "Write a versioned JSON result")
	storeDir := fs.String("store-dir", envOr("CAELIS_STORE_DIR", defaultStoreDir(cwd)), "Local Caelis Store directory")
	embedded := fs.Bool("embedded", envBool("CAELIS_CONTROL_EMBEDDED", false), "Use an exclusive in-process local Host")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: caelis plugin install <absolute-directory> | remove <id> | status <id> [--json] [--store-dir <directory>] [--embedded]")
		fs.PrintDefaults()
	}
	result := pluginCommandOutput{SchemaVersion: "1"}
	defer func() {
		if runErr == nil || !jsonOutput {
			return
		}
		result.Error = runErr.Error()
		var receiptErr *appserver.CommandReceiptError
		if errors.As(runErr, &receiptErr) {
			result.Receipt = &receiptErr.Receipt
			if receiptErr.Receipt.Outcome == appserver.OutcomeCommitted {
				switch result.Action {
				case "install":
					result.Installed = true
				case "remove":
					result.Installed, result.Plugin = false, nil
				}
			}
		}
		runErr = errors.Join(runErr, json.NewEncoder(stdout).Encode(result))
	}()
	// Go's flag parser stops at each positional argument. Continue parsing so
	// installers can place flags before or after the directory or ID.
	var positional []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		args = fs.Args()
		if len(args) > 0 {
			positional = append(positional, args[0])
			args = args[1:]
		}
	}
	if len(positional) != 2 {
		return errors.New("cli: plugin requires install <absolute-directory>, remove <id>, or status <id>")
	}
	result.Action = strings.ToLower(positional[0])
	target := strings.TrimSpace(positional[1])
	switch result.Action {
	case "install":
		if !filepath.IsAbs(target) {
			return errors.New("cli: plugin install requires an absolute local directory")
		}
		info, err := os.Stat(target)
		if err != nil {
			return fmt.Errorf("cli: plugin source directory: %w", err)
		}
		if !info.IsDir() {
			return errors.New("cli: plugin source is not a directory")
		}
	case "remove", "status":
		result.ID = strings.ToLower(target)
		if result.ID == "" {
			return errors.New("cli: plugin id is required")
		}
	default:
		return fmt.Errorf("cli: unknown plugin subcommand %q", result.Action)
	}
	if strings.TrimSpace(os.Getenv("CAELIS_CONTROL_URL")) != "" {
		return errors.New("cli: plugin commands require a local Host; unset CAELIS_CONTROL_URL")
	}
	workspaceKey, workspaceCWD, err := workspaceAddressFromCWD(cwd)
	if err != nil {
		return err
	}
	cfg := gatewayapp.Config{AppName: defaultAppName, UserID: defaultPrincipalID, StoreDir: *storeDir, WorkspaceKey: workspaceKey, WorkspaceCWD: workspaceCWD}
	mode := productClientModeManaged
	if *embedded {
		mode = productClientModeEmbedded
	}
	clients, err := openClients(ctx, cfg, productClientOptions{
		Mode: mode, StoreDir: cfg.StoreDir, AppName: cfg.AppName, UserID: cfg.UserID,
		WorkspaceKey: workspaceKey, WorkspaceCWD: workspaceCWD,
		Token: strings.TrimSpace(os.Getenv("CAELIS_CONTROL_TOKEN")), TokenFile: strings.TrimSpace(os.Getenv("CAELIS_CONTROL_TOKEN_FILE")),
		ListenAddress: strings.TrimSpace(os.Getenv("CAELIS_CONTROL_LISTEN")),
	})
	if err != nil {
		return fmt.Errorf("cli: open local Plugin Host: %w", err)
	}
	defer clients.Close()
	driver, err := appserveradapter.NewAppServerAdapter(appserveradapter.AppServerAdapterConfig{
		WorkspaceKey: workspaceKey, WorkspaceDir: workspaceCWD, Surface: "cli-plugin",
		Sessions: clients.Clients.Sessions, Participants: clients.Clients.Participants, SubagentInputs: clients.Clients.SubagentInputs,
		Status: clients.Clients.Status, Configuration: clients.Clients.Configuration, Agents: clients.Clients.Agents,
		Completion: clients.Clients.Completion, Plugins: clients.Clients.Plugins,
	})
	if err != nil {
		return err
	}
	defer driver.Close()
	if result.Action == "install" {
		installed, err := driver.InstallPlugin(ctx, target)
		if installed.ID != "" {
			result.ID, result.Installed, result.Plugin = installed.ID, true, &installed
		}
		if err != nil {
			return err
		}
	} else {
		plugins, err := driver.ListPlugins(ctx)
		if err != nil {
			return err
		}
		for _, installed := range plugins {
			if strings.EqualFold(installed.ID, result.ID) {
				result.Installed, result.Plugin = true, &installed
				break
			}
		}
		if result.Action == "remove" {
			if result.Installed {
				if err := driver.RemovePlugin(ctx, result.ID); err != nil {
					return err
				}
			}
			result.Installed, result.Plugin = false, nil
		}
	}
	if jsonOutput {
		return json.NewEncoder(stdout).Encode(result)
	}
	if result.Plugin != nil {
		_, err = fmt.Fprintf(stdout, "%s %s: %s (version %s, enabled %t)\n", result.Action, result.ID, result.Plugin.Root, result.Plugin.Version, result.Plugin.Enabled)
	} else {
		_, err = fmt.Fprintf(stdout, "%s %s: absent\n", result.Action, result.ID)
	}
	return err
}
