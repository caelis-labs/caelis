package bwrap

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestBwrapProbeFailureDetailDetectsAppArmorUserNSRestriction(t *testing.T) {
	detail := bwrapProbeFailureDetail(
		"/usr/bin/bwrap",
		"bwrap: Creating new namespace failed: Permission denied",
		func(string) (os.FileInfo, error) {
			return fakeFileInfo{mode: 0o755}, nil
		},
		func(name string) ([]byte, error) {
			switch name {
			case "/proc/sys/kernel/apparmor_restrict_unprivileged_userns":
				return []byte("1\n"), nil
			case "/sys/kernel/security/apparmor/profiles":
				return []byte("other-profile (enforce)\n"), nil
			default:
				return nil, os.ErrNotExist
			}
		},
	)

	for _, want := range []string{
		"kernel.apparmor_restrict_unprivileged_userns=1",
		"AppArmor bwrap profile not detected",
		"/etc/apparmor.d/bwrap",
		"sudo apparmor_parser -r /etc/apparmor.d/bwrap",
		"sandbox.requested_type=landlock",
	} {
		if !strings.Contains(detail, want) {
			t.Fatalf("expected detail to contain %q, got %q", want, detail)
		}
	}
}

func TestBwrapProbeFailureDetailAcceptsLoadedAppArmorProfile(t *testing.T) {
	detail := bwrapProbeFailureDetail(
		"/usr/bin/bwrap",
		"bwrap: Creating new namespace failed: Permission denied",
		func(string) (os.FileInfo, error) {
			return fakeFileInfo{mode: 0o755}, nil
		},
		func(name string) ([]byte, error) {
			switch name {
			case "/proc/sys/kernel/apparmor_restrict_unprivileged_userns":
				return []byte("1\n"), nil
			case "/sys/kernel/security/apparmor/profiles":
				return []byte("bwrap (unconfined)\n"), nil
			default:
				return nil, os.ErrNotExist
			}
		},
	)

	if !strings.Contains(detail, "kernel.apparmor_restrict_unprivileged_userns=1") {
		t.Fatalf("expected AppArmor sysctl in detail, got %q", detail)
	}
	if strings.Contains(detail, "AppArmor bwrap profile not detected") {
		t.Fatalf("did not expect missing-profile hint when profile is loaded: %q", detail)
	}
}

type fakeFileInfo struct {
	mode os.FileMode
}

func (f fakeFileInfo) Name() string       { return "bwrap" }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }
