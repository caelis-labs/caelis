//go:build windows

package win32

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// FileIdentity is the stable volume/file identity of a Windows object.
type FileIdentity struct {
	VolumeSerialNumber uint64   `json:"volume_serial_number"`
	FileID             [16]byte `json:"file_id"`
}

// StableDirectory pins one no-reparse directory against rename/replacement.
type StableDirectory struct {
	handle   windows.Handle
	path     string
	identity FileIdentity
	ownerSID string
}

// OpenStableDirectory opens one absolute local directory with a single
// OBJ_DONT_REPARSE kernel parse and without FILE_SHARE_DELETE. The retained
// handle prevents replacement while handle-relative repair IPC is active.
func OpenStableDirectory(path, ownerSID string) (*StableDirectory, error) {
	path, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil || path == "" || len(filepath.VolumeName(path)) != 2 {
		return nil, fmt.Errorf("win32: canonical local directory is required")
	}
	path = filepath.Clean(path)
	path, err = longDOSPath(path)
	if err != nil {
		return nil, fmt.Errorf("win32: resolve stable directory path %s: %w", path, err)
	}
	handle, err := ntOpenSecure(0, `\??\`+path, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES|windows.FILE_LIST_DIRECTORY|windows.SYNCHRONIZE, windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_OPEN_FOR_BACKUP_INTENT, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE)
	if err != nil {
		return nil, fmt.Errorf("win32: open stable directory %s: %w", path, err)
	}
	dir := &StableDirectory{handle: handle, path: path}
	fail := func(err error) (*StableDirectory, error) {
		_ = dir.Close()
		return nil, err
	}
	if err := rejectSecureReparse(handle, path); err != nil {
		return fail(err)
	}
	finalPath, err := finalDOSPath(handle)
	if err != nil {
		return fail(fmt.Errorf("win32: resolve stable directory final path %s: %w", path, err))
	}
	if !strings.EqualFold(filepath.Clean(finalPath), path) {
		return fail(fmt.Errorf("win32: stable directory final path %q does not match %q", finalPath, path))
	}
	identity, err := secureFileIdentity(handle)
	if err != nil {
		return fail(err)
	}
	owner, err := secureOwnerSID(handle)
	if err != nil || (strings.TrimSpace(ownerSID) != "" && !strings.EqualFold(strings.TrimSpace(owner), strings.TrimSpace(ownerSID))) {
		return fail(fmt.Errorf("win32: stable directory owner %q does not match %q: %w", owner, ownerSID, err))
	}
	dir.identity = identity
	dir.ownerSID = owner
	return dir, nil
}

func (d *StableDirectory) OwnerSID() string {
	if d == nil {
		return ""
	}
	return d.ownerSID
}

func (d *StableDirectory) Identity() FileIdentity {
	if d == nil {
		return FileIdentity{}
	}
	return d.identity
}

func (d *StableDirectory) Path() string {
	if d == nil {
		return ""
	}
	return d.path
}

func (d *StableDirectory) Close() error {
	if d == nil || d.handle == 0 || d.handle == windows.InvalidHandle {
		return nil
	}
	handle := d.handle
	d.handle = 0
	return windows.CloseHandle(handle)
}

// CreateNewFile creates one basename relative to the pinned directory. It
// never opens or truncates an existing object.
func (d *StableDirectory) CreateNewFile(name string) (*os.File, FileIdentity, error) {
	if d == nil || d.handle == 0 {
		return nil, FileIdentity{}, fmt.Errorf("win32: stable directory is closed")
	}
	if err := validateSecureBasename(name); err != nil {
		return nil, FileIdentity{}, err
	}
	handle, err := ntOpenSecure(d.handle, name, windows.FILE_READ_DATA|windows.FILE_WRITE_DATA|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.SYNCHRONIZE, windows.FILE_CREATE, windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE)
	if err != nil {
		return nil, FileIdentity{}, err
	}
	if err := rejectSecureReparse(handle, name); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, FileIdentity{}, err
	}
	identity, err := secureFileIdentity(handle)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return nil, FileIdentity{}, err
	}
	return os.NewFile(uintptr(handle), filepath.Join(d.path, name)), identity, nil
}

// OpenExpectedFile opens one retained directory child without reparsing and
// verifies its stable identity and owner before returning a handle-backed file.
func (d *StableDirectory) OpenExpectedFile(name string, expected FileIdentity, ownerSID string, write bool) (*os.File, error) {
	if d == nil || d.handle == 0 {
		return nil, fmt.Errorf("win32: stable directory is closed")
	}
	if err := validateSecureBasename(name); err != nil {
		return nil, err
	}
	access := uint32(windows.FILE_READ_DATA | windows.FILE_READ_ATTRIBUTES | windows.READ_CONTROL | windows.SYNCHRONIZE)
	if write {
		access |= windows.FILE_WRITE_DATA
	}
	handle, err := ntOpenSecure(d.handle, name, access, windows.FILE_OPEN, windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*os.File, error) {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	if err := rejectSecureReparse(handle, name); err != nil {
		return fail(err)
	}
	identity, err := secureFileIdentity(handle)
	if err != nil || identity != expected {
		return fail(fmt.Errorf("win32: repair IPC identity mismatch for %s: %w", name, err))
	}
	owner, err := secureOwnerSID(handle)
	if err != nil || (strings.TrimSpace(ownerSID) != "" && !strings.EqualFold(strings.TrimSpace(owner), strings.TrimSpace(ownerSID))) {
		return fail(fmt.Errorf("win32: repair IPC owner %q does not match %q: %w", owner, ownerSID, err))
	}
	return os.NewFile(uintptr(handle), filepath.Join(d.path, name)), nil
}

func WriteStableFile(file *os.File, data []byte) error {
	if file == nil {
		return fmt.Errorf("win32: stable file is required")
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

func validateSecureBasename(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
		return fmt.Errorf("win32: secure file basename is invalid")
	}
	return nil
}

func ntOpenSecure(root windows.Handle, name string, access, disposition, options, share uint32) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{Length: uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})), RootDirectory: root, ObjectName: objectName, Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE}
	var handle windows.Handle
	var iosb windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, access, attributes, &iosb, nil, windows.FILE_ATTRIBUTE_NORMAL, share, disposition, options, 0, 0)
	runtime.KeepAlive(objectName)
	return handle, err
}

func rejectSecureReparse(handle windows.Handle, label string) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("win32: repair IPC refuses reparse point %s", label)
	}
	return nil
}

type secureFileIDInfo struct {
	VolumeSerialNumber uint64
	FileID             [16]byte
}

func secureFileIdentity(handle windows.Handle) (FileIdentity, error) {
	var info secureFileIDInfo
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileIdInfo, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return FileIdentity{}, err
	}
	if info.FileID == ([16]byte{}) {
		return FileIdentity{}, fmt.Errorf("win32: empty stable file identity")
	}
	return FileIdentity(info), nil
}

func secureOwnerSID(handle windows.Handle) (string, error) {
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return "", err
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.IsValid() {
		return "", fmt.Errorf("win32: invalid stable file owner: %w", err)
	}
	return owner.String(), nil
}

func finalDOSPath(handle windows.Handle) (string, error) {
	buffer := make([]uint16, 512)
	for {
		// Zero selects FILE_NAME_NORMALIZED | VOLUME_NAME_DOS. x/sys/windows
		// does not currently export those zero-valued Win32 constants.
		n, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
		if err != nil {
			return "", err
		}
		if n < uint32(len(buffer)) {
			path := windows.UTF16ToString(buffer[:n])
			path = strings.TrimPrefix(path, `\\?\`)
			return path, nil
		}
		buffer = make([]uint16, n+1)
	}
}

func longDOSPath(path string) (string, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	buffer := make([]uint16, 512)
	for {
		n, err := windows.GetLongPathName(pathPtr, &buffer[0], uint32(len(buffer)))
		if err != nil {
			return "", err
		}
		if n < uint32(len(buffer)) {
			return filepath.Clean(windows.UTF16ToString(buffer[:n])), nil
		}
		buffer = make([]uint16, n+1)
	}
}
