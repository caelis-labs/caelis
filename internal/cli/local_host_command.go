package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/internal/servicelifecycle"
)

const localHostShutdownTimeout = 10 * time.Second

type localHostCommandResult struct {
	State               string `json:"state"`
	InstanceID          string `json:"instance_id,omitempty"`
	PID                 int    `json:"pid,omitempty"`
	Endpoint            string `json:"endpoint,omitempty"`
	DistributionVersion string `json:"distribution_version,omitempty"`
	BuildID             string `json:"build_id,omitempty"`
	BuildKind           string `json:"build_kind,omitempty"`
}

func runLocalHostCommand(
	ctx context.Context,
	action string,
	config gatewayapp.Config,
	format outputFormat,
	stdout io.Writer,
) error {
	options := productClientOptions{
		Mode: productClientModeManaged, AppName: config.AppName, UserID: config.UserID, StoreDir: config.StoreDir,
	}
	return runLocalHostCommandWithOptions(ctx, action, config, options, format, stdout)
}

func runLocalHostCommandWithOptions(
	ctx context.Context,
	action string,
	config gatewayapp.Config,
	options productClientOptions,
	format outputFormat,
	stdout io.Writer,
) error {
	manager, candidate, err := newLocalServiceManager(options)
	if err != nil {
		return err
	}
	var status servicelifecycle.Status
	switch action {
	case "start":
		status, err = manager.Start(ctx, candidate)
	case "restart":
		status, err = manager.Restart(ctx, candidate)
	case "stop":
		_, err = manager.Stop(ctx)
		if err == nil {
			return writeLocalHostCommandResult(stdout, format, localHostCommandResult{State: "stopped"})
		}
	case "status":
		status, err = manager.Status(ctx)
	default:
		return fmt.Errorf("cli: unsupported service action %q", action)
	}
	if errors.Is(err, os.ErrNotExist) {
		return writeLocalHostCommandResult(stdout, format, localHostCommandResult{State: "stopped"})
	}
	if err != nil {
		return fmt.Errorf("cli: %s Caelis service: %w", action, err)
	}
	return writeLocalHostCommandResult(stdout, format, localHostCommandResult{
		State: "running", InstanceID: status.InstanceID, PID: status.PID, Endpoint: status.Endpoint,
		DistributionVersion: status.DistributionVersion, BuildID: status.BuildID, BuildKind: status.BuildKind,
	})
}

func writeLocalHostCommandResult(writer io.Writer, format outputFormat, result localHostCommandResult) error {
	if writer == nil {
		writer = io.Discard
	}
	switch format {
	case outputJSON, outputJSONL:
		return json.NewEncoder(writer).Encode(result)
	case outputText:
		if result.State != "running" {
			_, err := fmt.Fprintln(writer, "Caelis service: stopped")
			return err
		}
		_, err := fmt.Fprintf(
			writer,
			"Caelis service: running\ndistribution_version: %s\nbuild_id: %s\nbuild_kind: %s\ninstance_id: %s\npid: %d\nendpoint: %s\n",
			result.DistributionVersion,
			result.BuildID,
			result.BuildKind,
			result.InstanceID,
			result.PID,
			result.Endpoint,
		)
		return err
	default:
		return fmt.Errorf("cli: unsupported output format %q", format)
	}
}
