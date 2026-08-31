package gatewayapp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	controlagents "github.com/caelis-labs/caelis/control/agents"
)

const (
	acpPreparationSchemaVersion   = 1
	acpPreparationTTL             = 30 * time.Minute
	maxACPPreparationDocumentSize = 1 << 20
	maxACPPreparationDocuments    = 4096
)

var (
	errACPPreparationNotFound   = errors.New("gatewayapp: ACP preparation not found")
	errACPPreparationConflict   = errors.New("gatewayapp: ACP preparation content conflict")
	errACPPreparationOwner      = errors.New("gatewayapp: ACP preparation ownership conflict")
	errACPPreparationExpired    = errors.New("gatewayapp: ACP preparation expired")
	errACPPreparationAmbiguous  = errors.New("gatewayapp: multiple ACP preparations match one intent")
	errACPPreparationInvalidRef = errors.New("gatewayapp: invalid ACP preparation reference")
)

// acpPreparationStore is the Host-private durable staging area for one ACP
// onboarding intent. Rows share the physical Control database but remain a
// distinct domain receipt, not a second operation ledger.
type acpPreparationStore struct {
	db  *sql.DB
	now func() time.Time
}

type acpPreparationDiskRecord struct {
	Version      int                          `json:"version"`
	PrincipalID  string                       `json:"principal_id"`
	OperationID  string                       `json:"operation_id"`
	IntentDigest string                       `json:"intent_digest"`
	Preparation  controlagents.ACPPreparation `json:"preparation"`
}

type acpPreparationRow struct {
	ref           string
	principalID   string
	operationID   string
	intentDigest  string
	contentDigest string
	expiresAtNS   int64
	raw           []byte
	preparation   controlagents.ACPPreparation
}

type acpPreparationRowScanner interface {
	Scan(...any) error
}

