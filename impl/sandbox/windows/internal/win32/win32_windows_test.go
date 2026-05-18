//go:build windows

package win32

import (
	"strings"
	"testing"
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
