//go:build windows

package acl

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const aceReceiptVersion = 2
const aclRevisionDS = 4

const (
	accessDeniedObjectACEType         = 0x06
	accessDeniedCallbackACEType       = 0x0a
	accessDeniedCallbackObjectACEType = 0x0c
)

type fileIDInfo struct {
	VolumeSerialNumber uint64
	FileID             [16]byte
}

type aclHeader struct {
	Revision byte
	Sbz1     byte
	Size     uint16
	ACECount uint16
	Sbz2     uint16
}

type managedObject struct {
	path   string
	handle windows.Handle
}

type daclSnapshot struct {
	identity    FileIdentity
	ownerSID    string
	daclControl uint16
	revision    byte
	aces        [][]byte
}

// PrepareExactFileDACLEntry captures a durable, read-only intent for one exact,
// basic explicit allow or deny ACE. The caller must persist the returned
// receipt before passing it to EnsureFileDACLReceipt.
func PrepareExactFileDACLEntry(path string, entry Entry) (ACEReceipt, error) {
	object, err := openManagedObject(path, false)
	if err != nil {
		return ACEReceipt{}, err
	}
	defer func() { object.close() }()

	before, err := readDACL(object)
	if err != nil {
		return ACEReceipt{}, err
	}
	raw, err := exactACE(entry)
	if err != nil {
		return ACEReceipt{}, err
	}
	baseline := countExactACE(before.aces, raw)
	receipt := newACEReceipt(object.path, before, raw, baseline, baseline == 0, expectedPostWriteDACLControl(before.daclControl))
	return receipt, nil
}

// AdoptExistingExactFileDACLEntry captures one existing exact ACE occurrence
// as owned. It does not establish provenance; callers may use it only after a
// trusted durable Caelis legacy record proves the synthetic SID and semantic
// effect. Unknown or multiply-occurring ACEs fail closed.
func AdoptExistingExactFileDACLEntry(path string, entry Entry) (ACEReceipt, error) {
	object, err := openManagedObject(path, false)
	if err != nil {
		return ACEReceipt{}, err
	}
	defer object.close()
	before, err := readDACL(object)
	if err != nil {
		return ACEReceipt{}, err
	}
	raw, err := exactACE(entry)
	if err != nil {
		return ACEReceipt{}, err
	}
	if count := countExactACE(before.aces, raw); count != 1 {
		return ACEReceipt{}, fmt.Errorf("acl: trusted exact ACE adoption count on %s = %d, want 1", object.path, count)
	}
	return newACEReceipt(object.path, before, raw, 0, true, before.daclControl), nil
}

// ExactFileDACLEntryCount returns the number of byte-identical explicit basic
// ACE occurrences for entry on the stable non-reparse object.
func ExactFileDACLEntryCount(path string, entry Entry) (uint32, error) {
	object, err := openManagedObject(path, false)
	if err != nil {
		return 0, err
	}
	defer object.close()
	snapshot, err := readDACL(object)
	if err != nil {
		return 0, err
	}
	raw, err := exactACE(entry)
	if err != nil {
		return 0, err
	}
	return countExactACE(snapshot.aces, raw), nil
}

// ValidateACEReceiptEntry verifies that a persisted raw receipt is exactly the
// basic ACE encoded by its semantic journal entry.
func ValidateACEReceiptEntry(receipt ACEReceipt, entry Entry) error {
	if err := validateACEReceipt(receipt); err != nil {
		return err
	}
	raw, err := exactACE(entry)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, receipt.RawACE) {
		return fmt.Errorf("acl: receipt raw ACE does not match its journal entry")
	}
	return nil
}

