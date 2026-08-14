package version

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildInfoKeepsDistributionVersionSeparateFromBuildIdentity(t *testing.T) {
	previousVersion, previousCommit, previousDate := Version, Commit, Date
	previousBuildID, previousBuildKind := BuildID, BuildKind
	t.Cleanup(func() {
		Version, Commit, Date = previousVersion, previousCommit, previousDate
		BuildID, BuildKind = previousBuildID, previousBuildKind
	})

	Version = "v1.2.3"
	Commit = "abc123"
	Date = "2026-08-13T00:00:00Z"
	BuildID = "release-build"
	BuildKind = BuildKindRelease

	got := BuildInfo()
	if got.Version != "v1.2.3" || got.BuildID != "release-build" || got.BuildKind != BuildKindRelease {
		t.Fatalf("BuildInfo() = %#v", got)
	}
}

func TestBuildInfoDerivesStampedDevelopmentIdentity(t *testing.T) {
	previousVersion, previousCommit, previousDate := Version, Commit, Date
	previousBuildID, previousBuildKind := BuildID, BuildKind
	t.Cleanup(func() {
		Version, Commit, Date = previousVersion, previousCommit, previousDate
		BuildID, BuildKind = previousBuildID, previousBuildKind
	})

	Version = "dev"
	Commit = "abc123"
	Date = "2026-08-13T00:00:00Z"
	BuildID = ""
	BuildKind = ""

	got := BuildInfo()
	if got.Version != "dev" || got.BuildID != "abc123@2026-08-13T00:00:00Z" || got.BuildKind != BuildKindDev {
		t.Fatalf("BuildInfo() = %#v", got)
	}
}

func TestUnstampedBuildNeverAssumesReleaseFromModuleVersion(t *testing.T) {
	previousVersion, previousBuildKind := Version, BuildKind
	t.Cleanup(func() { Version, BuildKind = previousVersion, previousBuildKind })
	Version = "v1.2.3"
	BuildKind = ""
	if got := BuildInfo().BuildKind; got != BuildKindDev {
		t.Fatalf("unstamped build kind = %q, want dev", got)
	}
}

func TestContentBuildIDChangesWithExecutableBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caelis")
	if err := os.WriteFile(path, []byte("first build"), 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := contentBuildID(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("second build"), 0o700); err != nil {
		t.Fatal(err)
	}
	second, err := contentBuildID(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, "sha256:") || !strings.HasPrefix(second, "sha256:") || first == second {
		t.Fatalf("content BuildIDs = %q, %q", first, second)
	}
}

func TestUnstampedBuildIDNeverUsesSharedSentinel(t *testing.T) {
	if got := derivedBuildID("", ""); got == "" || got == "unstamped" || got == "executable-unavailable" {
		t.Fatalf("derivedBuildID() = %q, want VCS identity or executable digest", got)
	}
}
