//go:build windows

package win32

import (
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestProtectStringRoundTrip(t *testing.T) {
	protected, err := ProtectString("sandbox-secret", "caelis-test")
	if err != nil {
		t.Fatalf("ProtectString() error = %v", err)
	}
	got, err := UnprotectString(protected)
	if err != nil {
		t.Fatalf("UnprotectString() error = %v", err)
	}
	if got != "sandbox-secret" {
		t.Fatalf("UnprotectString() = %q", got)
	}
}

func TestIsElevatedReturnsValue(t *testing.T) {
	if _, err := IsElevated(); err != nil {
		t.Fatalf("IsElevated() error = %v", err)
	}
}

func TestDeriveCapabilitySIDs(t *testing.T) {
	sids, err := DeriveCapabilitySIDs("internetClient")
	if err != nil {
		t.Fatalf("DeriveCapabilitySIDs() error = %v", err)
	}
	if len(sids.Capability) == 0 {
		t.Fatalf("DeriveCapabilitySIDs() = %#v, want capability SID", sids)
	}
	if len(sids.Group) == 0 {
		t.Fatalf("DeriveCapabilitySIDs() = %#v, want capability group SID", sids)
	}
	for _, sid := range sids.Group {
		if !strings.HasPrefix(sid, "S-1-5-32-") {
			t.Fatalf("capability group SID = %q, want S-1-5-32-*", sid)
		}
	}
	for _, sid := range sids.Capability {
		if !strings.HasPrefix(sid, "S-1-15-3-") {
			t.Fatalf("capability SID = %q, want S-1-15-3-*", sid)
		}
	}
}

func TestRestrictedCurrentProcessTokenWithCapabilitySIDs(t *testing.T) {
	sids, err := DeriveCapabilitySIDs("internetClient")
	if err != nil {
		t.Fatalf("DeriveCapabilitySIDs() error = %v", err)
	}
	token, err := RestrictedCurrentProcessTokenWithSIDs(sids.Group)
	if err != nil {
		t.Fatalf("RestrictedCurrentProcessTokenWithSIDs() error = %v", err)
	}
	if token == 0 {
		t.Fatal("RestrictedCurrentProcessTokenWithSIDs() = 0 token")
	}
	if err := token.Close(); err != nil {
		t.Fatalf("token.Close() error = %v", err)
	}
}

func TestLogonProcessCreationUsesPlainStartupInfo(t *testing.T) {
	if flags := logonCreationFlags(); flags&windows.EXTENDED_STARTUPINFO_PRESENT != 0 {
		t.Fatalf("logonCreationFlags() = %#x, must not include EXTENDED_STARTUPINFO_PRESENT", flags)
	}
	startupInfo := logonStartupInfo(1, 2, 3)
	wantSize := uint32(unsafe.Sizeof(windows.StartupInfo{}))
	if startupInfo.Cb != wantSize {
		t.Fatalf("StartupInfo.Cb = %d, want %d", startupInfo.Cb, wantSize)
	}
	if startupInfo.Flags&windows.STARTF_USESTDHANDLES == 0 {
		t.Fatalf("StartupInfo.Flags = %#x, want STARTF_USESTDHANDLES", startupInfo.Flags)
	}
	if startupInfo.StdInput != 1 || startupInfo.StdOutput != 2 || startupInfo.StdErr != 3 {
		t.Fatalf("StartupInfo std handles = %d/%d/%d", startupInfo.StdInput, startupInfo.StdOutput, startupInfo.StdErr)
	}
}
