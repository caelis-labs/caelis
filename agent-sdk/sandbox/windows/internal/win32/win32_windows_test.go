//go:build windows

package win32

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestRestrictedCurrentProcessTokenWithSIDsE2E(t *testing.T) {
	if os.Getenv("CAELIS_WINDOWS_SANDBOX_E2E") != "1" {
		t.Skip("set CAELIS_WINDOWS_SANDBOX_E2E=1 to run restricted token e2e")
	}
	token, err := RestrictedCurrentProcessTokenWithSIDs([]string{"S-1-5-21-1-2-3-4"})
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

func TestRestrictingSIDAttributesIncludePowerShellCompatibilitySIDs(t *testing.T) {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		t.Fatalf("OpenProcessToken() error = %v", err)
	}
	defer token.Close()
	requested, err := windows.StringToSid("S-1-5-21-1-2-3-4")
	if err != nil {
		t.Fatalf("StringToSid(requested) error = %v", err)
	}
	userSID, err := tokenUserSID(token)
	if err != nil {
		t.Fatalf("tokenUserSID() error = %v", err)
	}
	logonSID, err := tokenLogonSID(token)
	if err != nil {
		t.Fatalf("tokenLogonSID() error = %v", err)
	}
	everyoneSID, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatalf("CreateWellKnownSid(world) error = %v", err)
	}
	usersSID, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	if err != nil {
		t.Fatalf("CreateWellKnownSid(users) error = %v", err)
	}
	interactiveSID, err := windows.CreateWellKnownSid(windows.WinInteractiveSid)
	if err != nil {
		t.Fatalf("CreateWellKnownSid(interactive) error = %v", err)
	}

	attrs, _, err := restrictingSIDAttributes(token, []string{requested.String()})
	if err != nil {
		t.Fatalf("restrictingSIDAttributes() error = %v", err)
	}
	if !sidAttrsContain(attrs, requested) {
		t.Fatalf("restricting SIDs = %v, want requested %s", sidAttrsStrings(attrs), requested.String())
	}
	if !sidAttrsContain(attrs, logonSID) || !sidAttrsContain(attrs, everyoneSID) {
		t.Fatalf("restricting SIDs = %v, want logon and world compatibility SIDs", sidAttrsStrings(attrs))
	}
	for _, forbidden := range []*windows.SID{userSID, usersSID, interactiveSID} {
		if sidAttrsContain(attrs, forbidden) {
			t.Fatalf("restricting SID unexpectedly includes broad/user SID %s", forbidden.String())
		}
	}
}

func TestRestrictingSIDAttributesUsesNullSIDWhenNoWriteRootsRemain(t *testing.T) {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		t.Fatalf("OpenProcessToken() error = %v", err)
	}
	defer token.Close()
	nullSID, err := windows.CreateWellKnownSid(windows.WinNullSid)
	if err != nil {
		t.Fatalf("CreateWellKnownSid(null) error = %v", err)
	}

	attrs, _, err := restrictingSIDAttributes(token, nil)
	if err != nil {
		t.Fatalf("restrictingSIDAttributes(nil) error = %v", err)
	}
	if !sidAttrsContain(attrs, nullSID) {
		t.Fatalf("restricting SIDs = %v, want null SID when no write roots remain", sidAttrsStrings(attrs))
	}
}

func sidAttrsContain(attrs []windows.SIDAndAttributes, want *windows.SID) bool {
	for _, attr := range attrs {
		if attr.Sid != nil && windows.EqualSid(attr.Sid, want) {
			return true
		}
	}
	return false
}

func sidAttrsStrings(attrs []windows.SIDAndAttributes) []string {
	out := make([]string, 0, len(attrs))
	for _, attr := range attrs {
		if attr.Sid != nil {
			out = append(out, attr.Sid.String())
		}
	}
	return out
}

func TestRestrictingSIDAttributesDedupesInput(t *testing.T) {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		t.Fatalf("OpenProcessToken() error = %v", err)
	}
	defer token.Close()
	requested := "S-1-5-21-1-2-3-4"
	attrs, _, err := restrictingSIDAttributes(token, []string{requested, requested})
	if err != nil {
		t.Fatalf("restrictingSIDAttributes() error = %v", err)
	}
	count := 0
	for _, sid := range sidAttrsStrings(attrs) {
		if strings.EqualFold(sid, requested) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("requested SID count = %d in %v, want 1", count, sidAttrsStrings(attrs))
	}
}
