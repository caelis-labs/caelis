package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/app/gatewayapp"
)

type storeLifecycleResult struct {
	Command  string                          `json:"command"`
	Archive  string                          `json:"archive,omitempty"`
	Manifest *gatewayapp.StoreBackupManifest `json:"manifest,omitempty"`
	Restore  *gatewayapp.StoreRestoreReport  `json:"restore,omitempty"`
	Upgrade  *gatewayapp.StoreUpgradeReport  `json:"upgrade,omitempty"`
}

func runStoreLifecycleCommand(
	ctx context.Context,
	storeCommand string,
	upgradeCommand string,
	cfg gatewayapp.Config,
	format outputFormat,
	outputPath string,
	inputPath string,
	stdout io.Writer,
	stderr io.Writer,
) error {
	if storeCommand != "" && upgradeCommand != "" {
		return errors.New("cli: Store command and upgrade command cannot be combined")
	}
	if storeCommand == "restore" && strings.TrimSpace(inputPath) == "" {
		return errors.New("cli: restore requires --input")
	}
	if err := runLocalHostCommand(ctx, "stop", cfg, outputJSON, io.Discard); err != nil {
		return err
	}
	switch {
	case storeCommand == "backup":
		manifest, archive, err := writeStoreBackupArchive(ctx, cfg, outputPath)
		if err != nil {
			return err
		}
		return writeStoreLifecycleResult(stdout, format, storeLifecycleResult{
			Command: "backup", Archive: archive, Manifest: &manifest,
		})
	case storeCommand == "restore":
		input, err := os.Open(inputPath)
		if err != nil {
			return fmt.Errorf("cli: open Store restore archive: %w", err)
		}
		report, restoreErr := gatewayapp.RestoreStoreWithEmbeddedMemory(ctx, cfg.StoreDir, input)
		closeErr := input.Close()
		if restoreErr != nil {
			return restoreErr
		}
		if closeErr != nil {
			return fmt.Errorf("cli: close Store restore archive: %w", closeErr)
		}
		return writeStoreLifecycleResult(stdout, format, storeLifecycleResult{Command: "restore", Restore: &report})
	case upgradeCommand != "":
		return runStoreUpgradeCommand(ctx, upgradeCommand, cfg, format, stdout)
	default:
		_ = stderr
		return errors.New("cli: Store lifecycle command is required")
	}
}

func writeStoreBackupArchive(ctx context.Context, cfg gatewayapp.Config, requestedPath string) (gatewayapp.StoreBackupManifest, string, error) {
	requestedPath = strings.TrimSpace(requestedPath)
	if requestedPath == "" {
		requestedPath = filepath.Join(mustGetwdForStoreCommand(), "caelis-store-backup.zip")
	}
	absolute, err := filepath.Abs(requestedPath)
	if err != nil {
		return gatewayapp.StoreBackupManifest{}, "", fmt.Errorf("cli: resolve Store backup output: %w", err)
	}
	absolute = filepath.Clean(absolute)
	if err := rejectStoreBackupOutputInsideStore(cfg.StoreDir, absolute); err != nil {
		return gatewayapp.StoreBackupManifest{}, "", err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return gatewayapp.StoreBackupManifest{}, "", fmt.Errorf("cli: create Store backup output directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(absolute), ".caelis-store-backup-*.tmp")
	if err != nil {
		return gatewayapp.StoreBackupManifest{}, "", fmt.Errorf("cli: create Store backup staging file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return gatewayapp.StoreBackupManifest{}, "", err
	}
	authority, err := acquireProductHostOwnership(cfg.StoreDir)
	if err != nil {
		_ = temporary.Close()
		return gatewayapp.StoreBackupManifest{}, "", err
	}
	cfg.HostOwnership = authority
	stack, err := gatewayapp.NewLocalStack(cfg)
	if err != nil {
		_ = authority.Close()
		_ = temporary.Close()
		return gatewayapp.StoreBackupManifest{}, "", err
	}
	manifest, backupErr := stack.WriteStoreBackup(ctx, temporary)
	closeErr := stack.Close()
	authorityErr := authority.Close()
	if backupErr != nil {
		_ = temporary.Close()
		return gatewayapp.StoreBackupManifest{}, "", backupErr
	}
	if closeErr != nil {
		_ = temporary.Close()
		return gatewayapp.StoreBackupManifest{}, "", closeErr
	}
	if authorityErr != nil {
		_ = temporary.Close()
		return gatewayapp.StoreBackupManifest{}, "", authorityErr
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return gatewayapp.StoreBackupManifest{}, "", fmt.Errorf("cli: sync Store backup: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return gatewayapp.StoreBackupManifest{}, "", fmt.Errorf("cli: close Store backup: %w", err)
	}
	if err := os.Rename(temporaryPath, absolute); err != nil {
		return gatewayapp.StoreBackupManifest{}, "", fmt.Errorf("cli: publish Store backup: %w", err)
	}
	return manifest, absolute, nil
}

func rejectStoreBackupOutputInsideStore(storeDir, outputPath string) error {
	storeDir = strings.TrimSpace(storeDir)
	if storeDir == "" {
		return nil
	}
	storeAbsolute, err := filepath.Abs(storeDir)
	if err != nil {
		return fmt.Errorf("cli: resolve Store directory for backup output: %w", err)
	}
	relative, err := filepath.Rel(filepath.Clean(storeAbsolute), filepath.Clean(outputPath))
	if err != nil {
		return fmt.Errorf("cli: compare Store backup output with Store directory: %w", err)
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return errors.New("cli: Store backup output must be outside the Store directory")
	}
	return nil
}

func runStoreUpgradeCommand(ctx context.Context, command string, cfg gatewayapp.Config, format outputFormat, stdout io.Writer) error {
	result := storeLifecycleResult{Command: "upgrade " + command}
	switch command {
	case "prepare":
		report, err := gatewayapp.PrepareStoreUpgrade(ctx, cfg.StoreDir)
		if err != nil {
			return err
		}
		result.Upgrade = &report
	case "commit":
		if err := gatewayapp.CommitStoreUpgrade(ctx, cfg.StoreDir); err != nil {
			return err
		}
	case "rollback":
		report, err := gatewayapp.RollbackStoreUpgrade(ctx, cfg.StoreDir)
		if err != nil {
			return err
		}
		result.Upgrade = &report
	default:
		return fmt.Errorf("cli: unsupported upgrade command %q", command)
	}
	return writeStoreLifecycleResult(stdout, format, result)
}

func writeStoreLifecycleResult(writer io.Writer, format outputFormat, result storeLifecycleResult) error {
	if writer == nil {
		writer = io.Discard
	}
	switch format {
	case outputJSON, outputJSONL:
		return json.NewEncoder(writer).Encode(result)
	case outputText:
		if result.Archive != "" {
			_, err := fmt.Fprintf(writer, "Store backup: %s\n", result.Archive)
			return err
		}
		if result.Restore != nil {
			_, err := fmt.Fprintf(writer, "Store restore: restored %d components\n", len(result.Restore.RestoredComponents))
			return err
		}
		_, err := fmt.Fprintf(writer, "Store %s: complete\n", result.Command)
		return err
	default:
		return fmt.Errorf("cli: unsupported output format %q", format)
	}
}

func mustGetwdForStoreCommand() string {
	workingDirectory, err := getWorkingDirectory()
	if err != nil || strings.TrimSpace(workingDirectory) == "" {
		return "."
	}
	return workingDirectory
}
