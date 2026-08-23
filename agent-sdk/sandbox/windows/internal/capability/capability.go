package capability

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/pathutil"
)

const (
	storeLockPollInterval = 10 * time.Millisecond
	storeLockTimeout      = 10 * time.Second
	storeReadRetryAfter   = 10 * time.Millisecond
	storeReadRetryTimeout = time.Second
)

const (
	windowsFileNotFound     syscall.Errno = 2
	windowsPathNotFound     syscall.Errno = 3
	windowsSharingViolation syscall.Errno = 32
	windowsLockViolation    syscall.Errno = 33
)

var storeLocks sync.Map

const StoreVersion = 2

// Scope identifies one workspace capability set. Every managed root uses the
// same Host+canonical-path identity regardless of whether a caller observes it
// as a workspace, private environment, or external root. A physical directory
// therefore cannot accumulate role-specific sandbox SIDs.
type Scope struct {
	HostUserSID    string
	WorkspaceRoot  string
	SandboxEnvRoot string
	WriteRoots     []string
}

// HostUserRotationError reports that a store belongs to a different Host
// user. Rotation must install the new identity and its ACLs before replacing
// the store and cleaning the previous identity.
type HostUserRotationError struct {
	StoredHostUserSID  string
	CurrentHostUserSID string
}

func (e *HostUserRotationError) Error() string {
	if e == nil {
		return "capability: Host user identity rotation is required"
	}
	return fmt.Sprintf("capability: store Host user SID %s does not match current Host user SID %s; identity rotation is required", e.StoredHostUserSID, e.CurrentHostUserSID)
}

type LegacyV1Store struct {
	WorkspaceByCWD     map[string]string `json:"workspace_by_cwd,omitempty"`
	WritableRootByPath map[string]string `json:"writable_root_by_path,omitempty"`
}

type Store struct {
	Version            int               `json:"version,omitempty"`
	HostUserSID        string            `json:"host_user_sid,omitempty"`
	WorkspaceByRoot    map[string]string `json:"workspace_by_root,omitempty"`
	ExternalRootByPath map[string]string `json:"external_root_by_path,omitempty"`
	LegacyV1           *LegacyV1Store    `json:"legacy_v1,omitempty"`

	// WorkspaceByCWD and WritableRootByPath are read only from the
	// unversioned v1 schema. A v2 write moves them under LegacyV1 so old ACL
	// receipts remain recoverable.
	WorkspaceByCWD     map[string]string `json:"workspace_by_cwd,omitempty"`
	WritableRootByPath map[string]string `json:"writable_root_by_path,omitempty"`
}

type Binding struct {
	WorkspaceSID string            `json:"workspace_sid,omitempty"`
	ExternalSIDs []string          `json:"external_sids,omitempty"`
	AllSIDs      []string          `json:"all_sids,omitempty"`
	WriteRootTo  map[string]string `json:"write_root_to,omitempty"`
	Missing      []string          `json:"missing,omitempty"`
}

func BindWriteRoots(storePath string, scope Scope) (Binding, error) {
	return bindWriteRoots(storePath, scope, true)
}

func LookupWriteRoots(storePath string, scope Scope) (Binding, error) {
	return bindWriteRoots(storePath, scope, false)
}

// DeriveWriteRoots computes the deterministic Host/path identities without
// reading or writing a capability Store. Elevated repair uses it only to
// independently validate already-persisted receipt intents.
func DeriveWriteRoots(scope Scope) (Binding, error) {
	scope, err := normalizeScope(scope)
	if err != nil {
		return Binding{}, err
	}
	return bindingForScope(scope), nil
}

// DeriveLegacyV1SID recomputes the StateDir-scoped deterministic SID emitted
// by the immediately preceding capability-store schema. Elevated retirement
// must compare exact provenance to this value instead of trusting an arbitrary
// SID from a user-writable legacy Store.
func DeriveLegacyV1SID(storePath, root string) string {
	storePath = strings.TrimSpace(storePath)
	if abs, err := filepath.Abs(storePath); err == nil {
		storePath = abs
	}
	identity := strings.Join([]string{
		"caelis-windows-sandbox-capability-v1",
		strings.ToLower(filepath.Clean(storePath)),
		pathutil.Key(root),
	}, "\x00")
	sum := sha256.Sum256([]byte(identity))
	a := binary.LittleEndian.Uint32(sum[0:4])
	b := binary.LittleEndian.Uint32(sum[4:8])
	c := binary.LittleEndian.Uint32(sum[8:12])
	d := binary.LittleEndian.Uint32(sum[12:16])
	return fmt.Sprintf("S-1-5-21-%d-%d-%d-%d", a, b, c, d)
}

