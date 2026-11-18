package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/agentregistry"
	"github.com/caelis-labs/caelis/control/adapterhost"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/plugin"
)

func (s *controlCommandBackend) resolveACPConnectionLauncher(ctx context.Context, req controlagents.ConnectRequest) (controlagents.Connection, error) {
	req = controlagents.NormalizeConnectRequest(req)
	if req.AdapterID == "" {
		return controlagents.Connection{}, fmt.Errorf("gatewayapp: ACP adapter is required")
	}
	connection := controlagents.Connection{ID: req.AdapterID, Name: req.AdapterID}
	if _, ok := agentregistry.LookupConnectableAgent(req.AdapterID); !ok {
		return controlagents.Connection{}, fmt.Errorf("gatewayapp: unknown ACP adapter %q", req.AdapterID)
	}
	if !agentregistry.SupportsLauncher(req.AdapterID, req.Launcher) {
		return controlagents.Connection{}, fmt.Errorf(
			"gatewayapp: ACP adapter %q does not support launcher %q",
			req.AdapterID,
			req.Launcher,
		)
	}
	switch req.Launcher {
	case controlagents.LauncherChoiceHosted:
		if s == nil || s.composition == nil || s.composition.authorities.adapterHost == nil {
			return controlagents.Connection{}, fmt.Errorf("gatewayapp: built-in ACP adapter service is unavailable")
		}
		descriptor, err := s.composition.authorities.adapterHost.RequireAvailable(ctx, req.AdapterID)
		if err != nil {
			if req.AdapterID == adapterhost.CodexAdapterID && errors.Is(err, exec.ErrNotFound) {
				return controlagents.Connection{}, errorcode.New(errorcode.InvalidArgument, `Install Codex so "codex" is available on the Host's PATH.`)
			}
			return controlagents.Connection{}, fmt.Errorf("gatewayapp: built-in ACP adapter %q is unavailable: %w", req.AdapterID, err)
		}
		connection.Name = firstNonEmpty(descriptor.Name, req.AdapterID)
		connection.Launcher = controlagents.Launcher{
			Kind: controlagents.LaunchKindHostedAdapter, AdapterID: req.AdapterID,
		}
	case controlagents.LauncherChoiceCommand:
		command, args, err := splitACPCommandLine(req.CommandLine)
		if err != nil {
			return controlagents.Connection{}, err
		}
		connection.Name = filepath.Base(command)
		connection.Launcher = controlagents.Launcher{Kind: controlagents.LaunchKindExecutable, Command: command, Args: args}
		connection.ID = controlagents.CustomConnectionID(command, connection.Launcher)
	case controlagents.LauncherChoiceInstalled:
		preset, ok := agentregistry.LookupInstalledAgent(req.AdapterID)
		if !ok {
			return controlagents.Connection{}, catalogLauncherInconsistency(req.AdapterID, req.Launcher, "installed-command preset")
		}
		commands := agentregistry.InstalledAgentCommandCandidates(req.AdapterID)
		selectedCommand := agentregistry.FindInstalledCommand(req.AdapterID)
		if selectedCommand == "" {
			return controlagents.Connection{}, installedACPCommandNotFound(req.AdapterID, commands)
		}
		connection.Launcher = controlagents.Launcher{
			Kind: controlagents.LaunchKindExecutable, Command: selectedCommand, Args: append([]string(nil), preset.Args...),
			Env: preset.Env,
		}
	default:
		return controlagents.Connection{}, fmt.Errorf("gatewayapp: unsupported launcher %q for ACP adapter %q", req.Launcher, req.AdapterID)
	}
	return connection, controlagents.ValidateConnection(connection)
}

func installedACPCommandNotFound(adapterID string, commands []string) error {
	// An unresolved catalog launcher is invalid setup input. Keep this curated
	// diagnostic public for clients that bypass guided setup.
	if len(commands) == 1 {
		return errorcode.New(errorcode.InvalidArgument, fmt.Sprintf(
			"%s is not installed. Open /connect to set up %s.", commands[0], adapterID,
		))
	}
	quoted := make([]string, 0, len(commands))
	for _, command := range commands {
		quoted = append(quoted, fmt.Sprintf("%q", command))
	}
	return errorcode.New(errorcode.InvalidArgument, fmt.Sprintf(
		"Install ACP agent %q so one of %s is available on PATH.",
		adapterID,
		strings.Join(quoted, ", "),
	))
}

func catalogLauncherInconsistency(
	adapterID string,
	launcher controlagents.LauncherChoice,
	missing string,
) error {
	return fmt.Errorf(
		"gatewayapp: ACP adapter %q catalog declares launcher %q but has no %s",
		strings.TrimSpace(adapterID),
		launcher,
		strings.TrimSpace(missing),
	)
}

func splitACPCommandLine(commandLine string) (string, []string, error) {
	command, args, err := plugin.SplitCommand(commandLine)
	if err != nil {
		return "", nil, fmt.Errorf("gatewayapp: parse custom ACP command: %w", err)
	}
	if strings.TrimSpace(command) == "" {
		return "", nil, fmt.Errorf("gatewayapp: custom ACP command is required")
	}
	resolved, lookErr := exec.LookPath(command)
	if lookErr != nil || strings.TrimSpace(resolved) == "" {
		return "", nil, fmt.Errorf("gatewayapp: custom ACP command %q was not found", command)
	}
	if filepath.Base(command) != command {
		absolute, absErr := filepath.Abs(resolved)
		if absErr != nil {
			return "", nil, fmt.Errorf("gatewayapp: resolve custom ACP command %q: %w", command, absErr)
		}
		command = absolute
	}
	return command, append([]string(nil), args...), nil
}
