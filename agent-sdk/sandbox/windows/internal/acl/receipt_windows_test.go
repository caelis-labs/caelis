//go:build windows

package acl

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

const testCapabilitySID = "S-1-5-21-1414213562-1732050807-2236067977-424242"

func TestPreparedExactFileDACLReceiptIsDurableBeforeEffect(t *testing.T) {
	dir := t.TempDir()
	entry := Entry{Principal: testCapabilitySID, Rights: Modify, Mode: Grant, Inherit: true}
	receipt, err := PrepareExactFileDACLEntry(dir, entry)
	if err != nil {
		t.Fatalf("PrepareExactFileDACLEntry() error = %v", err)
	}
	if !receipt.Owned {
		t.Fatal("PrepareExactFileDACLEntry().Owned = false, want true")
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("json.Marshal(receipt) error = %v", err)
	}
	var durable ACEReceipt
	if err := json.Unmarshal(data, &durable); err != nil {
		t.Fatalf("json.Unmarshal(receipt) error = %v", err)
	}
	if err := VerifyFileDACLReceipt(dir, durable); err == nil {
		t.Fatal("VerifyFileDACLReceipt(before effect) error = nil, want missing ACE")
	}
	if err := EnsureFileDACLReceipt(dir, durable); err != nil {
		t.Fatalf("EnsureFileDACLReceipt(first) error = %v", err)
	}
	if err := EnsureFileDACLReceipt(dir, durable); err != nil {
		t.Fatalf("EnsureFileDACLReceipt(exact postimage recovery) error = %v", err)
	}
	if err := VerifyFileDACLReceipt(dir, durable); err != nil {
		t.Fatalf("VerifyFileDACLReceipt(after effect) error = %v", err)
	}
	if _, err := RemoveFileDACLReceipt(dir, durable); err != nil {
		t.Fatalf("RemoveFileDACLReceipt() error = %v", err)
	}
}

func TestExactFileDACLReceiptOwnedRoundTrip(t *testing.T) {
	dir := t.TempDir()
	before := readTestSnapshot(t, dir)
	entry := Entry{Principal: testCapabilitySID, Rights: Modify, Mode: Grant, Inherit: true}

	receipt, err := EnsureExactFileDACLEntry(dir, entry)
	if err != nil {
		t.Fatalf("EnsureExactFileDACLEntry() error = %v", err)
	}
	if !receipt.Owned {
		t.Fatal("EnsureExactFileDACLEntry().Owned = false, want true")
	}
	if receipt.BaselineExactCount != 0 {
		t.Fatalf("BaselineExactCount = %d, want 0", receipt.BaselineExactCount)
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("json.Marshal(receipt) error = %v", err)
	}
	var durable ACEReceipt
	if err := json.Unmarshal(data, &durable); err != nil {
		t.Fatalf("json.Unmarshal(receipt) error = %v", err)
	}
	if err := VerifyFileDACLReceipt(dir, durable); err != nil {
		t.Fatalf("VerifyFileDACLReceipt() error = %v", err)
	}
	removed, err := RemoveFileDACLReceipt(dir, durable)
	if err != nil {
		t.Fatalf("RemoveFileDACLReceipt() error = %v", err)
	}
	if !removed {
		t.Fatal("RemoveFileDACLReceipt() removed = false, want true")
	}
	after := readTestSnapshot(t, dir)
	if err := verifyPostWriteObject(before, after, expectedPostWriteDACLControl(before.daclControl)); err != nil {
		t.Fatalf("object metadata after round trip: %v", err)
	}
	if !equalACESequence(after.aces, before.aces) {
		t.Fatal("DACL after receipt round trip differs from original DACL")
	}
	removed, err = RemoveFileDACLReceipt(dir, durable)
	if err != nil {
		t.Fatalf("RemoveFileDACLReceipt(idempotent) error = %v", err)
	}
	if removed {
		t.Fatal("RemoveFileDACLReceipt(idempotent) removed = true, want false")
	}
}

