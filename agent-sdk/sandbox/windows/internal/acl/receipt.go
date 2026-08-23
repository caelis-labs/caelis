package acl

import "fmt"

// DACLWriteAccessError identifies a failure to acquire or use WRITE_DAC for
// an exact receipt operation. Callers may use errors.Is on Err to decide
// whether an explicit user-authorized repair can handle the failure.
type DACLWriteAccessError struct {
	Path string
	Err  error
}

func (e *DACLWriteAccessError) Error() string {
	if e == nil {
		return "acl: DACL write access failed"
	}
	return fmt.Sprintf("acl: DACL write access on %s: %v", e.Path, e.Err)
}

func (e *DACLWriteAccessError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// FileIdentity identifies one filesystem object independently of its path.
// VolumeSerialNumber and FileID are populated from FILE_ID_INFO on Windows.
type FileIdentity struct {
	VolumeSerialNumber uint64   `json:"volume_serial_number"`
	FileID             [16]byte `json:"file_id"`
}

// ACEReceipt records the exact explicit ACE intent captured by
// PrepareExactFileDACLEntry. It contains no Windows pointers and is safe to
// persist as JSON before EnsureFileDACLReceipt applies an external effect.
//
// Owned receipts authorize removal of one exact ACE occurrence. Borrowed
// receipts only attest that an identical ACE already existed and never
// authorize mutation.
type ACEReceipt struct {
	Version            int          `json:"version"`
	Path               string       `json:"path"`
	FileIdentity       FileIdentity `json:"file_identity"`
	RawACE             []byte       `json:"raw_ace"`
	RawACESHA256       string       `json:"raw_ace_sha256"`
	BaselineDACLSHA256 string       `json:"baseline_dacl_sha256"`
	AppliedDACLSHA256  string       `json:"applied_dacl_sha256"`
	Owned              bool         `json:"owned"`
	BaselineExactCount uint32       `json:"baseline_exact_count"`
	OwnerSID           string       `json:"owner_sid"`
	DACLControl        uint16       `json:"dacl_control"`
	AppliedDACLControl uint16       `json:"applied_dacl_control"`
}

// OwnershipAmbiguousError means an owned write-ahead receipt was still marked
// unapplied but an identical ACE is already present. A crash after Caelis'
// write and an independent actor adding the same raw ACE are indistinguishable,
// so recovery must preserve the ACE and fail closed rather than claim it.
type OwnershipAmbiguousError struct {
	Path string
}

func (e *OwnershipAmbiguousError) Error() string {
	if e == nil {
		return "acl: exact ACE occurrence ownership is ambiguous"
	}
	return fmt.Sprintf("acl: exact ACE occurrence ownership on %s is ambiguous; preserving it for explicit recovery", e.Path)
}
