package setup

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	winpolicy "github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/policy"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/setupstate"
)

const (
	GroupName      = "CaelisSandboxUsers"
	OfflineUser    = "CaelisSandboxOffline"
	OnlineUser     = "CaelisSandboxOnline"
	PayloadVersion = setupstate.CurrentSetupVersion
)

type SetupKind string

const (
	SetupKindFull           SetupKind = "full"
	SetupKindWorkspaceOnly  SetupKind = "workspace_only"
	SetupKindRuntimeRefresh SetupKind = "runtime_refresh"
	SetupKindReadACLRefresh SetupKind = "read_acl_refresh"
	SetupKindReset          SetupKind = "reset"
)

type Payload struct {
	Version             int              `json:"version"`
	Kind                SetupKind        `json:"kind,omitempty"`
	OperationID         string           `json:"operation_id,omitempty"`
	StartedAt           time.Time        `json:"started_at,omitempty"`
	ExpiresAt           time.Time        `json:"expires_at,omitempty"`
	StateRoot           string           `json:"state_root"`
	RunnerHash          string           `json:"runner_hash,omitempty"`
	PolicyHash          string           `json:"policy_hash,omitempty"`
	GlobalPolicyHash    string           `json:"global_policy_hash,omitempty"`
	WorkspacePolicyHash string           `json:"workspace_policy_hash,omitempty"`
	GlobalPolicy        winpolicy.Policy `json:"global_policy,omitempty"`
	Policy              winpolicy.Policy `json:"policy"`
	OfflineUsername     string           `json:"offline_username,omitempty"`
	OnlineUsername      string           `json:"online_username,omitempty"`
	OwnerUsername       string           `json:"owner_username,omitempty"`
	WorkspaceRoot       string           `json:"workspace_root,omitempty"`
	WorkspaceStatePath  string           `json:"workspace_state_path,omitempty"`
	RefreshOnly         bool             `json:"refresh_only,omitempty"`
	ProgressPath        string           `json:"progress_path,omitempty"`
	Debug               bool             `json:"debug,omitempty"`
}

type UserSecret struct {
	Username          string `json:"username"`
	PasswordProtected string `json:"password_protected"`
}

type UsersFile struct {
	Offline UserSecret  `json:"offline"`
	Online  *UserSecret `json:"online,omitempty"`
}

type Progress struct {
	Phase   string `json:"phase,omitempty"`
	Message string `json:"message,omitempty"`
	Step    int    `json:"step,omitempty"`
	Total   int    `json:"total,omitempty"`
	Done    bool   `json:"done,omitempty"`
	Debug   bool   `json:"debug,omitempty"`
}

type ProgressFunc func(Progress)

func (p Payload) Normalize() Payload {
	if p.Version == 0 {
		p.Version = PayloadVersion
	}
	if p.Kind == "" {
		if p.RefreshOnly {
			p.Kind = SetupKindRuntimeRefresh
		} else {
			p.Kind = SetupKindFull
		}
	}
	if p.Kind == SetupKindRuntimeRefresh || p.Kind == SetupKindReadACLRefresh {
		p.RefreshOnly = true
	}
	if p.GlobalPolicyHash == "" {
		p.GlobalPolicyHash = p.PolicyHash
	}
	if p.WorkspacePolicyHash == "" && p.Kind != SetupKindRuntimeRefresh && p.Kind != SetupKindReadACLRefresh && p.Kind != SetupKindReset {
		p.WorkspacePolicyHash = p.PolicyHash
	}
	if p.PolicyHash == "" {
		p.PolicyHash = p.GlobalPolicyHash
	}
	if p.OfflineUsername == "" {
		p.OfflineUsername = OfflineUser
	}
	return p
}

func EncodePayload(payload Payload) (string, error) {
	payload = payload.Normalize()
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

func DecodePayload(encoded string) (Payload, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return Payload{}, fmt.Errorf("decode setup payload: %w", err)
	}
	var payload Payload
	if err := json.Unmarshal(data, &payload); err != nil {
		return Payload{}, fmt.Errorf("unmarshal setup payload: %w", err)
	}
	return payload.Normalize(), nil
}