func TestExactFileDACLReceiptBorrowsPreexistingACE(t *testing.T) {
	dir := t.TempDir()
	entry := Entry{Principal: testCapabilitySID, Rights: ReadExecute, Mode: Grant, Inherit: true}
	owned, err := EnsureExactFileDACLEntry(dir, entry)
	if err != nil {
		t.Fatalf("EnsureExactFileDACLEntry(first) error = %v", err)
	}
	borrowed, err := EnsureExactFileDACLEntry(dir, entry)
	if err != nil {
		t.Fatalf("EnsureExactFileDACLEntry(second) error = %v", err)
	}
	if borrowed.Owned {
		t.Fatal("second receipt Owned = true, want borrowed")
	}
	if borrowed.BaselineExactCount != 1 {
		t.Fatalf("borrowed BaselineExactCount = %d, want 1", borrowed.BaselineExactCount)
	}
	removed, err := RemoveFileDACLReceipt(dir, borrowed)
	if err != nil {
		t.Fatalf("RemoveFileDACLReceipt(borrowed) error = %v", err)
	}
	if removed {
		t.Fatal("RemoveFileDACLReceipt(borrowed) removed = true, want false")
	}
	if err := VerifyFileDACLReceipt(dir, owned); err != nil {
		t.Fatalf("owned receipt after borrowed removal error = %v", err)
	}
	if _, err := RemoveFileDACLReceipt(dir, owned); err != nil {
		t.Fatalf("RemoveFileDACLReceipt(owned) error = %v", err)
	}
}

func TestRemoveFileDACLReceiptPreservesDifferentACEForSameSID(t *testing.T) {
	dir := t.TempDir()
	grant := Entry{Principal: testCapabilitySID, Rights: ReadExecute, Mode: Grant, Inherit: true}
	deny := Entry{Principal: testCapabilitySID, Rights: Write, Mode: Deny, Inherit: true}
	grantReceipt, err := EnsureExactFileDACLEntry(dir, grant)
	if err != nil {
		t.Fatalf("EnsureExactFileDACLEntry(grant) error = %v", err)
	}
	denyReceipt, err := EnsureExactFileDACLEntry(dir, deny)
	if err != nil {
		t.Fatalf("EnsureExactFileDACLEntry(deny) error = %v", err)
	}
	if _, err := RemoveFileDACLReceipt(dir, grantReceipt); err != nil {
		t.Fatalf("RemoveFileDACLReceipt(grant) error = %v", err)
	}
	if err := VerifyFileDACLReceipt(dir, denyReceipt); err != nil {
		t.Fatalf("deny receipt after grant removal error = %v", err)
	}
	if _, err := RemoveFileDACLReceipt(dir, denyReceipt); err != nil {
		t.Fatalf("RemoveFileDACLReceipt(deny) error = %v", err)
	}
}

func TestEnsureFileDACLReceiptRejectsInterveningDACLChange(t *testing.T) {
	dir := t.TempDir()
	managed := Entry{Principal: testCapabilitySID, Rights: Modify, Mode: Grant, Inherit: true}
	intervening := Entry{Principal: testCapabilitySID, Rights: Write, Mode: Deny, Inherit: true}
	receipt, err := PrepareExactFileDACLEntry(dir, managed)
	if err != nil {
		t.Fatalf("PrepareExactFileDACLEntry() error = %v", err)
	}
	if err := ModifyFileDACL(dir, intervening); err != nil {
		t.Fatalf("ModifyFileDACL(intervening) error = %v", err)
	}
	if err := EnsureFileDACLReceipt(dir, receipt); err == nil {
		t.Fatal("EnsureFileDACLReceipt() error = nil, want intervening-change rejection")
	}
	if missing, err := MissingFileDACLEntries(dir, intervening); err != nil || len(missing) != 0 {
		t.Fatalf("intervening ACE after ensure = %#v/%v, want present", missing, err)
	}
	if missing, err := MissingFileDACLEntries(dir, managed); err != nil || len(missing) != 1 {
		t.Fatalf("managed ACE after rejected ensure = %#v/%v, want absent", missing, err)
	}
}

func TestExpectedPostWriteDACLControlRecordsAutoInheritedTransition(t *testing.T) {
	before := uint16(windows.SE_DACL_PRESENT)
	want := uint16(windows.SE_DACL_PRESENT | windows.SE_DACL_AUTO_INHERITED)
	if got := expectedPostWriteDACLControl(before); got != want {
		t.Fatalf("expectedPostWriteDACLControl(%#x) = %#x, want %#x", before, got, want)
	}
}

func TestExactACESequencePreservesUnknownObjectCallbackAndInheritedACEs(t *testing.T) {
	denyObject := testRawACE(accessDeniedObjectACEType, 0, []byte{1, 2, 3, 4})
	callbackAllow := testRawACE(0x09, 0, []byte{5, 6, 7, 8})
	unknown := testRawACE(0x42, 0, []byte{9, 10, 11, 12})
	inherited := testRawACE(windows.ACCESS_ALLOWED_ACE_TYPE, windows.INHERITED_ACE, []byte{13, 14, 15, 16})
	original := [][]byte{denyObject, callbackAllow, unknown, inherited}
	managed := testRawACE(windows.ACCESS_DENIED_ACE_TYPE, 0, []byte{21, 22, 23, 24})

	inserted := insertCanonicalACE(original, managed)
	want := [][]byte{denyObject, managed, callbackAllow, unknown, inherited}
	if !equalACESequence(inserted, want) {
		t.Fatalf("insertCanonicalACE() = %#v, want %#v", inserted, want)
	}
	removed, ok := removeOneExactACE(inserted, managed)
	if !ok {
		t.Fatal("removeOneExactACE() removed = false, want true")
	}
	if !equalACESequence(removed, original) {
		t.Fatal("non-managed raw ACE sequence changed after insert/remove")
	}
	for i := range original {
		if !bytes.Equal(removed[i], original[i]) {
			t.Fatalf("raw ACE %d changed", i)
		}
	}
}

