package controlserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/internal/productpaths"
)

const (
	discoveryFilename      = "discovery.json"
	DiscoverySchemaVersion = "caelis.control.service-discovery/v1"
	maxDiscoveryBytes      = 64 << 10
)

// DiscoveryRecord is credential-free metadata for locating one ready local
// Control Host. The bearer token remains in DefaultTokenFile.
type DiscoveryRecord struct {
	SchemaVersion       string    `json:"schema_version"`
	ServerID            string    `json:"server_id"`
	InstanceID          string    `json:"instance_id"`
	AppName             string    `json:"app_name"`
	PrincipalID         string    `json:"principal_id"`
	PID                 int       `json:"pid"`
	Endpoint            string    `json:"endpoint"`
	ProtocolVersion     int       `json:"protocol_version"`
	EnvelopeVersion     string    `json:"envelope_version"`
	APIVersion          string    `json:"api_version"`
	DistributionVersion string    `json:"distribution_version"`
	BuildID             string    `json:"build_id"`
	BuildKind           string    `json:"build_kind"`
	Capabilities        []string  `json:"capabilities"`
	Transports          []string  `json:"transports"`
	StartedAt           time.Time `json:"started_at"`
}

// DefaultDiscoveryFile returns the user-private local Host metadata path.
func DefaultDiscoveryFile(storeDir string) string {
	return filepath.Join(productpaths.ServiceRuntimeDir(storeDir), discoveryFilename)
}

// PublishDiscoveryRecord atomically publishes one ready Host instance. It may
// replace stale metadata only while the caller owns product Host authority.
func PublishDiscoveryRecord(path string, record DiscoveryRecord) error {
	path = filepath.Clean(path)
	if path == "." || path == string(filepath.Separator) {
		return errors.New("controlserver: discovery file path is required")
	}
	record, err := normalizeDiscoveryRecord(record)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("controlserver: create discovery directory: %w", err)
	}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("controlserver: encode discovery record: %w", err)
	}
	data = append(data, '\n')
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("controlserver: create discovery temporary file: %w", err)
	}
	temporaryPath := file.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := secureTokenFile(file); err != nil {
		_ = file.Close()
		return fmt.Errorf("controlserver: secure discovery temporary file: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("controlserver: write discovery record: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("controlserver: sync discovery record: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("controlserver: close discovery temporary file: %w", err)
	}
	if err := replaceDiscoveryFile(temporaryPath, path); err != nil {
		return fmt.Errorf("controlserver: publish discovery record: %w", err)
	}
	if err := syncTokenDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("controlserver: sync discovery directory: %w", err)
	}
	return nil
}

// LoadDiscoveryRecord reads one stable, current-user-only discovery snapshot.
func LoadDiscoveryRecord(path string) (DiscoveryRecord, error) {
	path = filepath.Clean(path)
	before, err := os.Lstat(path)
	if err != nil {
		return DiscoveryRecord{}, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return DiscoveryRecord{}, errors.New("controlserver: discovery file must be a regular non-symlink file")
	}
	file, err := os.Open(path)
	if err != nil {
		return DiscoveryRecord{}, fmt.Errorf("controlserver: open discovery file: %w", err)
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return DiscoveryRecord{}, fmt.Errorf("controlserver: stat discovery file: %w", err)
	}
	if !os.SameFile(before, after) || !after.Mode().IsRegular() {
		return DiscoveryRecord{}, errors.New("controlserver: discovery file changed while opening")
	}
	if err := validateTokenFileSecurity(file, after); err != nil {
		return DiscoveryRecord{}, fmt.Errorf("controlserver: insecure discovery file: %w", err)
	}
	limited := io.LimitReader(file, maxDiscoveryBytes+1)
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	var record DiscoveryRecord
	if err := decoder.Decode(&record); err != nil {
		return DiscoveryRecord{}, fmt.Errorf("controlserver: decode discovery record: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return DiscoveryRecord{}, errors.New("controlserver: discovery file contains trailing JSON")
		}
		return DiscoveryRecord{}, fmt.Errorf("controlserver: decode discovery trailing data: %w", err)
	}
	return normalizeDiscoveryRecord(record)
}

