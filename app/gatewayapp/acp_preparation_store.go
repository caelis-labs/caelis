package gatewayapp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	controlagents "github.com/caelis-labs/caelis/control/agents"
)

const (
	acpPreparationDirectory       = "acp-preparations"
	acpPreparationLockFilename    = ".preparations.lock"
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
// onboarding intent. It is not a second roster or operation ledger.
type acpPreparationStore struct {
	root string
	now  func() time.Time
}

type acpPreparationDiskRecord struct {
	Version      int                          `json:"version"`
	PrincipalID  string                       `json:"principal_id"`
	OperationID  string                       `json:"operation_id"`
	IntentDigest string                       `json:"intent_digest"`
	Preparation  controlagents.ACPPreparation `json:"preparation"`
}

func newACPPreparationStore(storeDir string) (*acpPreparationStore, error) {
	storeDir = strings.TrimSpace(storeDir)
	if storeDir == "" {
		return nil, errors.New("gatewayapp: ACP preparation store directory is required")
	}
	absolute, err := filepath.Abs(storeDir)
	if err != nil {
		return nil, fmt.Errorf("gatewayapp: resolve ACP preparation store directory: %w", err)
	}
	return &acpPreparationStore{
		root: filepath.Join(filepath.Clean(absolute), acpPreparationDirectory),
		now:  time.Now,
	}, nil
}

// CreatePlanned creates one server-owned opaque preparation identity. Caller
// ownership and the observed Host revision must already be bound to record.
func (s *acpPreparationStore) CreatePlanned(ctx context.Context, record controlagents.ACPPreparation) (controlagents.ACPPreparation, error) {
	if s == nil {
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

	var created controlagents.ACPPreparation
	err := s.withLock(ctx, func() error {
		now := s.currentTime()
		live, err := s.pruneExpiredLocked(ctx, now)
		if err != nil {
			return err
		}
		if live >= maxACPPreparationDocuments {
			return fmt.Errorf("gatewayapp: ACP preparation capacity %d reached", maxACPPreparationDocuments)
		}
		for range 4 {
			if err := ctx.Err(); err != nil {
				return err
			}
			ref, err := newACPPreparationRef()
			if err != nil {
				return err
			}
			candidate := record
			candidate.Ref = ref
			candidate.CreatedAt = now
			candidate.ExpiresAt = now.Add(acpPreparationTTL)
			candidate, err = controlagents.SealACPPreparation(candidate)
			if err != nil {
				return err
			}
			path := s.path(candidate.Ref)
			if _, statErr := os.Lstat(path); statErr == nil {
				continue
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return fmt.Errorf("gatewayapp: inspect ACP preparation destination: %w", statErr)
			}
			writeErr := s.writeLocked(ctx, candidate)
			if writeErr != nil && !configstore.WriteCommitted(writeErr) {
				return writeErr
			}
			created = candidate
			return writeErr
		}
		return errors.New("gatewayapp: allocate unique ACP preparation reference")
	})
	return created, err
}

func (s *acpPreparationStore) pruneExpiredLocked(ctx context.Context, now time.Time) (int, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return 0, fmt.Errorf("gatewayapp: enumerate ACP preparations for pruning: %w", err)
	}
	live := 0
	documents := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		documents++
		if documents > maxACPPreparationDocuments {
			return 0, fmt.Errorf("gatewayapp: ACP preparation document limit %d exceeded", maxACPPreparationDocuments)
		}
		if !validACPPreparationFilename(entry.Name()) {
			return 0, fmt.Errorf("gatewayapp: invalid ACP preparation filename %q", entry.Name())
		}
		path := filepath.Join(s.root, entry.Name())
		preparation, err := s.readPathLocked(ctx, path, "")
		if err != nil {
			return 0, err
		}
		if preparation.ExpiresAt.After(now) {
			live++
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return 0, fmt.Errorf("gatewayapp: remove expired ACP preparation: %w", err)
		}
	}
	return live, nil
}