func TestRawACLRoundTripPreservesUnknownObjectAndCallbackACEs(t *testing.T) {
	original := [][]byte{
		testRawACE(accessDeniedObjectACEType, 0, []byte{1, 2, 3, 4}),
		testRawACE(0x09, 0, []byte{5, 6, 7, 8}),
		testRawACE(0x42, 0, []byte{9, 10, 11, 12}),
		testRawACE(windows.ACCESS_ALLOWED_ACE_TYPE, windows.INHERITED_ACE, []byte{13, 14, 15, 16}),
	}
	dacl, err := aclFromRawACEs(aclRevisionDS, original)
	if err != nil {
		t.Fatalf("aclFromRawACEs() error = %v", err)
	}
	roundTrip, revision, err := copyRawACEs(dacl)
	if err != nil {
		t.Fatalf("copyRawACEs() error = %v", err)
	}
	if revision != aclRevisionDS {
		t.Fatalf("ACL revision = %d, want %d", revision, aclRevisionDS)
	}
	if !equalACESequence(roundTrip, original) {
		t.Fatal("raw ACL round trip changed unknown/object/callback/inherited ACEs")
	}
}

func TestExactReceiptPreservesRealObjectAndCallbackACEs(t *testing.T) {
	dir := t.TempDir()
	basic, err := exactACE(Entry{Principal: testCapabilitySID, Rights: ReadExecute, Mode: Grant})
	if err != nil {
		t.Fatalf("exactACE() error = %v", err)
	}
	callback := append([]byte(nil), basic...)
	callback[0] = 0x09
	objectACE := make([]byte, len(basic)+4)
	objectACE[0] = 0x05
	binary.LittleEndian.PutUint16(objectACE[2:4], uint16(len(objectACE)))
	copy(objectACE[4:8], basic[4:8])
	copy(objectACE[12:], basic[8:])

	object, err := openManagedObject(dir, true)
	if err != nil {
		t.Fatalf("openManagedObject() error = %v", err)
	}
	before, err := readDACL(object)
	if err != nil {
		object.close()
		t.Fatalf("readDACL() error = %v", err)
	}
	withSpecial := insertCanonicalACE(before.aces, callback)
	withSpecial = insertCanonicalACE(withSpecial, objectACE)
	if err := writeDACL(object, aclRevisionDS, withSpecial); err != nil {
		object.close()
		t.Fatalf("writeDACL(object/callback) error = %v", err)
	}
	installed, err := readDACL(object)
	object.close()
	if err != nil {
		t.Fatalf("readDACL(after object/callback) error = %v", err)
	}
	if !equalACESequence(installed.aces, withSpecial) {
		t.Fatal("Windows SetSecurityInfo changed real object/callback ACEs")
	}

	receipt, err := EnsureExactFileDACLEntry(dir, Entry{
		Principal: "S-1-5-21-1414213562-1732050807-2236067977-434343",
		Rights:    Modify,
		Mode:      Grant,
		Inherit:   true,
	})
	if err != nil {
		t.Fatalf("EnsureExactFileDACLEntry() error = %v", err)
	}
	if _, err := RemoveFileDACLReceipt(dir, receipt); err != nil {
		t.Fatalf("RemoveFileDACLReceipt() error = %v", err)
	}
	after := readTestSnapshot(t, dir)
	if !equalACESequence(after.aces, withSpecial) {
		t.Fatal("receipt round trip changed real object/callback ACEs")
	}
}

