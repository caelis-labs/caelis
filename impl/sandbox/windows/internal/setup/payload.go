package setup

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	winpolicy "github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/policy"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/setupstate"
)

const (
	GroupName      = "CaelisSandboxUsers"
	OfflineUser    = "CaelisSandboxOffline"
	OnlineUser     = "CaelisSandboxOnline"
	PayloadVersion = setupstate.CurrentSetupVersion
)

type Payload struct {
	Version         int              `json:"version"`
	StateRoot       string           `json:"state_root"`
	RunnerHash      string           `json:"runner_hash,omitempty"`
	PolicyHash      string           `json:"policy_hash,omitempty"`
	Policy          winpolicy.Policy `json:"policy"`
	OfflineUsername string           `json:"offline_username,omitempty"`
	OnlineUsername  string           `json:"online_username,omitempty"`
	OwnerUsername   string           `json:"owner_username,omitempty"`
	RefreshOnly     bool             `json:"refresh_only,omitempty"`
	ProgressPath    string           `json:"progress_path,omitempty"`
}

type UserSecret struct {
	Username          string `json:"username"`
	PasswordProtected string `json:"password_protected"`
}

type UsersFile struct {
	Offline UserSecret `json:"offline"`
	Online  UserSecret `json:"online"`
}

type Progress struct {
	Phase   string `json:"phase,omitempty"`
	Message string `json:"message,omitempty"`
	Step    int    `json:"step,omitempty"`
	Total   int    `json:"total,omitempty"`
	Done    bool   `json:"done,omitempty"`
}

type ProgressFunc func(Progress)

func (p Payload) Normalize() Payload {
	if p.Version == 0 {
		p.Version = PayloadVersion
	}
	if p.OfflineUsername == "" {
		p.OfflineUsername = OfflineUser
	}
	if p.OnlineUsername == "" {
		p.OnlineUsername = OnlineUser
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