// Get returns one detached preparation after validating its opaque path,
// persisted ownership, content digest, permissions, and expiration.
func (s *acpPreparationStore) Get(ctx context.Context, ref string) (controlagents.ACPPreparation, error) {
	if s == nil {
		return controlagents.ACPPreparation{}, errors.New("gatewayapp: ACP preparation store is unavailable")
	}
	ctx = acpPreparationContextOrBackground(ctx)
	if err := validateACPPreparationStoreRef(ref); err != nil {
		return controlagents.ACPPreparation{}, err
	}
	var preparation controlagents.ACPPreparation
	err := s.withLock(ctx, func() error {
		loaded, err := s.readLocked(ctx, strings.TrimSpace(ref))
		if err != nil {
			return err
		}
		if !loaded.ExpiresAt.After(s.currentTime()) {
			return errACPPreparationExpired
		}
		preparation = loaded
		return nil
	})
	return preparation, err
}

// FindByIntent returns the unique unexpired preparation owned by one exact
// command intent. Corrupt records and ambiguous matches fail closed so command
// recovery cannot silently choose a different durable effect receipt.
func (s *acpPreparationStore) FindByIntent(
	ctx context.Context,
	principalID string,
	operationID string,
	intentDigest string,
) (controlagents.ACPPreparation, bool, error) {
	if s == nil {
		return controlagents.ACPPreparation{}, false, errors.New("gatewayapp: ACP preparation store is unavailable")
	}
	ctx = acpPreparationContextOrBackground(ctx)
	principalID = strings.TrimSpace(principalID)
	operationID = strings.TrimSpace(operationID)
	intentDigest = strings.ToLower(strings.TrimSpace(intentDigest))
	if principalID == "" || operationID == "" || !validACPPreparationDigest(intentDigest) {
		return controlagents.ACPPreparation{}, false, errors.New("gatewayapp: principal, operation, and intent digest are required")
	}

	var matched controlagents.ACPPreparation
	err := s.withLock(ctx, func() error {
		entries, err := os.ReadDir(s.root)
		if err != nil {
			return fmt.Errorf("gatewayapp: enumerate ACP preparations: %w", err)
		}
		documents := 0
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			documents++
			if documents > maxACPPreparationDocuments {
				return fmt.Errorf("gatewayapp: ACP preparation document limit %d exceeded", maxACPPreparationDocuments)
			}
			if !validACPPreparationFilename(entry.Name()) {
				return fmt.Errorf("gatewayapp: invalid ACP preparation filename %q", entry.Name())
			}
			preparation, err := s.readPathLocked(ctx, filepath.Join(s.root, entry.Name()), "")
			if err != nil {
				return err
			}
			if !preparation.ExpiresAt.After(s.currentTime()) {
				continue
			}
			if preparation.PrincipalID != principalID || preparation.OperationID != operationID || preparation.IntentDigest != intentDigest {
				continue
			}
			if matched.Ref != "" {
				return errACPPreparationAmbiguous
			}
			matched = preparation
		}
		return nil
	})
	return matched, matched.Ref != "", err
}