// EnsureFileDACLReceipt idempotently applies and verifies a receipt previously
// returned by PrepareExactFileDACLEntry. Persisting the receipt first lets a
// recovery pass distinguish a candidate ACE from an unrelated preexisting ACE.
func EnsureFileDACLReceipt(path string, receipt ACEReceipt) error {
	if err := validateACEReceipt(receipt); err != nil {
		return err
	}
	object, err := openManagedObject(path, false)
	if err != nil {
		return err
	}
	defer object.close()

	current, err := readDACL(object)
	if err != nil {
		return err
	}
	if err := verifyReceiptIdentity(receipt, current); err != nil {
		return err
	}
	count := countExactACE(current.aces, receipt.RawACE)
	if !receipt.Owned {
		if err := verifyReceiptControl(receipt.Path, receipt.DACLControl, current); err != nil {
			return err
		}
		if count != receipt.BaselineExactCount {
			return fmt.Errorf("acl: borrowed exact ACE count on %s = %d, want %d", object.path, count, receipt.BaselineExactCount)
		}
		return nil
	}
	if count == 1 {
		if err := verifyReceiptControl(receipt.Path, receipt.AppliedDACLControl, current); err != nil {
			return err
		}
		if daclSnapshotSHA256(current) == receipt.AppliedDACLSHA256 {
			return nil
		}
		return &OwnershipAmbiguousError{Path: object.path}
	}
	if count != 0 {
		return fmt.Errorf("acl: owned exact ACE count on %s = %d, want 0 or 1; refusing ambiguous ensure", object.path, count)
	}
	if err := verifyReceiptControl(receipt.Path, receipt.DACLControl, current); err != nil {
		return err
	}
	if daclSnapshotSHA256(current) != receipt.BaselineDACLSHA256 {
		return fmt.Errorf("acl: DACL baseline changed for receipt path %s", receipt.Path)
	}
	object.close()
	writable, err := openManagedObject(path, true)
	if err != nil {
		return err
	}
	object = writable

	// Windows has no compare-and-swap operation for a DACL. Re-read the whole
	// descriptor immediately before SetSecurityInfo and build from that fresh
	// snapshot so Caelis never knowingly overwrites an intervening ACE change.
	// A third-party SetSecurityInfo can still race after this read; callers must
	// serialize Caelis mutations, and post-write verification fails closed.
	fresh, err := readDACL(writable)
	if err != nil {
		return err
	}
	if err := verifyReceiptIdentity(receipt, fresh); err != nil {
		return err
	}
	count = countExactACE(fresh.aces, receipt.RawACE)
	if count == 1 {
		if err := verifyReceiptControl(receipt.Path, receipt.AppliedDACLControl, fresh); err != nil {
			return err
		}
		if daclSnapshotSHA256(fresh) == receipt.AppliedDACLSHA256 {
			return nil
		}
		return &OwnershipAmbiguousError{Path: writable.path}
	}
	if count != 0 {
		return fmt.Errorf("acl: owned exact ACE count on %s = %d, want 0 or 1; refusing ambiguous ensure", object.path, count)
	}
	if err := verifyReceiptControl(receipt.Path, receipt.DACLControl, fresh); err != nil {
		return err
	}
	if daclSnapshotSHA256(fresh) != receipt.BaselineDACLSHA256 {
		return fmt.Errorf("acl: DACL baseline changed for receipt path %s", receipt.Path)
	}

	nextACEs := insertCanonicalACE(fresh.aces, receipt.RawACE)
	if err := writeDACL(writable, fresh.revision, nextACEs); err != nil {
		return err
	}
	after, err := readDACL(writable)
	if err != nil {
		return fmt.Errorf("acl: verify exact ACE write on %s: %w", object.path, err)
	}
	if err := verifyPostWriteObject(fresh, after, receipt.AppliedDACLControl); err != nil {
		return fmt.Errorf("acl: verify exact ACE write on %s: %w", object.path, err)
	}
	if !equalACESequence(after.aces, nextACEs) {
		return fmt.Errorf("acl: verify exact ACE write on %s: DACL changed unexpectedly", object.path)
	}
	if daclSnapshotSHA256(after) != receipt.AppliedDACLSHA256 {
		return fmt.Errorf("acl: verify exact ACE write on %s: postimage fingerprint changed unexpectedly", object.path)
	}
	if count := countExactACE(after.aces, receipt.RawACE); count != 1 {
		return fmt.Errorf("acl: verify exact ACE write on %s: exact ACE count = %d, want 1", object.path, count)
	}
	return nil
}

