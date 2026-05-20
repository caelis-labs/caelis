package setupstate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const CurrentSetupVersion = 2

type Dirs struct {
	Root              string
	Sandbox           string
	Bin               string
	Secrets           string
	Logs              string
	Reset             string
	MarkerPath        string
	ErrorPath         string
	ResetErrorPath    string
	ProgressPath      string
	ResetProgressPath string
	UsersPath         string
	CapPath           string
	WorkspacePath     string
}

type Marker struct {
	Version         int       `json:"version"`
	RunnerHash      string    `json:"runner_hash,omitempty"`
	PolicyHash      string    `json:"policy_hash,omitempty"`
	OfflineUsername string    `json:"offline_username,omitempty"`
	OnlineUsername  string    `json:"online_username,omitempty"`
	OwnerUsername   string    `json:"owner_username,omitempty"`
	CreatedAt       time.Time `json:"created_at,omitempty"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
}

type ErrorReport struct {
	Phase   string    `json:"phase,omitempty"`
	Code    string    `json:"code,omitempty"`
	Message string    `json:"message,omitempty"`
	Time    time.Time `json:"time,omitempty"`
}

type ProgressReport struct {
	Phase   string    `json:"phase,omitempty"`
	Message string    `json:"message,omitempty"`
	Step    int       `json:"step,omitempty"`
	Total   int       `json:"total,omitempty"`
	Done    bool      `json:"done,omitempty"`
	Debug   bool      `json:"debug,omitempty"`
	Time    time.Time `json:"time,omitempty"`
}

type WorkspaceRecord struct {
	Version                 int               `json:"version"`
	WorkspaceRoot           string            `json:"workspace_root,omitempty"`
	ReadRoots               []string          `json:"read_roots,omitempty"`
	WriteRoots              []string          `json:"write_roots,omitempty"`
	TraverseRoots           []string          `json:"traverse_roots,omitempty"`
	DenyReadPaths           []string          `json:"deny_read_paths,omitempty"`
	DenyWritePaths          []string          `json:"deny_write_paths,omitempty"`
	PolicyHash              string            `json:"policy_hash,omitempty"`
	CapabilitySIDs          []string          `json:"capability_sids,omitempty"`
	WriteRootCapabilitySIDs map[string]string `json:"write_root_capability_sids,omitempty"`
	OfflineUsername         string            `json:"offline_username,omitempty"`
	OnlineUsername          string            `json:"online_username,omitempty"`
	OwnerUsername           string            `json:"owner_username,omitempty"`
	SetupVersion            int               `json:"setup_version,omitempty"`
	UpdatedAt               time.Time         `json:"updated_at,omitempty"`
}

type Expectation struct {
	Version         int
	RunnerHash      string
	PolicyHash      string
	OfflineUsername string
	OnlineUsername  string
	OwnerUsername   string
}

type Freshness struct {
	Current bool
	Reason  string
}

func NewDirs(root string) Dirs {
	root = strings.TrimSpace(root)
	return Dirs{
		Root:              root,
		Sandbox:           filepath.Join(root, ".sandbox"),
		Bin:               filepath.Join(root, ".sandbox-bin"),
		Secrets:           filepath.Join(root, ".sandbox-secrets"),
		Logs:              filepath.Join(root, ".sandbox", "logs"),
		Reset:             filepath.Join(root, ".sandbox-reset"),
		MarkerPath:        filepath.Join(root, ".sandbox", "setup_marker.json"),
		ErrorPath:         filepath.Join(root, ".sandbox", "setup_error.json"),
		ResetErrorPath:    filepath.Join(root, ".sandbox-reset", "reset_error.json"),
		ProgressPath:      filepath.Join(root, ".sandbox", "setup_progress.json"),
		ResetProgressPath: filepath.Join(root, ".sandbox-reset", "reset_progress.json"),
		UsersPath:         filepath.Join(root, ".sandbox-secrets", "sandbox_users.json"),
		CapPath:           filepath.Join(root, ".sandbox", "cap_sids.json"),
		WorkspacePath:     filepath.Join(root, ".sandbox", "workspace_setup.json"),
	}
}

func EnsureDirs(dirs Dirs) error {
	for _, dir := range []string{dirs.Sandbox, dirs.Bin, dirs.Secrets, dirs.Logs} {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func ReadMarker(path string) (Marker, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Marker{}, err
	}
	var marker Marker
	if err := json.Unmarshal(data, &marker); err != nil {
		return Marker{}, fmt.Errorf("decode setup marker: %w", err)
	}
	return marker, nil
}

func WriteMarker(path string, marker Marker) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	now := time.Now().UTC()
	if marker.CreatedAt.IsZero() {
		marker.CreatedAt = now
	}
	marker.UpdatedAt = now
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".setup_marker.*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	committed = true
	return nil
}

func WriteError(path string, report ErrorReport) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if report.Time.IsZero() {
		report.Time = time.Now().UTC()
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func ReadError(path string) (ErrorReport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ErrorReport{}, err
	}
	var report ErrorReport
	if err := json.Unmarshal(data, &report); err != nil {
		return ErrorReport{}, err
	}
	return report, nil
}

func ClearError(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func WriteProgress(path string, report ProgressReport) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if report.Time.IsZero() {
		report.Time = time.Now().UTC()
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func ReadProgress(path string) (ProgressReport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ProgressReport{}, err
	}
	var report ProgressReport
	if err := json.Unmarshal(data, &report); err != nil {
		return ProgressReport{}, err
	}
	return report, nil
}

func ClearProgress(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func ReadWorkspace(path string) (WorkspaceRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return WorkspaceRecord{}, err
	}
	var record WorkspaceRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return WorkspaceRecord{}, fmt.Errorf("decode workspace setup state: %w", err)
	}
	return record, nil
}

func WriteWorkspace(path string, record WorkspaceRecord) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if record.Version == 0 {
		record.Version = 1
	}
	record.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".workspace_setup.*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	committed = true
	return nil
}

func CheckFreshness(marker Marker, expect Expectation) Freshness {
	if expect.Version == 0 {
		expect.Version = CurrentSetupVersion
	}
	if marker.Version != expect.Version {
		return Freshness{Reason: "setup version changed"}
	}
	if strings.TrimSpace(expect.RunnerHash) != "" && marker.RunnerHash != expect.RunnerHash {
		return Freshness{Reason: "runner hash changed"}
	}
	if strings.TrimSpace(expect.PolicyHash) != "" && marker.PolicyHash != expect.PolicyHash {
		return Freshness{Reason: "policy hash changed"}
	}
	if strings.TrimSpace(expect.OfflineUsername) != "" && !strings.EqualFold(marker.OfflineUsername, expect.OfflineUsername) {
		return Freshness{Reason: "offline sandbox user changed"}
	}
	if strings.TrimSpace(expect.OnlineUsername) != "" && !strings.EqualFold(marker.OnlineUsername, expect.OnlineUsername) {
		return Freshness{Reason: "online sandbox user changed"}
	}
	if strings.TrimSpace(expect.OwnerUsername) != "" && !strings.EqualFold(marker.OwnerUsername, expect.OwnerUsername) {
		return Freshness{Reason: "sandbox owner changed"}
	}
	return Freshness{Current: true}
}

func HashJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