// Save performs an exact content-digest update. Trusted ownership, parent,
// observed Host revision, launcher identity, creation time, and expiration are
// immutable across the preparation lifetime.
func (s *acpPreparationStore) Save(
	ctx context.Context,
	expectedContentDigest string,
	record controlagents.ACPPreparation,
) (controlagents.ACPPreparation, error) {
	if s == nil {
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

	var saved controlagents.ACPPreparation
	err := s.withLock(ctx, func() error {
		current, err := s.readLocked(ctx, record.Ref)
		if err != nil {
			return err
		}
		if !current.ExpiresAt.After(s.currentTime()) {
			return errACPPreparationExpired
		}
		if current.ContentDigest != expectedContentDigest {
			return errACPPreparationConflict
		}
		if !sameACPPreparationOwnership(current, record) {
			return errACPPreparationOwner
		}
		if !validACPPreparationTransition(current.State, record.State) {
			return fmt.Errorf("gatewayapp: invalid ACP preparation transition %q -> %q", current.State, record.State)
		}
		sealed, err := controlagents.SealACPPreparation(record)
		if err != nil {
			return err
		}
		if sealed.ContentDigest == current.ContentDigest {
			saved = current
			return nil
		}
		writeErr := s.writeLocked(ctx, sealed)
		if writeErr != nil && !configstore.WriteCommitted(writeErr) {
			return writeErr
		}
		saved = sealed
		return writeErr
	})
	return saved, err
}

func (s *acpPreparationStore) readLocked(ctx context.Context, ref string) (controlagents.ACPPreparation, error) {
	return s.readPathLocked(ctx, s.path(ref), ref)
}

func (s *acpPreparationStore) readPathLocked(
	ctx context.Context,
	path string,
	expectedRef string,
) (preparation controlagents.ACPPreparation, returnErr error) {
	if err := ctx.Err(); err != nil {
		return controlagents.ACPPreparation{}, err
	}
	file, err := openSecureACPPreparationFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return controlagents.ACPPreparation{}, errACPPreparationNotFound
	}
	if err != nil {
		return controlagents.ACPPreparation{}, fmt.Errorf("gatewayapp: open ACP preparation: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, file.Close())
	}()
	payload, err := io.ReadAll(io.LimitReader(file, maxACPPreparationDocumentSize+1))
	if err != nil {
		return controlagents.ACPPreparation{}, fmt.Errorf("gatewayapp: read ACP preparation: %w", err)
	}
	if len(payload) > maxACPPreparationDocumentSize {
		return controlagents.ACPPreparation{}, errors.New("gatewayapp: ACP preparation exceeds size limit")
	}
	var disk acpPreparationDiskRecord
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
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
	preparation = disk.Preparation
	preparation.PrincipalID = strings.TrimSpace(disk.PrincipalID)
	preparation.OperationID = strings.TrimSpace(disk.OperationID)
	preparation.IntentDigest = strings.ToLower(strings.TrimSpace(disk.IntentDigest))
	preparation = controlagents.NormalizeACPPreparation(preparation)
	if (expectedRef != "" && preparation.Ref != expectedRef) || filepath.Clean(s.path(preparation.Ref)) != filepath.Clean(path) {
		return controlagents.ACPPreparation{}, errors.New("gatewayapp: ACP preparation does not match its opaque path")
	}
	if err := controlagents.ValidateACPPreparation(preparation); err != nil {
		return controlagents.ACPPreparation{}, fmt.Errorf("gatewayapp: validate ACP preparation: %w", err)
	}
	return preparation, nil
}

func (s *acpPreparationStore) writeLocked(ctx context.Context, preparation controlagents.ACPPreparation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := controlagents.ValidateACPPreparation(preparation); err != nil {
		return err
	}
	path := s.path(preparation.Ref)
	if info, err := os.Lstat(path); err == nil {
		if err := validateACPPreparationFileInfo(path, info, false); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("gatewayapp: inspect ACP preparation file: %w", err)
	}
	disk := acpPreparationDiskRecord{
		Version:      acpPreparationSchemaVersion,
		PrincipalID:  preparation.PrincipalID,
		OperationID:  preparation.OperationID,
		IntentDigest: preparation.IntentDigest,
		Preparation:  preparation,
	}
	payload, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return fmt.Errorf("gatewayapp: encode ACP preparation: %w", err)
	}
	payload = append(payload, '\n')
	if len(payload) > maxACPPreparationDocumentSize {
		return errors.New("gatewayapp: ACP preparation exceeds size limit")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := configstore.AtomicWriteFile(path, payload, 0o600, configstore.AtomicWriteOps{}); err != nil {
		return fmt.Errorf("gatewayapp: persist ACP preparation: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("gatewayapp: inspect persisted ACP preparation: %w", err)
	}
	return validateACPPreparationFileInfo(path, info, false)
}

func (s *acpPreparationStore) withLock(ctx context.Context, fn func() error) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	processLock := acpPreparationProcessLockFor(s.root)
	if err := processLock.lock(ctx); err != nil {
		return err
	}
	defer processLock.unlock()
	if err := ensureACPPreparationRoot(s.root); err != nil {
		return err
	}
	lock, err := acquireACPPreparationFileLock(ctx, filepath.Join(s.root, acpPreparationLockFilename))
	if err != nil {
		return fmt.Errorf("gatewayapp: lock ACP preparation store: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, lock.Close())
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ensureACPPreparationRoot(s.root); err != nil {
		return err
	}
	return fn()
}

func (s *acpPreparationStore) path(ref string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(ref)))
	return filepath.Join(s.root, hex.EncodeToString(digest[:])+".json")
}

func (s *acpPreparationStore) currentTime() time.Time {
	now := time.Now
	if s != nil && s.now != nil {
		now = s.now
	}
	return now().UTC().Round(0)
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
	if current.Ref != next.Ref ||
		current.PrincipalID != next.PrincipalID ||
		current.OperationID != next.OperationID ||
		current.IntentDigest != next.IntentDigest ||
		current.ParentRef != next.ParentRef ||
		!reflect.DeepEqual(current.Request, next.Request) ||
		current.ObservedRevision != next.ObservedRevision ||
		!current.CreatedAt.Equal(next.CreatedAt) ||
		!current.ExpiresAt.Equal(next.ExpiresAt) {
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
	return strings.TrimSpace(connection.ID) != "" || strings.TrimSpace(connection.Launcher.Command) != ""
}

func validACPPreparationFilename(name string) bool {
	if len(name) != sha256.Size*2+len(".json") || filepath.Ext(name) != ".json" {
		return false
	}
	digest := strings.TrimSuffix(name, ".json")
	return validACPPreparationDigest(digest)
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

func ensureACPPreparationRoot(root string) error {
	storeDir := filepath.Dir(root)
	if info, err := os.Lstat(storeDir); err == nil {
		if err := validateACPPreparationFileInfo(storeDir, info, true); err != nil {
			return err
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(storeDir, 0o700); err != nil {
			return fmt.Errorf("gatewayapp: create ACP preparation store directory: %w", err)
		}
	} else {
		return fmt.Errorf("gatewayapp: inspect ACP preparation store directory: %w", err)
	}
	if info, err := os.Lstat(root); err == nil {
		return validateACPPreparationFileInfo(root, info, true)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("gatewayapp: inspect ACP preparation directory: %w", err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		return fmt.Errorf("gatewayapp: create ACP preparation directory: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("gatewayapp: inspect created ACP preparation directory: %w", err)
	}
	return validateACPPreparationFileInfo(root, info, true)
}

func validateACPPreparationFileInfo(path string, info os.FileInfo, directory bool) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("gatewayapp: ACP preparation path %q must not be a symlink", path)
	}
	if directory {
		if !info.IsDir() {
			return fmt.Errorf("gatewayapp: ACP preparation path %q is not a directory", path)
		}
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("gatewayapp: ACP preparation path %q is not a regular file", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("gatewayapp: ACP preparation path %q has unsafe permissions %04o", path, info.Mode().Perm())
	}
	return nil
}

func openSecureACPPreparationFile(path string) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if err := validateACPPreparationFileInfo(path, before, false); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	opened, statErr := file.Stat()
	after, lstatErr := os.Lstat(path)
	if statErr != nil || lstatErr != nil || !os.SameFile(opened, after) {
		_ = file.Close()
		return nil, errors.Join(errors.New("gatewayapp: ACP preparation changed while opening"), statErr, lstatErr)
	}
	if err := validateACPPreparationFileInfo(path, after, false); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func openACPPreparationLockFile(path string) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil {
		if err := validateACPPreparationFileInfo(path, info, false); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	opened, statErr := file.Stat()
	after, lstatErr := os.Lstat(path)
	if statErr != nil || lstatErr != nil || !os.SameFile(opened, after) {
		_ = file.Close()
		return nil, errors.Join(errors.New("gatewayapp: ACP preparation lock changed while opening"), statErr, lstatErr)
	}
	if err := validateACPPreparationFileInfo(path, after, false); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

var acpPreparationProcessLocks sync.Map

type acpPreparationProcessLock struct {
	once  sync.Once
	token chan struct{}
}

func acpPreparationProcessLockFor(root string) *acpPreparationProcessLock {
	value, _ := acpPreparationProcessLocks.LoadOrStore(filepath.Clean(root), &acpPreparationProcessLock{})
	return value.(*acpPreparationProcessLock)
}

func (l *acpPreparationProcessLock) lock(ctx context.Context) error {
	l.once.Do(func() {
		l.token = make(chan struct{}, 1)
		l.token <- struct{}{}
	})
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.token:
		if err := ctx.Err(); err != nil {
			l.token <- struct{}{}
			return err
		}
		return nil
	}
}

func (l *acpPreparationProcessLock) unlock() {
	l.token <- struct{}{}
}
