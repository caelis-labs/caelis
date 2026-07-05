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
	storeLockStaleAfter   = 2 * time.Minute
	storeReadRetryAfter   = 10 * time.Millisecond
	storeReadRetryTimeout = time.Second
)

const (
	windowsAccessDenied     syscall.Errno = 5
	windowsSharingViolation syscall.Errno = 32
	windowsLockViolation    syscall.Errno = 33
)

var storeLocks sync.Map

type Store struct {
	WorkspaceByCWD     map[string]string `json:"workspace_by_cwd,omitempty"`
	WritableRootByPath map[string]string `json:"writable_root_by_path,omitempty"`
}

type Binding struct {
	AllSIDs     []string
	WriteRootTo map[string]string
	Missing     []string
}

func BindWriteRoots(storePath string, cwd string, writeRoots []string) (Binding, error) {
	return bindWriteRoots(storePath, cwd, writeRoots, true)
}

func LookupWriteRoots(storePath string, cwd string, writeRoots []string) (Binding, error) {
	return bindWriteRoots(storePath, cwd, writeRoots, false)
}

func bindWriteRoots(storePath string, cwd string, writeRoots []string, create bool) (Binding, error) {
	writeRoots = pathutil.Dedupe(writeRoots)
	if len(writeRoots) == 0 {
		return Binding{}, nil
	}
	if !create {
		store, err := readStore(storePath)
		if err != nil {
			return Binding{}, err
		}
		binding, _, err := bindStore(storePath, &store, cwd, writeRoots, false)
		return binding, err
	}
	if binding, ok, err := lookupCompleteBinding(storePath, cwd, writeRoots); ok || err != nil {
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
	binding, changed, err := bindStore(storePath, &store, cwd, writeRoots, true)
	if err != nil {
		return Binding{}, err
	}
	if changed {
		if err := writeStore(storePath, store); err != nil {
			return Binding{}, err
		}
	}
	return binding, nil
}

func lookupCompleteBinding(storePath string, cwd string, writeRoots []string) (Binding, bool, error) {
	store, err := readStore(storePath)
	if err != nil {
		return Binding{}, false, err
	}
	binding, _, err := bindStore(storePath, &store, cwd, writeRoots, false)
	if err != nil {
		return Binding{}, false, err
	}
	return binding, len(binding.Missing) == 0, nil
}

func bindStore(storePath string, store *Store, cwd string, writeRoots []string, create bool) (Binding, bool, error) {
	if store == nil {
		store = &Store{}
	}
	if store.WorkspaceByCWD == nil {
		store.WorkspaceByCWD = map[string]string{}
	}
	if store.WritableRootByPath == nil {
		store.WritableRootByPath = map[string]string{}
	}
	cwdKey := pathutil.Key(cwd)
	binding := Binding{WriteRootTo: map[string]string{}}
	seen := map[string]struct{}{}
	changed := false
	for _, root := range writeRoots {
		key := pathutil.Key(root)
		if key == "" {
			continue
		}
		table := store.WritableRootByPath
		if cwdKey != "" && key == cwdKey {
			table = store.WorkspaceByCWD
		}
		sid := strings.TrimSpace(table[key])
		if sid == "" {
			if !create {
				binding.Missing = append(binding.Missing, pathutil.Normalize(root))
				continue
			}
			sid = stableSID(storePath, key)
			table[key] = sid
			changed = true
		}
		binding.WriteRootTo[pathutil.Normalize(root)] = sid
		if _, ok := seen[sid]; !ok {
			seen[sid] = struct{}{}
			binding.AllSIDs = append(binding.AllSIDs, sid)
		}
	}
	if changed {
		return binding, true, nil
	}
	return binding, false, nil
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
	file, err := acquireLockFile(path + ".lock")
	if err != nil {
		mu.Unlock()
		return nil, err
	}
	return func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
		mu.Unlock()
	}, nil
}

func storeMutex(path string) *sync.Mutex {
	key := strings.ToLower(filepath.Clean(path))
	actual, _ := storeLocks.LoadOrStore(key, &sync.Mutex{})
	return actual.(*sync.Mutex)
}

func acquireLockFile(path string) (*os.File, error) {
	deadline := time.Now().Add(storeLockTimeout)
	for {
		file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(file, "pid=%d\ncreated_at=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano))
			return file, nil
		}
		if !storeLockContention(path, err) {
			return nil, fmt.Errorf("capability: acquire store lock: %w", err)
		}
		if staleLock(path) {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("capability: acquire store lock %s: timed out", path)
		}
		time.Sleep(storeLockPollInterval)
	}
}

func storeLockContention(path string, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrExist) || os.IsExist(err) {
		return true
	}
	if !transientLockOpenError(err) {
		return false
	}
	if _, statErr := os.Stat(path); statErr == nil {
		return true
	}
	return lockDirectoryWritable(filepath.Dir(path))
}

func transientLockOpenError(err error) bool {
	var pathErr *os.PathError
	if errors.Is(err, os.ErrPermission) || errors.Is(err, windowsAccessDenied) || errors.Is(err, windowsSharingViolation) || errors.Is(err, windowsLockViolation) {
		return true
	}
	if errors.As(err, &pathErr) && pathErr.Err != nil {
		message := strings.ToLower(pathErr.Err.Error())
		return strings.Contains(message, "access is denied") ||
			strings.Contains(message, "being used by another process") ||
			strings.Contains(message, "sharing violation")
	}
	return false
}

func lockDirectoryWritable(dir string) bool {
	file, err := os.CreateTemp(dir, ".capability-lock-probe-*.tmp")
	if err != nil {
		return false
	}
	name := file.Name()
	_ = file.Close()
	_ = os.Remove(name)
	return true
}

func staleLock(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) > storeLockStaleAfter
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

func stableSID(storePath string, rootKey string) string {
	sum := sha256.Sum256([]byte("caelis-windows-sandbox-capability-v1\x00" + storeNamespace(storePath) + "\x00" + strings.ToLower(strings.TrimSpace(rootKey))))
	a := binary.LittleEndian.Uint32(sum[0:4])
	b := binary.LittleEndian.Uint32(sum[4:8])
	c := binary.LittleEndian.Uint32(sum[8:12])
	d := binary.LittleEndian.Uint32(sum[12:16])
	return fmt.Sprintf("S-1-5-21-%d-%d-%d-%d", a, b, c, d)
}

func storeNamespace(path string) string {
	path = strings.TrimSpace(path)
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return strings.ToLower(filepath.Clean(path))
}
