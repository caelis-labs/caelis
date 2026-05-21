//go:build windows

package win32

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
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

func TestRestrictedCurrentProcessTokenWithCapabilitySIDsE2E(t *testing.T) {
	if os.Getenv("CAELIS_WINDOWS_SANDBOX_E2E") != "1" {
		t.Skip("set CAELIS_WINDOWS_SANDBOX_E2E=1 to run restricted token e2e")
	}
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

func TestLogonOptionsDefaultToNoProfileAndExplicitEnvironment(t *testing.T) {
	if flags := logonFlagsForOptions(LogonProcessOptions{}); flags != 0 {
		t.Fatalf("default logon flags = %#x, want no profile", flags)
	}
	if flags := logonFlagsForOptions(LogonProcessOptions{LoadProfile: true}); flags&logonWithProfile == 0 {
		t.Fatalf("profile logon flags = %#x, want LOGON_WITH_PROFILE", flags)
	}
	block, err := environmentBlock([]string{`SystemRoot=C:\Windows`, `TEMP=C:\Temp`})
	if err != nil {
		t.Fatalf("environmentBlock() error = %v", err)
	}
	got := string(utf16.Decode(block))
	if !strings.Contains(got, "SystemRoot=C:\\Windows\x00") || !strings.HasSuffix(got, "\x00\x00") {
		t.Fatalf("environment block = %q, want double-NUL terminated Unicode environment", got)
	}
}

func TestDecodeCodePageToUTF8(t *testing.T) {
	gbkDate := []byte{0x32, 0x30, 0x32, 0x36, 0xc4, 0xea, 0x35, 0xd4, 0xc2, 0x31, 0x39, 0xc8, 0xd5, 0x20, 0x31, 0x37, 0x3a, 0x32, 0x31, 0x3a, 0x34, 0x34}
	got, err := decodeCodePageToUTF8(936, gbkDate)
	if err != nil {
		t.Fatalf("decodeCodePageToUTF8() error = %v", err)
	}
	want := "2026\u5e745\u670819\u65e5 17:21:44"
	if string(got) != want {
		t.Fatalf("decodeCodePageToUTF8() = %q, want %q", string(got), want)
	}
}

func TestWithNamedMutexAllowsExistingMutex(t *testing.T) {
	name := fmt.Sprintf(`Local\CaelisTestMutex-%d`, time.Now().UnixNano())
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatalf("UTF16PtrFromString() error = %v", err)
	}
	handle, err := windows.CreateMutex(nil, false, namePtr)
	if handle == 0 {
		t.Fatalf("CreateMutex() handle = 0, err = %v", err)
	}
	defer closeHandle(handle)

	called := false
	if err := WithNamedMutex(context.Background(), name, time.Second, func() error {
		called = true
		return nil
	}); err != nil {
		t.Fatalf("WithNamedMutex() error = %v", err)
	}
	if !called {
		t.Fatal("WithNamedMutex() did not run callback")
	}
}