// WithStoreLock serializes a Host capability-ledger transaction with SID
// binding and other receipt transactions in this StateDir. The callback must
// not call BindWriteRoots or recursively acquire the same store lock.
func WithStoreLock(storePath string, fn func() error) error {
	if fn == nil {
		return fmt.Errorf("capability: store transaction callback is required")
	}
	unlock, err := lockStore(storePath)
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

// LegacyV1Snapshot returns a copy of the retained v1 provenance under the
// Store lock. Callers must treat it only as evidence for exact adoption.
func LegacyV1Snapshot(storePath string) (*LegacyV1Store, error) {
	var snapshot *LegacyV1Store
	err := WithStoreLock(storePath, func() error {
		store, err := readStore(storePath)
		if err != nil {
			return err
		}
		snapshot = mergeLegacyV1(nil, store.LegacyV1)
		return nil
	})
	return snapshot, err
}

// ConsumeLegacyV1 removes only exact key/SID pairs already retired by the
// caller. Other workspace provenance is preserved for a later migration.
func ConsumeLegacyV1(storePath string, consumed LegacyV1Store) error {
	return WithStoreLock(storePath, func() error {
		store, err := readStore(storePath)
		if err != nil {
			return err
		}
		if store.Version != StoreVersion || store.LegacyV1 == nil {
			return nil
		}
		for key, sid := range consumed.WorkspaceByCWD {
			if current := store.LegacyV1.WorkspaceByCWD[key]; !strings.EqualFold(strings.TrimSpace(current), strings.TrimSpace(sid)) {
				return fmt.Errorf("capability: legacy workspace provenance changed for %s", key)
			}
			delete(store.LegacyV1.WorkspaceByCWD, key)
		}
		for key, sid := range consumed.WritableRootByPath {
			if current := store.LegacyV1.WritableRootByPath[key]; !strings.EqualFold(strings.TrimSpace(current), strings.TrimSpace(sid)) {
				return fmt.Errorf("capability: legacy writable-root provenance changed for %s", key)
			}
			delete(store.LegacyV1.WritableRootByPath, key)
		}
		if len(store.LegacyV1.WorkspaceByCWD) == 0 && len(store.LegacyV1.WritableRootByPath) == 0 {
			store.LegacyV1 = nil
		}
		return writeStore(storePath, store)
	})
}

func bindWriteRoots(storePath string, scope Scope, create bool) (Binding, error) {
	scope, err := normalizeScope(scope)
	if err != nil {
		return Binding{}, err
	}
	binding := bindingForScope(scope)
	if !create {
		store, err := readStore(storePath)
		if err != nil {
			return Binding{}, err
		}
		complete, err := storeContainsBinding(store, scope, binding)
		if err != nil {
			return Binding{}, err
		}
		if !complete {
			binding.Missing = append([]string(nil), scope.WriteRoots...)
		}
		return binding, nil
	}
	if complete, err := lookupCompleteBinding(storePath, scope, binding); complete || err != nil {
		return binding, err
	}
	unlock, err := lockStore(storePath)
	if err != nil {
		return Binding{}, err
	}
	defer unlock()
	store, err := readStore(storePath)
	if err != nil {
		return Binding{}, err
	}
	changed, err := upgradeStore(&store, scope.HostUserSID)
	if err != nil {
		return binding, err
	}
	recorded, err := recordBinding(&store, scope, binding)
	if err != nil {
		return Binding{}, err
	}
	changed = changed || recorded
	if changed {
		if err := writeStore(storePath, store); err != nil {
			return Binding{}, err
		}
	}
	return binding, nil
}

func lookupCompleteBinding(storePath string, scope Scope, binding Binding) (bool, error) {
	store, err := readStore(storePath)
	if err != nil {
		return false, err
	}
	return storeContainsBinding(store, scope, binding)
}

func normalizeScope(scope Scope) (Scope, error) {
	scope.HostUserSID = strings.ToUpper(strings.TrimSpace(scope.HostUserSID))
	if !strings.HasPrefix(scope.HostUserSID, "S-1-") {
		return Scope{}, fmt.Errorf("capability: current Host user SID is required")
	}
	scope.WorkspaceRoot = pathutil.Normalize(scope.WorkspaceRoot)
	if scope.WorkspaceRoot == "" {
		return Scope{}, fmt.Errorf("capability: canonical workspace root is required")
	}
	scope.SandboxEnvRoot = pathutil.Normalize(scope.SandboxEnvRoot)
	scope.WriteRoots = pathutil.Dedupe(scope.WriteRoots)
	if len(scope.WriteRoots) == 0 {
		return Scope{}, fmt.Errorf("capability: at least one write root is required")
	}
	return scope, nil
}

func bindingForScope(scope Scope) Binding {
	workspaceSID := stableWorkspaceSID(scope.HostUserSID, scope.WorkspaceRoot)
	binding := Binding{
		WorkspaceSID: workspaceSID,
		AllSIDs:      []string{workspaceSID},
		WriteRootTo:  map[string]string{},
	}
	for _, root := range scope.WriteRoots {
		sid := stableRootSID(scope.HostUserSID, root)
		if durablePathKey(root) != durablePathKey(scope.WorkspaceRoot) {
			binding.ExternalSIDs = append(binding.ExternalSIDs, sid)
		}
		binding.AllSIDs = append(binding.AllSIDs, sid)
		binding.WriteRootTo[pathutil.Normalize(root)] = sid
	}
	binding.ExternalSIDs = dedupeSIDs(binding.ExternalSIDs)
	binding.AllSIDs = dedupeSIDs(binding.AllSIDs)
	return binding
}

func workspaceOwnedRoot(scope Scope, root string) bool {
	if pathutil.IsUnder(root, scope.WorkspaceRoot) {
		return true
	}
	return scope.SandboxEnvRoot != "" && pathutil.IsUnder(root, scope.SandboxEnvRoot)
}

func storeContainsBinding(store Store, scope Scope, binding Binding) (bool, error) {
	if store.Version == 0 || store.Version == 1 {
		return false, nil
	}
	if store.Version != StoreVersion {
		return false, fmt.Errorf("capability: unsupported store version %d", store.Version)
	}
	if !strings.EqualFold(strings.TrimSpace(store.HostUserSID), scope.HostUserSID) {
		return false, hostUserRotationError(store.HostUserSID, scope.HostUserSID)
	}
	if !strings.EqualFold(strings.TrimSpace(store.WorkspaceByRoot[pathutil.Key(scope.WorkspaceRoot)]), binding.WorkspaceSID) {
		return false, nil
	}
	for root, sid := range binding.WriteRootTo {
		if workspaceOwnedRoot(scope, root) {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(store.ExternalRootByPath[pathutil.Key(root)]), sid) {
			return false, nil
		}
	}
	return true, nil
}

func upgradeStore(store *Store, hostUserSID string) (bool, error) {
	if store == nil {
		return false, fmt.Errorf("capability: store is required")
	}
	switch store.Version {
	case 0, 1:
		legacy := mergeLegacyV1(store.LegacyV1, &LegacyV1Store{
			WorkspaceByCWD:     store.WorkspaceByCWD,
			WritableRootByPath: store.WritableRootByPath,
		})
		*store = Store{Version: StoreVersion, HostUserSID: hostUserSID, WorkspaceByRoot: map[string]string{}, ExternalRootByPath: map[string]string{}, LegacyV1: legacy}
		return true, nil
	case StoreVersion:
		if !strings.EqualFold(strings.TrimSpace(store.HostUserSID), strings.TrimSpace(hostUserSID)) {
			return false, hostUserRotationError(store.HostUserSID, hostUserSID)
		}
		changed := false
		if store.WorkspaceByRoot == nil {
			store.WorkspaceByRoot = map[string]string{}
			changed = true
		}
		if store.ExternalRootByPath == nil {
			store.ExternalRootByPath = map[string]string{}
			changed = true
		}
		return changed, nil
	default:
		return false, fmt.Errorf("capability: unsupported store version %d", store.Version)
	}
}

func cloneMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func recordBinding(store *Store, scope Scope, binding Binding) (bool, error) {
	workspaceKey := pathutil.Key(scope.WorkspaceRoot)
	if current := strings.TrimSpace(store.WorkspaceByRoot[workspaceKey]); current != "" && !strings.EqualFold(current, binding.WorkspaceSID) {
		return false, fmt.Errorf("capability: workspace SID conflict for %s", scope.WorkspaceRoot)
	}
	changed := false
	if store.WorkspaceByRoot[workspaceKey] == "" {
		store.WorkspaceByRoot[workspaceKey] = binding.WorkspaceSID
		changed = true
	}
	for root, sid := range binding.WriteRootTo {
		if workspaceOwnedRoot(scope, root) {
			continue
		}
		rootKey := pathutil.Key(root)
		if current := strings.TrimSpace(store.ExternalRootByPath[rootKey]); current != "" {
			if !strings.EqualFold(current, sid) {
				return false, fmt.Errorf("capability: external writable-root SID conflict for %s", root)
			}
			continue
		}
		store.ExternalRootByPath[rootKey] = sid
		changed = true
	}
	return changed, nil
}

func hostUserRotationError(stored, current string) error {
	return &HostUserRotationError{
		StoredHostUserSID:  strings.ToUpper(strings.TrimSpace(stored)),
		CurrentHostUserSID: strings.ToUpper(strings.TrimSpace(current)),
	}
}

func mergeLegacyV1(left, right *LegacyV1Store) *LegacyV1Store {
	out := &LegacyV1Store{WorkspaceByCWD: map[string]string{}, WritableRootByPath: map[string]string{}}
	for _, value := range []*LegacyV1Store{left, right} {
		if value == nil {
			continue
		}
		for root, sid := range value.WorkspaceByCWD {
			out.WorkspaceByCWD[root] = sid
		}
		for root, sid := range value.WritableRootByPath {
			out.WritableRootByPath[root] = sid
		}
	}
	if len(out.WorkspaceByCWD) == 0 && len(out.WritableRootByPath) == 0 {
		return nil
	}
	return out
}

func dedupeSIDs(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToUpper(value)
		if value == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

func readStore(path string) (Store, error) {
	if strings.TrimSpace(path) == "" {
		return Store{}, fmt.Errorf("capability: store path is required")
	}
	deadline := time.Now().Add(storeReadRetryTimeout)
	for {
		store, err := readStoreOnce(path)
		if err == nil || !transientStoreReadError(err) || time.Now().After(deadline) {
			return store, err
		}
		time.Sleep(storeReadRetryAfter)
	}
}

func readStoreOnce(path string) (Store, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Store{}, nil
	}
	if err != nil {
		return Store{}, err
	}
	var store Store
	if err := json.Unmarshal(data, &store); err != nil {
		return Store{}, fmt.Errorf("capability: decode store: %w", err)
	}
	return store, nil
}

func transientStoreReadError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, windowsSharingViolation) || errors.Is(err, windowsLockViolation) {
		return true
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr.Err == nil {
		return false
	}
	message := strings.ToLower(pathErr.Err.Error())
	return strings.Contains(message, "being used by another process") || strings.Contains(message, "sharing violation")
}