// EnsureExactFileDACLEntry is a convenience composition of Prepare and Ensure.
// Durable rotation code must call and persist PrepareExactFileDACLEntry before
// EnsureFileDACLReceipt; this convenience cannot close the crash window between
// the external DACL effect and persistence of the returned receipt.
func EnsureExactFileDACLEntry(path string, entry Entry) (ACEReceipt, error) {
	receipt, err := PrepareExactFileDACLEntry(path, entry)
	if err != nil {
		return ACEReceipt{}, err
	}
	if err := EnsureFileDACLReceipt(path, receipt); err != nil {
		return ACEReceipt{}, err
	}
	return receipt, nil
}

// VerifyFileDACLReceipt verifies the receipt's stable file identity, owner,
// DACL control flags, and exact raw ACE occurrence count.
func VerifyFileDACLReceipt(path string, receipt ACEReceipt) error {
	if err := validateACEReceipt(receipt); err != nil {
		return err
	}
	object, err := openManagedObject(path, false)
	if err != nil {
		return err
	}
	defer object.close()
	current, err := readDACL(object)
	if err != nil {
		return err
	}
	return verifyReceiptSnapshot(receipt, current)
}

// ProbeFileDACLWriteAccess verifies that the current token can open path for
// WRITE_DAC without mutating its security descriptor.
func ProbeFileDACLWriteAccess(path string) error {
	object, err := openManagedObject(path, true)
	if err != nil {
		return err
	}
	object.close()
	return nil
}

// RemoveFileDACLReceipt removes the one exact ACE occurrence owned by receipt.
// Borrowed receipts never mutate the DACL. The operation is idempotent after an
// owned receipt has already been removed.
func RemoveFileDACLReceipt(path string, receipt ACEReceipt) (bool, error) {
	if err := validateACEReceipt(receipt); err != nil {
		return false, err
	}
	object, err := openManagedObject(path, false)
	if err != nil {
		return false, err
	}
	before, err := readDACL(object)
	object.close()
	if err != nil {
		return false, err
	}
	if err := verifyReceiptIdentity(receipt, before); err != nil {
		return false, err
	}

	count := countExactACE(before.aces, receipt.RawACE)
	if !receipt.Owned {
		if err := verifyReceiptControl(receipt.Path, receipt.DACLControl, before); err != nil {
			return false, err
		}
		if count != receipt.BaselineExactCount {
			return false, fmt.Errorf("acl: borrowed exact ACE count on %s = %d, want %d", object.path, count, receipt.BaselineExactCount)
		}
		return false, nil
	}
	if err := verifyReceiptControl(receipt.Path, receipt.AppliedDACLControl, before); err != nil {
		return false, err
	}
	if count == 0 {
		return false, nil
	}
	if count != 1 {
		return false, fmt.Errorf("acl: owned exact ACE count on %s = %d, want 1; refusing ambiguous removal", receipt.Path, count)
	}
	object, err = openManagedObject(path, true)
	if err != nil {
		return false, err
	}
	defer object.close()
	// As in EnsureFileDACLReceipt, refresh the entire DACL immediately before
	// the effect. The exact-count guard refuses to guess if an identical ACE was
	// added concurrently.
	fresh, err := readDACL(object)
	if err != nil {
		return false, err
	}
	if err := verifyReceiptIdentity(receipt, fresh); err != nil {
		return false, err
	}
	if err := verifyReceiptControl(receipt.Path, receipt.AppliedDACLControl, fresh); err != nil {
		return false, err
	}
	count = countExactACE(fresh.aces, receipt.RawACE)
	if count == 0 {
		return false, nil
	}
	if count != 1 {
		return false, fmt.Errorf("acl: owned exact ACE count on %s = %d, want 1; refusing ambiguous removal", object.path, count)
	}
	nextACEs, removed := removeOneExactACE(fresh.aces, receipt.RawACE)
	if !removed {
		return false, fmt.Errorf("acl: owned exact ACE disappeared from %s", object.path)
	}
	if err := writeDACL(object, fresh.revision, nextACEs); err != nil {
		return false, err
	}
	after, err := readDACL(object)
	if err != nil {
		return false, fmt.Errorf("acl: verify exact ACE removal on %s: %w", object.path, err)
	}
	if err := verifyPostWriteObject(fresh, after, expectedPostWriteDACLControl(fresh.daclControl)); err != nil {
		return false, fmt.Errorf("acl: verify exact ACE removal on %s: %w", object.path, err)
	}
	if !equalACESequence(after.aces, nextACEs) {
		return false, fmt.Errorf("acl: verify exact ACE removal on %s: DACL changed unexpectedly", object.path)
	}
	return true, nil
}

