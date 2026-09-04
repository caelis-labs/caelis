package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/caelis-labs/caelis/app/gatewayapp"
)

func runDoctorStartupRepairs(
	ctx context.Context,
	config gatewayapp.Config,
	options productClientOptions,
) ([]doctorRepairResult, error) {
	plans, err := gatewayapp.InspectDoctorRepairs(ctx, config.StoreDir)
	if err != nil {
		return nil, fmt.Errorf("caelis doctor could not inspect compatibility repairs: %w", err)
	}
	if len(plans) == 0 {
		return nil, nil
	}
	if err := runLocalHostCommandWithOptions(ctx, "stop", config, options, outputJSON, io.Discard); err != nil {
		return nil, fmt.Errorf("caelis doctor repair failed [%s]: stop the local Control Host: %w", plans[0].Code, err)
	}

	results := make([]doctorRepairResult, 0, len(plans))
	for _, plan := range plans {
		report, err := gatewayapp.RepairDoctorIssue(ctx, config.StoreDir, plan.Code)
		if err != nil {
			return results, fmt.Errorf("caelis doctor repair failed [%s]: %w", plan.Code, err)
		}
		results = append(results, doctorRepairResult{
			Code:                     string(report.Code),
			Status:                   "repaired",
			ConflictingWorkspaceKeys: firstNonZero(report.ConflictingWorkspaceKeys, plan.ConflictingWorkspaceKeys),
			AffectedSessions:         firstNonZero(report.AffectedSessions, plan.AffectedSessions),
			RepairedSessions:         report.RepairedSessions,
			RepairedTasks:            report.RepairedTasks,
		})
	}
	return results, nil
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}