func newACPPreparationStore(storeDir string) (*acpPreparationStore, error) {
	db, _, err := openControlStoreDatabase(storeDir)
	if err != nil {
		return nil, err
	}
	store := &acpPreparationStore{db: db, now: time.Now}
	if err := store.initialize(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *acpPreparationStore) initialize(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS control_acp_preparations (
	ref TEXT PRIMARY KEY,
	principal_id TEXT NOT NULL,
	operation_id TEXT NOT NULL,
	intent_digest TEXT NOT NULL,
	content_digest TEXT NOT NULL,
	expires_at_ns INTEGER NOT NULL,
	record_json BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS control_acp_preparations_intent_idx
	ON control_acp_preparations(principal_id, operation_id, intent_digest, expires_at_ns);
CREATE INDEX IF NOT EXISTS control_acp_preparations_expiry_idx
	ON control_acp_preparations(expires_at_ns);
`); err != nil {
		return fmt.Errorf("gatewayapp: initialize ACP preparation schema: %w", err)
	}
	now := s.currentTime()
	if err := s.pruneExpired(ctx, now); err != nil {
		return err
	}
	return nil
}

func (s *acpPreparationStore) CreatePlanned(ctx context.Context, record controlagents.ACPPreparation) (controlagents.ACPPreparation, error) {
	if s == nil || s.db == nil {
		return controlagents.ACPPreparation{}, errors.New("gatewayapp: ACP preparation store is unavailable")
	}
	ctx = acpPreparationContextOrBackground(ctx)
	if err := ctx.Err(); err != nil {
		return controlagents.ACPPreparation{}, err
	}
	record = controlagents.NormalizeACPPreparation(record)
	if record.Ref != "" || record.ContentDigest != "" || !record.CreatedAt.IsZero() || !record.ExpiresAt.IsZero() {
		return controlagents.ACPPreparation{}, errors.New("gatewayapp: ACP preparation identity, time window, and digest are store-owned")
	}
	if record.State != "" && record.State != controlagents.PreparationStatePlanned {
		return controlagents.ACPPreparation{}, errors.New("gatewayapp: CreatePlanned requires planned ACP preparation state")
	}
	record.State = controlagents.PreparationStatePlanned
	now := s.currentTime()
	if err := s.pruneExpired(ctx, now); err != nil {
		return controlagents.ACPPreparation{}, err
	}
	for range 4 {
		ref, err := newACPPreparationRef()
		if err != nil {
			return controlagents.ACPPreparation{}, err
		}
		candidate := record
		candidate.Ref = ref
		candidate.CreatedAt = now
		candidate.ExpiresAt = now.Add(acpPreparationTTL)
		candidate, err = controlagents.SealACPPreparation(candidate)
		if err != nil {
			return controlagents.ACPPreparation{}, err
		}
		raw, err := encodeACPPreparation(candidate)
		if err != nil {
			return controlagents.ACPPreparation{}, err
		}
		result, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO control_acp_preparations
	(ref, principal_id, operation_id, intent_digest, content_digest, expires_at_ns, record_json)
SELECT ?, ?, ?, ?, ?, ?, ?
WHERE (SELECT COUNT(*) FROM control_acp_preparations) < ?`, candidate.Ref, candidate.PrincipalID, candidate.OperationID,
			candidate.IntentDigest, candidate.ContentDigest, candidate.ExpiresAt.UnixNano(), raw, maxACPPreparationDocuments)
		if err != nil {
			return controlagents.ACPPreparation{}, err
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return controlagents.ACPPreparation{}, err
		}
		if inserted == 1 {
			return candidate, nil
		}
	}
	var live int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM control_acp_preparations`).Scan(&live); err != nil {
		return controlagents.ACPPreparation{}, err
	}
	if live >= maxACPPreparationDocuments {
		return controlagents.ACPPreparation{}, fmt.Errorf("gatewayapp: ACP preparation capacity %d reached", maxACPPreparationDocuments)
	}
	return controlagents.ACPPreparation{}, errors.New("gatewayapp: allocate unique ACP preparation reference")
}

func (s *acpPreparationStore) Get(ctx context.Context, ref string) (controlagents.ACPPreparation, error) {
	if s == nil || s.db == nil {
		return controlagents.ACPPreparation{}, errors.New("gatewayapp: ACP preparation store is unavailable")
	}
	ctx = acpPreparationContextOrBackground(ctx)
	ref = strings.TrimSpace(ref)
	if err := validateACPPreparationStoreRef(ref); err != nil {
		return controlagents.ACPPreparation{}, err
	}
	preparation, err := s.load(ctx, ref)
	if err != nil {
		return controlagents.ACPPreparation{}, err
	}
	if !preparation.ExpiresAt.After(s.currentTime()) {
		return controlagents.ACPPreparation{}, errACPPreparationExpired
	}
	return preparation, nil
}

func (s *acpPreparationStore) FindByIntent(
	ctx context.Context,
	principalID string,
	operationID string,
	intentDigest string,
) (controlagents.ACPPreparation, bool, error) {
	if s == nil || s.db == nil {
		return controlagents.ACPPreparation{}, false, errors.New("gatewayapp: ACP preparation store is unavailable")
	}
	ctx = acpPreparationContextOrBackground(ctx)
	principalID = strings.TrimSpace(principalID)
	operationID = strings.TrimSpace(operationID)
	intentDigest = strings.ToLower(strings.TrimSpace(intentDigest))
	if principalID == "" || operationID == "" || !validACPPreparationDigest(intentDigest) {
		return controlagents.ACPPreparation{}, false, errors.New("gatewayapp: principal, operation, and intent digest are required")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT ref, principal_id, operation_id, intent_digest, content_digest, expires_at_ns, record_json
FROM control_acp_preparations ORDER BY ref`)
	if err != nil {
		return controlagents.ACPPreparation{}, false, err
	}
	defer rows.Close()
	var matched controlagents.ACPPreparation
	now := s.currentTime()
	for rows.Next() {
		row, err := scanACPPreparationRow(rows)
		if err != nil {
			return controlagents.ACPPreparation{}, false, err
		}
		preparation := row.preparation
		if preparation.PrincipalID != principalID || preparation.OperationID != operationID ||
			preparation.IntentDigest != intentDigest || !preparation.ExpiresAt.After(now) {
			continue
		}
		if matched.Ref != "" {
			return controlagents.ACPPreparation{}, false, errACPPreparationAmbiguous
		}
		matched = preparation
	}
	if err := rows.Err(); err != nil {
		return controlagents.ACPPreparation{}, false, err
	}
	return matched, matched.Ref != "", nil
}

