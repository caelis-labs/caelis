package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

// DoctorRepairPlan describes one recognized local compatibility repair without
// mutating the Store.
type DoctorRepairPlan struct {
	Code                     StartupIssueCode
	ConflictingWorkspaceKeys int
	AffectedSessions         int
}

// DoctorRepairReport describes one compatibility repair completed by doctor.
type DoctorRepairReport struct {
	Code                     StartupIssueCode
	ConflictingWorkspaceKeys int
	AffectedSessions         int
	RepairedSessions         int
	RepairedTasks            int
}

type durableWorkspaceIdentity struct {
	sessionID    string
	workspaceKey string
	cwd          string
}

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
			Code: StartupIssueWorkspaceIdentityConflict, AffectedSessions: len(pending),
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
		Code:                     StartupIssueWorkspaceIdentityConflict,
		ConflictingWorkspaceKeys: conflicts,
		AffectedSessions:         len(repairs),
	}}, nil
}

// RepairDoctorIssue performs one recognized compatibility repair. It is an
// explicit doctor operation and is never called by normal Host startup.
func RepairDoctorIssue(ctx context.Context, storeDir string, code StartupIssueCode) (DoctorRepairReport, error) {
	if code != StartupIssueWorkspaceIdentityConflict {
		return DoctorRepairReport{}, fmt.Errorf("gatewayapp: doctor does not recognize repair %q", code)
	}
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

func doctorSessionStore(storeDir string) *sessionfile.Store {
	return sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(filepath.Clean(storeDir), "sessions")})
}

func planWorkspaceIdentityRepairs(
	ctx context.Context,
	store *sessionfile.Store,
) ([]sessionfile.WorkspaceKeyRepair, int, error) {
	identities, err := listDurableWorkspaceIdentities(ctx, store)
	if err != nil {
		return nil, 0, err
	}
	byKey := map[string]map[string]bool{}
	for _, identity := range identities {
		if byKey[identity.workspaceKey] == nil {
			byKey[identity.workspaceKey] = map[string]bool{}
		}
		byKey[identity.workspaceKey][identity.cwd] = true
	}
	marked := map[string]bool{}
	conflicts := 0
	for key, cwds := range byKey {
		if len(cwds) <= 1 {
			continue
		}
		conflicts++
		for _, identity := range identities {
			if identity.workspaceKey == key {
				marked[identity.sessionID] = true
			}
		}
	}

	// A replacement key may itself be occupied by corrupt legacy data. Expand
	// the plan until every resulting key has exactly one CWD.
	for {
		resulting := map[string]map[string][]durableWorkspaceIdentity{}
		for _, identity := range identities {
			key := identity.workspaceKey
			if marked[identity.sessionID] {
				key = canonicalStoredWorkspaceKey(identity.cwd)
			}
			if resulting[key] == nil {
				resulting[key] = map[string][]durableWorkspaceIdentity{}
			}
			resulting[key][identity.cwd] = append(resulting[key][identity.cwd], identity)
		}
		changed := false
		for key, byCWD := range resulting {
			if len(byCWD) <= 1 {
				continue
			}
			for _, group := range byCWD {
				for _, identity := range group {
					if marked[identity.sessionID] {
						continue
					}
					replacement := canonicalStoredWorkspaceKey(identity.cwd)
					if replacement == key {
						return nil, conflicts, fmt.Errorf(
							"gatewayapp: Session %q has an irreparable durable workspace identity",
							identity.sessionID,
						)
					}
					marked[identity.sessionID] = true
					changed = true
				}
			}
		}
		if !changed {
			break
		}
	}

	repairs := make([]sessionfile.WorkspaceKeyRepair, 0, len(marked))
	for _, identity := range identities {
		if !marked[identity.sessionID] {
			continue
		}
		replacement := canonicalStoredWorkspaceKey(identity.cwd)
		if replacement == identity.workspaceKey {
			continue
		}
		repairs = append(repairs, sessionfile.WorkspaceKeyRepair{
			SessionID:               identity.sessionID,
			ExpectedWorkspaceKey:    identity.workspaceKey,
			ReplacementWorkspaceKey: replacement,
		})
	}
	sort.Slice(repairs, func(i, j int) bool { return repairs[i].SessionID < repairs[j].SessionID })
	return repairs, conflicts, nil
}

func listDurableWorkspaceIdentities(
	ctx context.Context,
	store session.Service,
) ([]durableWorkspaceIdentity, error) {
	var identities []durableWorkspaceIdentity
	cursor := ""
	for {
		listed, err := store.ListSessions(ctx, session.ListSessionsRequest{Cursor: cursor, Limit: 200})
		if err != nil {
			return nil, fmt.Errorf("gatewayapp: inspect durable workspace identities: %w", err)
		}
		for _, summary := range listed.Sessions {
			key := strings.TrimSpace(summary.WorkspaceKey)
			cwd := filepath.Clean(strings.TrimSpace(summary.CWD))
			if key == "" || cwd == "" || cwd == "." || !filepath.IsAbs(cwd) {
				return nil, fmt.Errorf("gatewayapp: Session %q has an invalid durable workspace identity", summary.SessionID)
			}
			identities = append(identities, durableWorkspaceIdentity{
				sessionID: strings.TrimSpace(summary.SessionID), workspaceKey: key, cwd: cwd,
			})
		}
		next := strings.TrimSpace(listed.NextCursor)
		if next == "" {
			return identities, nil
		}
		if next == cursor {
			return nil, errors.New("gatewayapp: durable Session listing did not advance")
		}
		cursor = next
	}
}

func canonicalStoredWorkspaceKey(cwd string) string {
	key := filepath.Clean(strings.TrimSpace(cwd))
	if key == string(filepath.Separator) {
		return "workspace:" + key
	}
	return key
}
