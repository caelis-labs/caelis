package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/internal/hostownership"
)

func runDoctorAttachmentPrune(ctx context.Context, cfg gatewayapp.Config, options productClientOptions, id, cutoff string, apply bool, out io.Writer) error {
	before, err := time.Parse(time.RFC3339, cutoff)
	if err != nil || strings.TrimSpace(id) == "" {
		return fmt.Errorf("attachment prune requires --session and an RFC3339 --prune-attachments-before")
	}
	var owner *hostownership.Authority
	if apply {
		if err := runLocalHostCommandWithOptions(ctx, "stop", cfg, options, outputJSON, io.Discard); err != nil {
			return err
		}
		owner, err = acquireProductHostOwnership(cfg.StoreDir)
		if err != nil {
			return err
		}
		defer owner.Close()
	}
	report, err := gatewayapp.MaintainDoctorAttachments(ctx, cfg.StoreDir, strings.TrimSpace(id), before.UTC(), owner)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(struct {
		Applied bool  `json:"applied"`
		Files   int   `json:"files"`
		Bytes   int64 `json:"bytes"`
	}{apply, report.Files, report.Bytes})
}