func (s *acpPreparationStore) referencesLauncherRoot(ctx context.Context, root string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("gatewayapp: ACP preparation store is unavailable")
	}
	ctx = acpPreparationContextOrBackground(ctx)
	rows, err := s.db.QueryContext(ctx, `
SELECT ref, principal_id, operation_id, intent_digest, content_digest, expires_at_ns, record_json
FROM control_acp_preparations ORDER BY ref`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	now := s.currentTime()
	for rows.Next() {
		row, err := scanACPPreparationRow(rows)
		if err != nil {
			return false, err
		}
		preparation := row.preparation
		if !preparation.ExpiresAt.After(now) {
			continue
		}
		if externalAgentConfigurationReferencesRoot(controlagents.Configuration{
			Connections: []controlagents.Connection{preparation.Connection},
		}, root) || textReferencesRoot(preparation.Request.CWD, root) ||
			textReferencesRoot(preparation.Request.CommandLine, root) {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return false, nil
}

func (s *acpPreparationStore) Save(
	ctx context.Context,
	expectedContentDigest string,
	record controlagents.ACPPreparation,
) (controlagents.ACPPreparation, error) {
	if s == nil || s.db == nil {
		return controlagents.ACPPreparation{}, errors.New("gatewayapp: ACP preparation store is unavailable")
	}
	ctx = acpPreparationContextOrBackground(ctx)
	expectedContentDigest = strings.ToLower(strings.TrimSpace(expectedContentDigest))
	if !validACPPreparationDigest(expectedContentDigest) {
		return controlagents.ACPPreparation{}, errors.New("gatewayapp: expected ACP preparation content digest is required")
	}
	record = controlagents.NormalizeACPPreparation(record)
	if err := validateACPPreparationStoreRef(record.Ref); err != nil {
		return controlagents.ACPPreparation{}, err
	}
	current, err := s.Get(ctx, record.Ref)
	if err != nil {
		return controlagents.ACPPreparation{}, err
	}
	if current.ContentDigest != expectedContentDigest {
		return controlagents.ACPPreparation{}, errACPPreparationConflict
	}
	if !sameACPPreparationOwnership(current, record) {
		return controlagents.ACPPreparation{}, errACPPreparationOwner
	}
	if !validACPPreparationTransition(current.State, record.State) {
		return controlagents.ACPPreparation{}, fmt.Errorf("gatewayapp: invalid ACP preparation transition %q -> %q", current.State, record.State)
	}
	sealed, err := controlagents.SealACPPreparation(record)
	if err != nil {
		return controlagents.ACPPreparation{}, err
	}
	if sealed.ContentDigest == current.ContentDigest {
		return current, nil
	}
	raw, err := encodeACPPreparation(sealed)
	if err != nil {
		return controlagents.ACPPreparation{}, err
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE control_acp_preparations
SET content_digest = ?, record_json = ?
WHERE ref = ? AND content_digest = ?`, sealed.ContentDigest, raw, sealed.Ref, expectedContentDigest)
	if err != nil {
		return controlagents.ACPPreparation{}, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return controlagents.ACPPreparation{}, err
	}
	if updated != 1 {
		return controlagents.ACPPreparation{}, errACPPreparationConflict
	}
	return sealed, nil
}

func (s *acpPreparationStore) load(ctx context.Context, ref string) (controlagents.ACPPreparation, error) {
	row, err := scanACPPreparationRow(s.db.QueryRowContext(ctx, `
SELECT ref, principal_id, operation_id, intent_digest, content_digest, expires_at_ns, record_json
FROM control_acp_preparations WHERE ref = ?`, ref))
	if errors.Is(err, sql.ErrNoRows) {
		return controlagents.ACPPreparation{}, errACPPreparationNotFound
	}
	if err != nil {
		return controlagents.ACPPreparation{}, err
	}
	return row.preparation, nil
}

func scanACPPreparationRow(scanner acpPreparationRowScanner) (acpPreparationRow, error) {
	var row acpPreparationRow
	if err := scanner.Scan(
		&row.ref, &row.principalID, &row.operationID, &row.intentDigest,
		&row.contentDigest, &row.expiresAtNS, &row.raw,
	); err != nil {
		return acpPreparationRow{}, err
	}
	preparation, err := decodeACPPreparation(row.raw)
	if err != nil {
		return acpPreparationRow{}, err
	}
	if preparation.Ref != row.ref || preparation.PrincipalID != row.principalID ||
		preparation.OperationID != row.operationID || preparation.IntentDigest != row.intentDigest ||
		preparation.ContentDigest != row.contentDigest || preparation.ExpiresAt.UnixNano() != row.expiresAtNS {
		return acpPreparationRow{}, errors.New("gatewayapp: ACP preparation indexes do not match the stored record")
	}
	row.preparation = preparation
	row.raw = append([]byte(nil), row.raw...)
	return row, nil
}

func (s *acpPreparationStore) pruneExpired(ctx context.Context, now time.Time) error {
	rows, err := s.db.QueryContext(ctx, `
SELECT ref, principal_id, operation_id, intent_digest, content_digest, expires_at_ns, record_json
FROM control_acp_preparations ORDER BY ref`)
	if err != nil {
		return err
	}
	type expiredRow struct {
		ref string
		raw []byte
	}
	expired := make([]expiredRow, 0)
	for rows.Next() {
		row, err := scanACPPreparationRow(rows)
		if err != nil {
			rows.Close()
			return fmt.Errorf("gatewayapp: validate ACP preparation before expiry cleanup: %w", err)
		}
		if !row.preparation.ExpiresAt.After(now) {
			expired = append(expired, expiredRow{ref: row.ref, raw: row.raw})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, row := range expired {
		if _, err := s.db.ExecContext(ctx, `
DELETE FROM control_acp_preparations WHERE ref = ? AND record_json = ?`, row.ref, row.raw); err != nil {
			return err
		}
	}
	return nil
}

func encodeACPPreparation(preparation controlagents.ACPPreparation) ([]byte, error) {
	if err := controlagents.ValidateACPPreparation(preparation); err != nil {
		return nil, err
	}
	disk := acpPreparationDiskRecord{
		Version: acpPreparationSchemaVersion, PrincipalID: preparation.PrincipalID,
		OperationID: preparation.OperationID, IntentDigest: preparation.IntentDigest, Preparation: preparation,
	}
	raw, err := json.Marshal(disk)
	if err != nil {
		return nil, fmt.Errorf("gatewayapp: encode ACP preparation: %w", err)
	}
	if len(raw) > maxACPPreparationDocumentSize {
		return nil, errors.New("gatewayapp: ACP preparation exceeds size limit")
	}
	return raw, nil
}

func decodeACPPreparation(raw []byte) (controlagents.ACPPreparation, error) {
	if len(raw) > maxACPPreparationDocumentSize {
		return controlagents.ACPPreparation{}, errors.New("gatewayapp: ACP preparation exceeds size limit")
	}
	var disk acpPreparationDiskRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&disk); err != nil {
		return controlagents.ACPPreparation{}, fmt.Errorf("gatewayapp: decode ACP preparation: %w", err)
	}
	if err := requireACPPreparationJSONEOF(decoder); err != nil {
		return controlagents.ACPPreparation{}, err
	}
	if disk.Version != acpPreparationSchemaVersion {
		return controlagents.ACPPreparation{}, fmt.Errorf("gatewayapp: unsupported ACP preparation version %d", disk.Version)
	}
	preparation := disk.Preparation
	preparation.PrincipalID = strings.TrimSpace(disk.PrincipalID)
	preparation.OperationID = strings.TrimSpace(disk.OperationID)
	preparation.IntentDigest = strings.ToLower(strings.TrimSpace(disk.IntentDigest))
	preparation = controlagents.NormalizeACPPreparation(preparation)
	if err := controlagents.ValidateACPPreparation(preparation); err != nil {
		return controlagents.ACPPreparation{}, fmt.Errorf("gatewayapp: validate ACP preparation: %w", err)
	}
	return preparation, nil
}

func (s *acpPreparationStore) currentTime() time.Time {
	now := time.Now
	if s != nil && s.now != nil {
		now = s.now
	}
	return now().UTC().Round(0)
}

func (s *acpPreparationStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

func acpPreparationContextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func newACPPreparationRef() (string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", fmt.Errorf("gatewayapp: generate ACP preparation reference: %w", err)
	}
	return "acpp_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func validateACPPreparationStoreRef(ref string) error {
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(ref, "acpp_") {
		return errACPPreparationInvalidRef
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(ref, "acpp_"))
	if err != nil || len(raw) != 32 {
		return errACPPreparationInvalidRef
	}
	return nil
}

func validACPPreparationDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func sameACPPreparationOwnership(current, next controlagents.ACPPreparation) bool {
	current = controlagents.NormalizeACPPreparation(current)
	next = controlagents.NormalizeACPPreparation(next)
	if current.Ref != next.Ref || current.PrincipalID != next.PrincipalID || current.OperationID != next.OperationID ||
		current.IntentDigest != next.IntentDigest || current.ParentRef != next.ParentRef ||
		!reflect.DeepEqual(current.Request, next.Request) || current.ObservedRevision != next.ObservedRevision ||
		!current.CreatedAt.Equal(next.CreatedAt) || !current.ExpiresAt.Equal(next.ExpiresAt) {
		return false
	}
	if current.State == controlagents.PreparationStatePlanned && !storedACPPreparationConnectionPresent(current.Connection) {
		return true
	}
	currentConnection := current.Connection
	nextConnection := next.Connection
	currentConnection.Authentication = controlagents.Authentication{}
	nextConnection.Authentication = controlagents.Authentication{}
	return reflect.DeepEqual(currentConnection, nextConnection)
}

func storedACPPreparationConnectionPresent(connection controlagents.Connection) bool {
	return strings.TrimSpace(connection.ID) != "" || strings.TrimSpace(connection.Launcher.Command) != "" ||
		strings.TrimSpace(connection.Launcher.AdapterID) != ""
}

func validACPPreparationTransition(current, next controlagents.PreparationState) bool {
	if current == next {
		return true
	}
	switch current {
	case controlagents.PreparationStatePlanned:
		return next == controlagents.PreparationStateNeedsAuth || next == controlagents.PreparationStateReady
	case controlagents.PreparationStateNeedsAuth:
		return next == controlagents.PreparationStateReady
	default:
		return false
	}
}

func requireACPPreparationJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("gatewayapp: ACP preparation contains trailing JSON")
		}
		return fmt.Errorf("gatewayapp: decode ACP preparation trailing data: %w", err)
	}
	return nil
}
