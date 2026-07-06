package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/caelis-labs/caelis/internal/updater"
	"github.com/caelis-labs/caelis/internal/version"
)

type versionResult = version.Info

var (
	runUpdateOperation = func(ctx context.Context, cfg updater.Config, opts updater.UpdateOptions) (updater.Result, error) {
		return updater.New(cfg).Update(ctx, opts)
	}
	checkUpdateOperation = func(ctx context.Context, cfg updater.Config, opts updater.CheckOptions) (updater.Result, error) {
		return updater.New(cfg).Check(ctx, opts)
	}
)

func runVersionSubcommand(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("caelis version", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	format := fs.String("format", string(outputText), "Output format: text|json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unknown version arguments: %v", fs.Args())
	}
	outFmt, err := parseOutputFormat(*format)
	if err != nil {
		return err
	}
	return writeVersionResult(stdout, outFmt, version.BuildInfo())
}

func runUpdateSubcommand(ctx context.Context, args []string, defaultStoreDir string, stdout io.Writer, stderr io.Writer) error {
	fs := flag.NewFlagSet("caelis update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	checkOnly := fs.Bool("check", false, "Check for updates without installing")
	storeDir := fs.String("store-dir", defaultStoreDir, "Store directory for update cache")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unknown update arguments: %v", fs.Args())
	}
	return runUpdate(ctx, strings.TrimSpace(*storeDir), *checkOnly, stdout, stderr)
}

func runUpdate(ctx context.Context, storeDir string, checkOnly bool, stdout io.Writer, stderr io.Writer) error {
	result, err := runUpdateOperation(ctx, updateConfig(storeDir), updater.UpdateOptions{
		CheckOnly: checkOnly,
		Stdout:    stdout,
		Stderr:    stderr,
	})
	if err != nil {
		return err
	}
	return writeUpdateResult(stdout, result)
}

func updateConfig(storeDir string) updater.Config {
	info := version.BuildInfo()
	return updater.Config{
		StoreDir:       strings.TrimSpace(storeDir),
		CurrentVersion: info.Version,
	}
}

func writeVersionResult(w io.Writer, format outputFormat, result versionResult) error {
	switch format {
	case outputJSON:
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		return enc.Encode(result)
	default:
		lines := []string{"version: " + firstNonEmptyString(strings.TrimSpace(result.Version), "dev")}
		if commit := strings.TrimSpace(result.Commit); commit != "" {
			lines = append(lines, "commit: "+commit)
		}
		if date := strings.TrimSpace(result.Date); date != "" {
			lines = append(lines, "date: "+date)
		}
		_, err := fmt.Fprintln(w, strings.Join(lines, "\n"))
		return err
	}
}

func writeUpdateResult(w io.Writer, result updater.Result) error {
	_, err := fmt.Fprintln(w, formatUpdateResult(result))
	return err
}

func formatUpdateResult(result updater.Result) string {
	current := firstNonEmptyString(strings.TrimSpace(result.CurrentVersion), "dev")
	latest := strings.TrimSpace(result.LatestVersion)
	method := strings.TrimSpace(result.InstallMethod)
	if method == "" {
		method = "unknown"
	}
	if result.Skipped {
		reason := firstNonEmptyString(strings.TrimSpace(result.Reason), "not supported for this installation")
		return "update skipped: " + reason
	}
	if result.Deferred {
		return fmt.Sprintf("update scheduled: %s -> %s (%s). Restart caelis after this process exits.", current, latest, method)
	}
	if result.Updated {
		return fmt.Sprintf("updated caelis: %s -> %s (%s)", current, latest, method)
	}
	if result.Available {
		return fmt.Sprintf("update available: %s -> %s (%s)", current, latest, method)
	}
	return fmt.Sprintf("caelis is up to date (%s)", current)
}
