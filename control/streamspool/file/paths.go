package file

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/caelis-labs/caelis/control/streamspool"
)

func secureRoot(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("stream spool root is required")
	}
	root, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("resolve stream spool root: %w", err)
	}
	root = filepath.Clean(root)
	if root == string(filepath.Separator) || root == "." {
		return "", errors.New("stream spool root is too broad")
	}
	if err := secureMkdirAll(root); err != nil {
		return "", err
	}
	return root, nil
}

func secureMkdirAll(path string) error {
	path = filepath.Clean(path)
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create stream spool directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("stream spool path is not a real directory")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return err
	}
	return nil
}

func validateLogicalKey(key streamspool.LogicalKey) error {
	if !key.Namespace.Valid() {
		return errors.New("stream spool namespace is invalid")
	}
	if key.Digest == (streamspool.Digest{}) {
		return errors.New("stream spool digest is empty")
	}
	return nil
}

func partitionDir(root string, key streamspool.Key) string {
	return filepath.Join(root, partitionRelativeDir(key))
}

func partitionRelativeDir(key streamspool.Key) string {
	digest := key.Digest.Hex()
	return filepath.Join(
		key.Namespace.String(),
		digest[:2],
		digest,
		fmt.Sprintf("%x", key.Epoch[:]),
		fmt.Sprintf("%x", key.Incarnation[:]),
	)
}

func secureManagedMkdirAll(root *os.Root, path string) error {
	if root == nil {
		return errors.New("stream spool root handle is unavailable")
	}
	path = filepath.Clean(path)
	if path == "." || !filepath.IsLocal(path) {
		return errors.New("stream spool managed path escapes its root")
	}
	current := ""
	for _, component := range strings.Split(path, string(filepath.Separator)) {
		if component == "" || component == "." || component == ".." {
			return errors.New("stream spool managed path is invalid")
		}
		current = filepath.Join(current, component)
		info, err := root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := root.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("create stream spool directory: %w", err)
			}
			info, err = root.Lstat(current)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("stream spool managed path is not a real directory")
		}
		if err := root.Chmod(current, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func validateManagedDirectory(root *os.Root, path string) error {
	if root == nil {
		return errors.New("stream spool root handle is unavailable")
	}
	path = filepath.Clean(path)
	if path == "." || !filepath.IsLocal(path) {
		return errors.New("stream spool managed directory escapes its root")
	}
	current := ""
	for _, component := range strings.Split(path, string(filepath.Separator)) {
		if component == "" || component == "." || component == ".." {
			return errors.New("stream spool managed directory is invalid")
		}
		current = filepath.Join(current, component)
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("stream spool managed directory is not a real directory")
		}
	}
	return nil
}

func validateOpenedRegular(root *os.Root, path string, file *os.File) error {
	if root == nil || file == nil || !filepath.IsLocal(path) {
		return errors.New("stream spool opened path is invalid")
	}
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	linked, err := root.Lstat(path)
	if err != nil {
		return err
	}
	if linked.Mode()&os.ModeSymlink != 0 || !linked.Mode().IsRegular() || !opened.Mode().IsRegular() || !os.SameFile(opened, linked) {
		return errors.New("stream spool opened file no longer matches its regular path")
	}
	return nil
}

func removeManagedPartition(root *os.Root, path string) error {
	if root == nil || !filepath.IsLocal(path) {
		return errors.New("stream spool partition path is invalid")
	}
	path = filepath.Clean(path)
	if err := validateManagedDirectory(root, filepath.Dir(path)); err != nil {
		return err
	}
	if info, err := root.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("stream spool partition path is not a real directory")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := root.RemoveAll(path); err != nil {
		return err
	}
	namespace := strings.Split(path, string(filepath.Separator))[0]
	for parent := filepath.Dir(path); parent != "." && parent != namespace; parent = filepath.Dir(parent) {
		if err := root.Remove(parent); err == nil || errors.Is(err, os.ErrNotExist) {
			continue
		} else {
			dir, openErr := root.Open(parent)
			if openErr != nil {
				return errors.Join(err, openErr)
			}
			_, readErr := dir.Readdirnames(1)
			closeErr := dir.Close()
			if readErr == nil {
				return closeErr
			}
			if !errors.Is(readErr, io.EOF) {
				return errors.Join(err, readErr, closeErr)
			}
			return errors.Join(err, closeErr)
		}
	}
	return nil
}

func segmentFilename(offset streamspool.Offset) string {
	return fmt.Sprintf("%020d.log", uint64(offset))
}

func parseSegmentFilename(name string) (streamspool.Offset, bool) {
	if len(name) != 24 || !strings.HasSuffix(name, ".log") {
		return 0, false
	}
	raw := strings.TrimSuffix(name, ".log")
	value, err := strconv.ParseUint(raw, 10, 64)
	return streamspool.Offset(value), err == nil
}

func openExclusiveRegular(root *os.Root, path string) (*os.File, error) {
	if root == nil || !filepath.IsLocal(path) {
		return nil, errors.New("stream spool segment path is invalid")
	}
	if info, err := root.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, errors.New("stream spool segment is not a regular file")
		}
		return nil, os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := root.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := errors.Join(validateManagedDirectory(root, filepath.Dir(path)), validateOpenedRegular(root, path, file)); err != nil {
		_ = file.Close()
		_ = root.Remove(path)
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func openReadOnlyRegular(root *os.Root, path string) (*os.File, error) {
	if root == nil || !filepath.IsLocal(path) {
		return nil, errors.New("stream spool segment path is invalid")
	}
	info, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: segment is not a regular file", streamspool.ErrCorrupt)
	}
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	if err := errors.Join(validateManagedDirectory(root, filepath.Dir(path)), validateOpenedRegular(root, path, file)); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: %w", streamspool.ErrCorrupt, err)
	}
	return file, nil
}

func reclaimOldEpochs(root *os.Root) error {
	if root == nil {
		return errors.New("stream spool root handle is unavailable")
	}
	for _, namespace := range []string{streamspool.NamespaceTask.String(), streamspool.NamespaceSession.String()} {
		if info, err := root.Lstat(namespace); err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("stream spool namespace path %q is invalid", namespace)
			}
			if err := root.RemoveAll(namespace); err != nil {
				return fmt.Errorf("reclaim old stream spool namespace %q: %w", namespace, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := secureManagedMkdirAll(root, namespace); err != nil {
			return err
		}
	}
	return nil
}