func TestExactFileDACLReceiptPreservesProtectedDACL(t *testing.T) {
	dir := t.TempDir()
	descriptor, err := ReadFileDACL(dir)
	if err != nil {
		t.Fatalf("ReadFileDACL() error = %v", err)
	}
	if err := WriteFileDACL(dir, descriptor, true); err != nil {
		t.Fatalf("WriteFileDACL(protected) error = %v", err)
	}
	before := readTestSnapshot(t, dir)
	if before.daclControl&windows.SE_DACL_PROTECTED == 0 {
		t.Skip("filesystem did not retain SE_DACL_PROTECTED")
	}
	receipt, err := EnsureExactFileDACLEntry(dir, Entry{
		Principal: testCapabilitySID,
		Rights:    Modify,
		Mode:      Grant,
		Inherit:   true,
	})
	if err != nil {
		t.Fatalf("EnsureExactFileDACLEntry() error = %v", err)
	}
	if _, err := RemoveFileDACLReceipt(dir, receipt); err != nil {
		t.Fatalf("RemoveFileDACLReceipt() error = %v", err)
	}
	after := readTestSnapshot(t, dir)
	if err := verifyPostWriteObject(before, after, expectedPostWriteDACLControl(before.daclControl)); err != nil {
		t.Fatalf("protected object metadata after round trip: %v", err)
	}
}

func TestExactFileDACLReceiptRejectsReparsePoint(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(target, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		output, junctionErr := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, target).CombinedOutput()
		if junctionErr != nil {
			t.Skipf("directory symlink/junction unavailable: symlink=%v junction=%v (%s)", err, junctionErr, strings.TrimSpace(string(output)))
		}
		defer func() { _ = os.Remove(link) }()
	}
	_, err := EnsureExactFileDACLEntry(filepath.Join(link, "child"), Entry{
		Principal: testCapabilitySID,
		Rights:    Modify,
		Mode:      Grant,
		Inherit:   true,
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "reparse") {
		t.Fatalf("EnsureExactFileDACLEntry(reparse) error = %v, want reparse rejection", err)
	}
}

func TestExactFileDACLReceiptRejectsPathReplacement(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "managed")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	receipt, err := EnsureExactFileDACLEntry(path, Entry{
		Principal: testCapabilitySID,
		Rights:    Modify,
		Mode:      Grant,
		Inherit:   true,
	})
	if err != nil {
		t.Fatalf("EnsureExactFileDACLEntry() error = %v", err)
	}
	moved := filepath.Join(base, "original")
	if err := os.Rename(path, moved); err != nil {
		t.Fatalf("os.Rename() error = %v", err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := VerifyFileDACLReceipt(path, receipt); err == nil || !strings.Contains(strings.ToLower(err.Error()), "identity") {
		t.Fatalf("VerifyFileDACLReceipt(replacement) error = %v, want identity rejection", err)
	}
	if _, err := RemoveFileDACLReceipt(path, receipt); err == nil || !strings.Contains(strings.ToLower(err.Error()), "identity") {
		t.Fatalf("RemoveFileDACLReceipt(replacement) error = %v, want identity rejection", err)
	}
	if _, err := RemoveFileDACLReceipt(moved, receipt); err != nil {
		t.Fatalf("RemoveFileDACLReceipt(original object) error = %v", err)
	}
}

func TestExactFileDACLReceiptRejectsNullDACL(t *testing.T) {
	dir := t.TempDir()
	original, err := ReadFileDACL(dir)
	if err != nil {
		t.Fatalf("ReadFileDACL() error = %v", err)
	}
	info, err := InspectFileDACL(dir)
	if err != nil {
		t.Fatalf("InspectFileDACL() error = %v", err)
	}
	defer func() {
		if err := WriteFileDACL(dir, original, info.Protected); err != nil {
			t.Errorf("restore DACL: %v", err)
		}
	}()
	if err := windows.SetNamedSecurityInfo(
		dir,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
		nil,
		nil,
		nil,
		nil,
	); err != nil {
		t.Fatalf("SetNamedSecurityInfo(null DACL) error = %v", err)
	}
	_, err = EnsureExactFileDACLEntry(dir, Entry{
		Principal: testCapabilitySID,
		Rights:    Modify,
		Mode:      Grant,
		Inherit:   true,
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "null dacl") {
		t.Fatalf("EnsureExactFileDACLEntry(null DACL) error = %v, want null-DACL rejection", err)
	}
}

func readTestSnapshot(t *testing.T, path string) daclSnapshot {
	t.Helper()
	object, err := openManagedObject(path, false)
	if err != nil {
		t.Fatalf("openManagedObject(%s) error = %v", path, err)
	}
	defer object.close()
	snapshot, err := readDACL(object)
	if err != nil {
		t.Fatalf("readDACL(%s) error = %v", path, err)
	}
	return snapshot
}

func testRawACE(aceType, flags byte, payload []byte) []byte {
	raw := make([]byte, 4+len(payload))
	raw[0] = aceType
	raw[1] = flags
	binary.LittleEndian.PutUint16(raw[2:4], uint16(len(raw)))
	copy(raw[4:], payload)
	return raw
}
