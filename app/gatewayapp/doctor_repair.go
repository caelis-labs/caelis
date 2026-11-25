package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/internal/hostownership"
	"github.com/caelis-labs/caelis/internal/workspaceidentity"
)

// DoctorRepairPlan describes one recognized local compatibility repair without
// mutating the Store.
type DoctorRepairPlan struct {
	Code                     string
	ConflictingWorkspaceKeys int
	AffectedSessions         int
}

// DoctorRepairReport describes one compatibility repair completed by doctor.
type DoctorRepairReport struct {
	Code                     string
	ConflictingWorkspaceKeys int
	AffectedSessions         int
	RepairedSessions         int
	RepairedTasks            int
}

// DoctorWorkspaceIdentityConflict identifies the repair for a key shared by different directories.
const DoctorWorkspaceIdentityConflict = "CAELIS_STARTUP_WORKSPACE_IDENTITY_CONFLICT"

// InspectDoctorRepairs reports recognized compatibility work for a local
// Store. It also reports an interrupted repair so doctor can resume it.
func InspectDoctorRepairs(ctx context.Context, storeDir string) ([]DoctorRepairPlan, error) {
	store := doctorSessionStore(storeDir)
	pending, err := store.PendingWorkspaceKeyRepairs(ctx)
	if err != nil {
		return nil, fmt.Errorf("gatewayapp: inspect pending doctor repair: %w", err)
	}
	if len(pending) > 0 {
		return []DoctorRepairPlan{{
			Code: DoctorWorkspaceIdentityConflict, AffectedSessions: len(pending),
		}}, nil
	}
	repairs, conflicts, err := planWorkspaceIdentityRepairs(ctx, store)
	if err != nil {
		return nil, err
	}
	if len(repairs) == 0 {
		return nil, nil
	}
	return []DoctorRepairPlan{{
		Code:                     DoctorWorkspaceIdentityConflict,
		ConflictingWorkspaceKeys: conflicts,
		AffectedSessions:         len(repairs),
	}}, nil
}

// RepairDoctorIssue pins exclusive ownership of the Store through planning,
// repair, and verification. The caller acquires ownership after stopping the Host.
func RepairDoctorIssue(ctx context.Context, storeDir string, code string, ownership *hostownership.Authority) (DoctorRepairReport, error) {
	if code != DoctorWorkspaceIdentityConflict {
		return DoctorRepairReport{}, fmt.Errorf("gatewayapp: doctor does not recognize repair %q", code)
	}
	release, ok := ownership.Pin(storeDir)
	if !ok {
		return DoctorRepairReport{}, errors.New("gatewayapp: doctor repair requires exclusive Store ownership")
	}
	defer release()
	store := doctorSessionStore(storeDir)
	pending, err := store.PendingWorkspaceKeyRepairs(ctx)
	if err != nil {
		return DoctorRepairReport{}, fmt.Errorf("gatewayapp: inspect pending doctor repair: %w", err)
	}
	repairs := pending
	conflicts := 0
	if len(repairs) == 0 {
		repairs, conflicts, err = planWorkspaceIdentityRepairs(ctx, store)
		if err != nil {
			return DoctorRepairReport{}, err
		}
	}
	report := DoctorRepairReport{
		Code: code, ConflictingWorkspaceKeys: conflicts, AffectedSessions: len(repairs),
	}
	if len(repairs) == 0 {
		return report, nil
	}
	stored, err := store.RepairWorkspaceKeys(ctx, repairs)
	if err != nil {
		return report, fmt.Errorf("gatewayapp: repair durable workspace identities: %w", err)
	}
	report.RepairedSessions = stored.RepairedSessions
	report.RepairedTasks = stored.RepairedTasks
	remaining, _, err := planWorkspaceIdentityRepairs(ctx, store)
	if err != nil {
		return report, fmt.Errorf("gatewayapp: verify durable workspace identities: %w", err)
	}
	if len(remaining) > 0 {
		return report, errors.New("gatewayapp: durable workspace identity repair did not converge")
	}
	return report, nil
}

// Doctor and the product Host hold the same exclusive ownership guard, so the
// journal cannot change during the lifetime of a Host's Runtime registry.
func pendingWorkspaceRepairKeys(ctx context.Context, sessions session.Lifecycle) (map[string]string, error) {
	store, ok := sessions.(*sessionfile.Store)
	if !ok {
		return nil, nil
	}
	repairs, err := store.PendingWorkspaceKeyRepairs(ctx)
	if err != nil {
		return nil, fmt.Errorf("gatewayapp: inspect pending workspace repairs: %w", err)
	}
	keys := make(map[string]string, len(repairs))
	for _, repair := range repairs {
		keys[repair.SessionID] = repair.ReplacementWorkspaceKey
	}
	return keys, nil
}

func doctorSessionStore(storeDir string) *sessionfile.Store {
	return sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(filepath.Clean(storeDir), "sessions")})
}

func planWorkspaceIdentityRepairs(
	ctx context.Context,
	store *sessionfile.Store,
) ([]sessionfile.WorkspaceKeyRepair, int, error) {
	type member struct{ sessionID, cwd string }
	groups := make(map[string][]member)
	err := visitStoredWorkspaces(ctx, store, func(id string, workspace session.WorkspaceRef) {
		groups[workspace.Key] = append(groups[workspace.Key], member{id, workspace.CWD})
	})
	if err != nil {
		return nil, 0, err
	}
	var keys []string
	for key, members := range groups {
		for _, m := range members[1:] {
			if m.cwd != members[0].cwd {
				keys = append(keys, key)
				break
			}
		}
	}
	conflicts := len(keys)
	var repairs []sessionfile.WorkspaceKeyRepair
	// Follow occupied replacement keys. Each group is processed once, including
	// when a chain leads back to a key that is already part of the repair.
	for len(keys) > 0 {
		key := keys[0]
		keys = keys[1:]
		members := groups[key]
		delete(groups, key)
		for _, m := range members {
			replacement := workspaceidentity.KeyForDirectory(m.cwd)
			if replacement == key {
				continue
			}
			repairs = append(repairs, sessionfile.WorkspaceKeyRepair{
				SessionID: m.sessionID, ExpectedWorkspaceKey: key, ReplacementWorkspaceKey: replacement,
			})
			if len(groups[replacement]) > 0 {
				keys = append(keys, replacement)
			}
		}
	}
	sort.Slice(repairs, func(i, j int) bool { return repairs[i].SessionID < repairs[j].SessionID })
	return repairs, conflicts, nil
}