func openManagedObject(path string, write bool) (managedObject, error) {
	normalized, err := normalizeManagedPath(path)
	if err != nil {
		return managedObject{}, err
	}
	handle, err := openPathWithoutReparse(normalized, write)
	if err != nil {
		if write {
			return managedObject{}, &DACLWriteAccessError{Path: normalized, Err: err}
		}
		return managedObject{}, fmt.Errorf("acl: open %s for exact DACL access: %w", normalized, err)
	}
	object := managedObject{path: normalized, handle: handle}
	fail := func(err error) (managedObject, error) {
		object.close()
		return managedObject{}, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return fail(fmt.Errorf("acl: inspect %s: %w", normalized, err))
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fail(fmt.Errorf("acl: refuse reparse point %s", normalized))
	}
	if _, err := fileIdentity(handle); err != nil {
		return fail(fmt.Errorf("acl: identify %s: %w", normalized, err))
	}
	return object, nil
}

func (o managedObject) close() {
	if o.handle != 0 && o.handle != windows.InvalidHandle {
		_ = windows.CloseHandle(o.handle)
	}
}

func normalizeManagedPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("acl: path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("acl: resolve path %s: %w", path, err)
	}
	return filepath.Clean(abs), nil
}

func openPathWithoutReparse(path string, write bool) (windows.Handle, error) {
	volume := filepath.VolumeName(path)
	if len(volume) != 2 || volume[1] != ':' {
		return 0, fmt.Errorf("only local drive paths can be opened without reparse traversal: %s", path)
	}
	finalAccess := uint32(windows.READ_CONTROL | windows.FILE_READ_ATTRIBUTES)
	if write {
		finalAccess |= windows.WRITE_DAC
	}
	// OBJ_DONT_REPARSE makes name resolution itself fail with
	// STATUS_REPARSE_POINT_ENCOUNTERED if any ancestor or the final component
	// would be reparsed. Unlike a scan around CreateFile, this is one kernel
	// parse and has no pathname swap window.
	handle, err := ntOpenPathComponent(0, `\??\`+path, finalAccess, false)
	if err != nil {
		return 0, err
	}
	if err := rejectHandleReparse(handle, path); err != nil {
		_ = windows.CloseHandle(handle)
		return 0, err
	}
	return handle, nil
}

func ntOpenPathComponent(root windows.Handle, name string, access uint32, requireDirectory bool) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: root,
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	options := uint32(windows.FILE_OPEN_REPARSE_POINT | windows.FILE_OPEN_FOR_BACKUP_INTENT)
	if requireDirectory {
		options |= windows.FILE_DIRECTORY_FILE
	}
	var (
		handle windows.Handle
		iosb   windows.IO_STATUS_BLOCK
	)
	err = windows.NtCreateFile(
		&handle,
		access,
		attributes,
		&iosb,
		nil,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		options,
		0,
		0,
	)
	runtime.KeepAlive(objectName)
	if err != nil {
		return 0, err
	}
	return handle, nil
}

func rejectHandleReparse(handle windows.Handle, label string) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return fmt.Errorf("inspect %s: %w", label, err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("acl: refuse path through reparse point %s", label)
	}
	return nil
}

func fileIdentity(handle windows.Handle) (FileIdentity, error) {
	var info fileIDInfo
	if err := windows.GetFileInformationByHandleEx(
		handle,
		windows.FileIdInfo,
		(*byte)(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		return FileIdentity{}, err
	}
	if info.FileID == ([16]byte{}) {
		return FileIdentity{}, fmt.Errorf("filesystem returned an empty file ID")
	}
	return FileIdentity(info), nil
}

func readDACL(object managedObject) (daclSnapshot, error) {
	identity, err := fileIdentity(object.handle)
	if err != nil {
		return daclSnapshot{}, fmt.Errorf("acl: identify %s while reading DACL: %w", object.path, err)
	}
	sd, err := windows.GetSecurityInfo(
		object.handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return daclSnapshot{}, fmt.Errorf("acl: read %s owner/DACL: %w", object.path, err)
	}
	if sd == nil {
		return daclSnapshot{}, fmt.Errorf("acl: %s has no security descriptor", object.path)
	}
	control, _, err := sd.Control()
	if err != nil {
		return daclSnapshot{}, fmt.Errorf("acl: read %s DACL control: %w", object.path, err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.IsValid() {
		if err == nil {
			err = fmt.Errorf("owner SID is missing or invalid")
		}
		return daclSnapshot{}, fmt.Errorf("acl: read %s owner: %w", object.path, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		if errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) {
			return daclSnapshot{}, fmt.Errorf("acl: refuse %s without a DACL", object.path)
		}
		return daclSnapshot{}, fmt.Errorf("acl: read %s DACL: %w", object.path, err)
	}
	if dacl == nil {
		return daclSnapshot{}, fmt.Errorf("acl: refuse null DACL on %s", object.path)
	}
	aces, revision, err := copyRawACEs(dacl)
	if err != nil {
		return daclSnapshot{}, fmt.Errorf("acl: read %s ACEs: %w", object.path, err)
	}
	const daclControlMask = windows.SE_DACL_PRESENT |
		windows.SE_DACL_DEFAULTED |
		windows.SE_DACL_AUTO_INHERIT_REQ |
		windows.SE_DACL_AUTO_INHERITED |
		windows.SE_DACL_PROTECTED
	runtime.KeepAlive(sd)
	return daclSnapshot{
		identity:    identity,
		ownerSID:    owner.String(),
		daclControl: uint16(control & daclControlMask),
		revision:    revision,
		aces:        aces,
	}, nil
}

func writeDACL(object managedObject, revision byte, aces [][]byte) error {
	dacl, err := aclFromRawACEs(revision, aces)
	if err != nil {
		return fmt.Errorf("acl: build exact DACL for %s: %w", object.path, err)
	}
	if err := windows.SetSecurityInfo(
		object.handle,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		return &DACLWriteAccessError{Path: object.path, Err: err}
	}
	runtime.KeepAlive(dacl)
	return nil
}

func exactACE(entry Entry) ([]byte, error) {
	principal := strings.TrimSpace(entry.Principal)
	if principal == "" {
		return nil, fmt.Errorf("acl: principal is required")
	}
	var aceType byte
	switch entry.Mode {
	case "", Grant:
		aceType = windows.ACCESS_ALLOWED_ACE_TYPE
	case Deny:
		aceType = windows.ACCESS_DENIED_ACE_TYPE
	default:
		return nil, fmt.Errorf("acl: exact ACE mode %q is not a basic allow or deny", entry.Mode)
	}
	switch entry.Rights {
	case ReadExecute, Traverse, Write, Modify, FullControl:
	default:
		return nil, fmt.Errorf("acl: exact ACE rights %q are unsupported", entry.Rights)
	}
	_, sid, err := trustee(principal)
	if err != nil {
		return nil, err
	}
	sidLength := sid.Len()
	aceLength := 8 + sidLength
	if aceLength > math.MaxUint16 {
		return nil, fmt.Errorf("acl: exact ACE for %s is too large", principal)
	}
	raw := make([]byte, aceLength)
	raw[0] = aceType
	if entry.Inherit {
		raw[1] = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	}
	binary.LittleEndian.PutUint16(raw[2:4], uint16(aceLength))
	binary.LittleEndian.PutUint32(raw[4:8], uint32(rightsMask(entry.Rights)))
	copy(raw[8:], unsafe.Slice((*byte)(unsafe.Pointer(sid)), sidLength))
	runtime.KeepAlive(sid)
	return raw, nil
}

func copyRawACEs(dacl *windows.ACL) ([][]byte, byte, error) {
	header := (*aclHeader)(unsafe.Pointer(dacl))
	if header.Revision != aclRevision && header.Revision != aclRevisionDS {
		return nil, 0, fmt.Errorf("unsupported ACL revision %d", header.Revision)
	}
	aces := make([][]byte, 0, dacl.AceCount)
	used := uint32(unsafe.Sizeof(aclHeader{}))
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return nil, 0, fmt.Errorf("read ACE %d: %w", i, err)
		}
		if ace == nil {
			return nil, 0, fmt.Errorf("read ACE %d: nil ACE", i)
		}
		aceHeader := (*windows.ACE_HEADER)(unsafe.Pointer(ace))
		size := uint32(aceHeader.AceSize)
		if size < uint32(unsafe.Sizeof(windows.ACE_HEADER{})) || used+size > uint32(header.Size) {
			return nil, 0, fmt.Errorf("ACE %d has invalid size %d", i, size)
		}
		raw := make([]byte, size)
		copy(raw, unsafe.Slice((*byte)(unsafe.Pointer(ace)), size))
		aces = append(aces, raw)
		used += size
	}
	runtime.KeepAlive(dacl)
	return aces, header.Revision, nil
}

func aclFromRawACEs(revision byte, aces [][]byte) (*windows.ACL, error) {
	if revision != aclRevision && revision != aclRevisionDS {
		return nil, fmt.Errorf("unsupported ACL revision %d", revision)
	}
	if len(aces) > math.MaxUint16 {
		return nil, fmt.Errorf("ACL has too many ACEs")
	}
	size := uint32(unsafe.Sizeof(aclHeader{}))
	for i, ace := range aces {
		if err := validateRawACE(ace, false); err != nil {
			return nil, fmt.Errorf("ACE %d: %w", i, err)
		}
		size += uint32(len(ace))
		if size > math.MaxUint16 {
			return nil, fmt.Errorf("ACL is too large")
		}
	}
	buf := make([]byte, size)
	header := (*aclHeader)(unsafe.Pointer(&buf[0]))
	header.Revision = revision
	header.Size = uint16(size)
	header.ACECount = uint16(len(aces))
	offset := int(unsafe.Sizeof(aclHeader{}))
	for _, ace := range aces {
		copy(buf[offset:], ace)
		offset += len(ace)
	}
	runtime.KeepAlive(buf)
	runtime.KeepAlive(aces)
	return (*windows.ACL)(unsafe.Pointer(&buf[0])), nil
}

func insertCanonicalACE(aces [][]byte, managed []byte) [][]byte {
	index := len(aces)
	managedDeny := managed[0] == windows.ACCESS_DENIED_ACE_TYPE
	for i, ace := range aces {
		if ace[1]&windows.INHERITED_ACE != 0 {
			index = i
			break
		}
		if managedDeny && !isDenyACEType(ace[0]) {
			index = i
			break
		}
	}
	out := make([][]byte, 0, len(aces)+1)
	out = append(out, aces[:index]...)
	out = append(out, managed)
	out = append(out, aces[index:]...)
	return out
}

func isDenyACEType(aceType byte) bool {
	switch aceType {
	case windows.ACCESS_DENIED_ACE_TYPE,
		accessDeniedObjectACEType,
		accessDeniedCallbackACEType,
		accessDeniedCallbackObjectACEType:
		return true
	default:
		return false
	}
}

func removeOneExactACE(aces [][]byte, exact []byte) ([][]byte, bool) {
	for i, ace := range aces {
		if bytes.Equal(ace, exact) {
			out := make([][]byte, 0, len(aces)-1)
			out = append(out, aces[:i]...)
			out = append(out, aces[i+1:]...)
			return out, true
		}
	}
	return aces, false
}

func countExactACE(aces [][]byte, exact []byte) uint32 {
	var count uint32
	for _, ace := range aces {
		if bytes.Equal(ace, exact) {
			count++
		}
	}
	return count
}

func equalACESequence(left, right [][]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if !bytes.Equal(left[i], right[i]) {
			return false
		}
	}
	return true
}

func newACEReceipt(path string, snapshot daclSnapshot, raw []byte, baseline uint32, owned bool, appliedControl uint16) ACEReceipt {
	digest := sha256.Sum256(raw)
	applied := snapshot
	applied.daclControl = appliedControl
	if owned && countExactACE(snapshot.aces, raw) == 0 {
		applied.aces = insertCanonicalACE(snapshot.aces, raw)
	}
	return ACEReceipt{
		Version:            aceReceiptVersion,
		Path:               path,
		FileIdentity:       snapshot.identity,
		RawACE:             append([]byte(nil), raw...),
		RawACESHA256:       hex.EncodeToString(digest[:]),
		BaselineDACLSHA256: daclSnapshotSHA256(snapshot),
		AppliedDACLSHA256:  daclSnapshotSHA256(applied),
		Owned:              owned,
		BaselineExactCount: baseline,
		OwnerSID:           snapshot.ownerSID,
		DACLControl:        snapshot.daclControl,
		AppliedDACLControl: appliedControl,
	}
}

func validateACEReceipt(receipt ACEReceipt) error {
	if receipt.Version != aceReceiptVersion {
		return fmt.Errorf("acl: unsupported exact ACE receipt version %d", receipt.Version)
	}
	if strings.TrimSpace(receipt.Path) == "" {
		return fmt.Errorf("acl: exact ACE receipt path is required")
	}
	if receipt.FileIdentity.FileID == ([16]byte{}) {
		return fmt.Errorf("acl: exact ACE receipt file ID is required")
	}
	if strings.TrimSpace(receipt.OwnerSID) == "" {
		return fmt.Errorf("acl: exact ACE receipt owner SID is required")
	}
	if receipt.DACLControl&windows.SE_DACL_PRESENT == 0 || receipt.AppliedDACLControl&windows.SE_DACL_PRESENT == 0 {
		return fmt.Errorf("acl: exact ACE receipt DACL control is invalid")
	}
	if err := validateRawACE(receipt.RawACE, true); err != nil {
		return fmt.Errorf("acl: invalid managed exact ACE: %w", err)
	}
	digest := sha256.Sum256(receipt.RawACE)
	if !strings.EqualFold(receipt.RawACESHA256, hex.EncodeToString(digest[:])) {
		return fmt.Errorf("acl: exact ACE receipt digest does not match")
	}
	if len(receipt.BaselineDACLSHA256) != sha256.Size*2 || len(receipt.AppliedDACLSHA256) != sha256.Size*2 {
		return fmt.Errorf("acl: exact ACE receipt DACL fingerprint is invalid")
	}
	if receipt.Owned && receipt.BaselineExactCount != 0 {
		return fmt.Errorf("acl: owned exact ACE receipt has ambiguous baseline count %d", receipt.BaselineExactCount)
	}
	if !receipt.Owned && receipt.BaselineExactCount == 0 {
		return fmt.Errorf("acl: borrowed exact ACE receipt has no baseline occurrence")
	}
	return nil
}

func validateRawACE(raw []byte, managed bool) error {
	if len(raw) < int(unsafe.Sizeof(windows.ACE_HEADER{})) {
		return fmt.Errorf("ACE is too short")
	}
	size := int(binary.LittleEndian.Uint16(raw[2:4]))
	if size != len(raw) {
		return fmt.Errorf("ACE size = %d, want %d", size, len(raw))
	}
	if size%4 != 0 {
		return fmt.Errorf("ACE size %d is not DWORD aligned", size)
	}
	if !managed {
		return nil
	}
	if len(raw) < 12 {
		return fmt.Errorf("basic ACE is too short")
	}
	if raw[0] != windows.ACCESS_ALLOWED_ACE_TYPE && raw[0] != windows.ACCESS_DENIED_ACE_TYPE {
		return fmt.Errorf("ACE type %d is not a basic allow or deny", raw[0])
	}
	if raw[1]&windows.INHERITED_ACE != 0 {
		return fmt.Errorf("managed ACE must be explicit")
	}
	allowedFlags := byte(windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE)
	if raw[1]&^allowedFlags != 0 {
		return fmt.Errorf("managed ACE flags %#x are unsupported", raw[1])
	}
	sid := (*windows.SID)(unsafe.Pointer(&raw[8]))
	if !sid.IsValid() || 8+sid.Len() != len(raw) {
		return fmt.Errorf("managed ACE SID is invalid")
	}
	runtime.KeepAlive(raw)
	return nil
}

func verifyReceiptSnapshot(receipt ACEReceipt, current daclSnapshot) error {
	if err := verifyReceiptIdentity(receipt, current); err != nil {
		return err
	}
	wantControl := receipt.DACLControl
	if receipt.Owned {
		wantControl = receipt.AppliedDACLControl
	}
	if err := verifyReceiptControl(receipt.Path, wantControl, current); err != nil {
		return err
	}
	want := receipt.BaselineExactCount
	if receipt.Owned {
		want++
	}
	count := countExactACE(current.aces, receipt.RawACE)
	if count != want {
		return fmt.Errorf("acl: exact ACE count = %d, want %d", count, want)
	}
	return nil
}

func verifyReceiptIdentity(receipt ACEReceipt, current daclSnapshot) error {
	if current.identity != receipt.FileIdentity {
		return fmt.Errorf("acl: file identity changed for receipt path %s", receipt.Path)
	}
	if !strings.EqualFold(current.ownerSID, receipt.OwnerSID) {
		return fmt.Errorf("acl: owner changed for receipt path %s", receipt.Path)
	}
	return nil
}

func verifyReceiptControl(path string, want uint16, current daclSnapshot) error {
	if current.daclControl != want {
		return fmt.Errorf("acl: DACL control changed for receipt path %s", path)
	}
	return nil
}

func verifyPostWriteObject(before, after daclSnapshot, expectedControl uint16) error {
	if before.identity != after.identity {
		return fmt.Errorf("file identity changed")
	}
	if !strings.EqualFold(before.ownerSID, after.ownerSID) {
		return fmt.Errorf("owner changed")
	}
	if after.daclControl != expectedControl {
		return fmt.Errorf("DACL control changed from %#x to unexpected %#x (want %#x)", before.daclControl, after.daclControl, expectedControl)
	}
	if before.revision != after.revision {
		return fmt.Errorf("ACL revision changed from %d to %d", before.revision, after.revision)
	}
	return nil
}

func expectedPostWriteDACLControl(control uint16) uint16 {
	if control&windows.SE_DACL_PROTECTED == 0 {
		control &^= windows.SE_DACL_AUTO_INHERIT_REQ
		control |= windows.SE_DACL_AUTO_INHERITED
	}
	return control
}

func daclSnapshotSHA256(snapshot daclSnapshot) string {
	digest := sha256.New()
	var header [5]byte
	header[0] = snapshot.revision
	binary.LittleEndian.PutUint16(header[1:3], snapshot.daclControl)
	binary.LittleEndian.PutUint16(header[3:5], uint16(len(snapshot.aces)))
	_, _ = digest.Write(header[:])
	for _, ace := range snapshot.aces {
		var size [4]byte
		binary.LittleEndian.PutUint32(size[:], uint32(len(ace)))
		_, _ = digest.Write(size[:])
		_, _ = digest.Write(ace)
	}
	return hex.EncodeToString(digest.Sum(nil))
}
