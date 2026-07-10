package main

import (
	"strings"
	"testing"
)

func TestCompareSnapshotsRequiresExactRemovalWaiver(t *testing.T) {
	t.Parallel()

	baseline := []byte(`# header

package example/sdk
  func Keep(value string) error
  type Changed struct {
      Value string
  }
`)
	current := []byte(`# header

package example/sdk
  func Added() bool
  func Keep(value string) error
  type Changed struct {
      Value string
      Count int
  }
`)
	base, err := parseSnapshot(baseline)
	if err != nil {
		t.Fatal(err)
	}
	next, err := parseSnapshot(current)
	if err != nil {
		t.Fatal(err)
	}
	removed := removedDeclarations(base, next)
	if len(removed) != 1 || !strings.HasPrefix(removed[0].Declaration, "type Changed struct") {
		t.Fatalf("removed = %#v", removed)
	}
	if err := validateCompatibility(removed, nil); err == nil || !strings.Contains(err.Error(), removed[0].SHA256) {
		t.Fatalf("validateCompatibility() error = %v, want missing waiver digest", err)
	}
	waivers := []removalWaiver{{
		Package: "example/sdk", SHA256: removed[0].SHA256,
		Symbol: "type Changed", Reason: "pre-v1 field contract was intentionally replaced",
	}}
	if err := validateCompatibility(removed, waivers); err != nil {
		t.Fatalf("validateCompatibility(waived) error = %v", err)
	}
}

func TestCompareSnapshotsRejectsStaleOrAmbiguousWaivers(t *testing.T) {
	t.Parallel()

	removed := []declaration{{Package: "example/sdk", Declaration: "func Removed()", SHA256: "exact"}}
	tests := []struct {
		name    string
		waivers []removalWaiver
	}{
		{name: "stale", waivers: []removalWaiver{{Package: "example/sdk", SHA256: "stale", Symbol: "Removed", Reason: "old"}}},
		{name: "empty reason", waivers: []removalWaiver{{Package: "example/sdk", SHA256: "exact", Symbol: "Removed"}}},
		{name: "duplicate", waivers: []removalWaiver{
			{Package: "example/sdk", SHA256: "exact", Symbol: "Removed", Reason: "one"},
			{Package: "example/sdk", SHA256: "exact", Symbol: "Removed", Reason: "two"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := validateCompatibility(removed, tt.waivers); err == nil {
				t.Fatal("validateCompatibility() error = nil")
			}
		})
	}
}

func TestParseSnapshotRejectsDeclarationWithoutPackage(t *testing.T) {
	t.Parallel()

	if _, err := parseSnapshot([]byte("  func Invalid()\n")); err == nil {
		t.Fatal("parseSnapshot() error = nil")
	}
}
