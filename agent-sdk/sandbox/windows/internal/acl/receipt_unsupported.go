//go:build !windows

package acl

import (
	"fmt"
	"runtime"
)

// PrepareExactFileDACLEntry is unavailable outside Windows.
func PrepareExactFileDACLEntry(string, Entry) (ACEReceipt, error) {
	return ACEReceipt{}, fmt.Errorf("acl: exact ACE receipts are unsupported on %s", runtime.GOOS)
}

// AdoptExistingExactFileDACLEntry is unavailable outside Windows.
func AdoptExistingExactFileDACLEntry(string, Entry) (ACEReceipt, error) {
	return ACEReceipt{}, fmt.Errorf("acl: exact ACE adoption is unsupported on %s", runtime.GOOS)
}

func ExactFileDACLEntryCount(string, Entry) (uint32, error) {
	return 0, fmt.Errorf("acl: exact ACE inspection is unsupported on %s", runtime.GOOS)
}

// ValidateACEReceiptEntry is unavailable outside Windows.
func ValidateACEReceiptEntry(ACEReceipt, Entry) error {
	return fmt.Errorf("acl: exact ACE receipt validation is unsupported on %s", runtime.GOOS)
}

// EnsureFileDACLReceipt is unavailable outside Windows.
func EnsureFileDACLReceipt(string, ACEReceipt) error {
	return fmt.Errorf("acl: exact ACE receipts are unsupported on %s", runtime.GOOS)
}

// EnsureExactFileDACLEntry is unavailable outside Windows.
func EnsureExactFileDACLEntry(string, Entry) (ACEReceipt, error) {
	return ACEReceipt{}, fmt.Errorf("acl: exact ACE receipts are unsupported on %s", runtime.GOOS)
}

// VerifyFileDACLReceipt is unavailable outside Windows.
func VerifyFileDACLReceipt(string, ACEReceipt) error {
	return fmt.Errorf("acl: exact ACE receipts are unsupported on %s", runtime.GOOS)
}

func ProbeFileDACLWriteAccess(string) error {
	return fmt.Errorf("acl: exact ACE receipts are unsupported on %s", runtime.GOOS)
}

// RemoveFileDACLReceipt is unavailable outside Windows.
func RemoveFileDACLReceipt(string, ACEReceipt) (bool, error) {
	return false, fmt.Errorf("acl: exact ACE receipts are unsupported on %s", runtime.GOOS)
}