// RemoveDiscoveryRecord removes metadata only when it still identifies the
// caller's Host instance. A replacement Host can never be unpublished by an
// older process exiting late.
func RemoveDiscoveryRecord(path string, instanceID string) error {
	record, err := LoadDiscoveryRecord(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if record.InstanceID != strings.TrimSpace(instanceID) {
		return nil
	}
	if err := os.Remove(filepath.Clean(path)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("controlserver: remove discovery record: %w", err)
	}
	return nil
}

func normalizeDiscoveryRecord(record DiscoveryRecord) (DiscoveryRecord, error) {
	record.SchemaVersion = strings.TrimSpace(record.SchemaVersion)
	record.ServerID = strings.TrimSpace(record.ServerID)
	record.InstanceID = strings.TrimSpace(record.InstanceID)
	record.AppName = strings.TrimSpace(record.AppName)
	record.PrincipalID = strings.TrimSpace(record.PrincipalID)
	record.Endpoint = strings.TrimSpace(record.Endpoint)
	record.EnvelopeVersion = strings.TrimSpace(record.EnvelopeVersion)
	record.APIVersion = strings.TrimSpace(record.APIVersion)
	record.DistributionVersion = strings.TrimSpace(record.DistributionVersion)
	record.BuildID = strings.TrimSpace(record.BuildID)
	record.BuildKind = strings.TrimSpace(record.BuildKind)
	if record.SchemaVersion != DiscoverySchemaVersion {
		return DiscoveryRecord{}, fmt.Errorf("controlserver: unsupported discovery schema %q", record.SchemaVersion)
	}
	if record.ServerID != controlclient.ServerIdentity {
		return DiscoveryRecord{}, fmt.Errorf("controlserver: unsupported discovery server %q", record.ServerID)
	}
	if _, err := uuid.Parse(record.InstanceID); err != nil {
		return DiscoveryRecord{}, errors.New("controlserver: discovery instance ID is invalid")
	}
	if record.AppName == "" || record.PrincipalID == "" {
		return DiscoveryRecord{}, errors.New("controlserver: discovery Host scope is incomplete")
	}
	if record.PID <= 0 || record.StartedAt.IsZero() {
		return DiscoveryRecord{}, errors.New("controlserver: discovery process metadata is invalid")
	}
	if err := validateDiscoveryEndpoint(record.Endpoint); err != nil {
		return DiscoveryRecord{}, err
	}
	if record.ProtocolVersion <= 0 || record.EnvelopeVersion == "" || record.APIVersion == "" {
		return DiscoveryRecord{}, errors.New("controlserver: discovery protocol metadata is incomplete")
	}
	if record.DistributionVersion == "" || record.BuildID == "" || record.BuildKind == "" {
		return DiscoveryRecord{}, errors.New("controlserver: discovery build metadata is incomplete")
	}
	record.Capabilities = normalizeDiscoveryStrings(record.Capabilities)
	record.Transports = normalizeDiscoveryStrings(record.Transports)
	if len(record.Capabilities) == 0 || len(record.Transports) == 0 {
		return DiscoveryRecord{}, errors.New("controlserver: discovery capability metadata is incomplete")
	}
	endpoint, _ := url.Parse(record.Endpoint)
	if !slices.Contains(record.Transports, endpoint.Scheme) {
		return DiscoveryRecord{}, errors.New("controlserver: discovery transport does not match its endpoint")
	}
	return record, nil
}

func validateDiscoveryEndpoint(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.EscapedPath() != "" {
		return errors.New("controlserver: discovery endpoint must be an origin")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("controlserver: discovery endpoint must use http or https")
	}
	host, port, splitErr := net.SplitHostPort(parsed.Host)
	if splitErr != nil {
		return errors.New("controlserver: discovery endpoint must include an explicit port")
	}
	ip := net.ParseIP(strings.Trim(strings.TrimSpace(host), "[]"))
	portNumber, portErr := strconv.Atoi(port)
	if ip == nil || !ip.IsLoopback() || portErr != nil || portNumber <= 0 || portNumber > 65535 {
		return errors.New("controlserver: discovery endpoint must be loopback")
	}
	return nil
}

func normalizeDiscoveryStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !slices.Contains(result, value) {
			result = append(result, value)
		}
	}
	slices.Sort(result)
	return result
}