func writeStore(path string, store Store) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(path, data, 0o600)
}

func lockStore(path string) (func(), error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("capability: store path is required")
	}
	path, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	mu := storeMutex(path)
	mu.Lock()
	fileLock, err := acquireLockFile(path + ".lock")
	if err != nil {
		mu.Unlock()
		return nil, err
	}
	return func() {
		_ = releaseStoreFileLock(fileLock)
		mu.Unlock()
	}, nil
}

func storeMutex(path string) *sync.Mutex {
	key := strings.ToLower(filepath.Clean(path))
	actual, _ := storeLocks.LoadOrStore(key, &sync.Mutex{})
	return actual.(*sync.Mutex)
}

func acquireLockFile(path string) (*storeFileLock, error) {
	deadline := time.Now().Add(storeLockTimeout)
	for {
		fileLock, contended, err := tryAcquireStoreFileLock(path)
		if err == nil && !contended {
			return fileLock, nil
		}
		if err != nil {
			return nil, fmt.Errorf("capability: acquire store lock: %w", err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("capability: acquire store lock %s: timed out", path)
		}
		time.Sleep(storeLockPollInterval)
	}
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
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
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
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

func stableWorkspaceSID(hostUserSID, workspaceRoot string) string {
	return stableRootSID(hostUserSID, workspaceRoot)
}

func stableExternalRootSID(hostUserSID, externalRoot string) string {
	return stableRootSID(hostUserSID, externalRoot)
}

func stableRootSID(hostUserSID, root string) string {
	identity := strings.Join([]string{
		"caelis-windows-sandbox-capability-v2",
		strings.ToUpper(strings.TrimSpace(hostUserSID)),
		pathutil.Key(root),
	}, "\x00")
	sum := sha256.Sum256([]byte(identity))
	a := binary.LittleEndian.Uint32(sum[0:4])
	b := binary.LittleEndian.Uint32(sum[4:8])
	c := binary.LittleEndian.Uint32(sum[8:12])
	d := binary.LittleEndian.Uint32(sum[12:16])
	return fmt.Sprintf("S-1-5-21-%d-%d-%d-%d", a, b, c, d)
}

func durablePathKey(path string) string {
	return strings.ToLower(filepath.Clean(strings.TrimSpace(path)))
}
