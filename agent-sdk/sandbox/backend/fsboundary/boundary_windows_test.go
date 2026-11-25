package fsboundary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsContainmentUsesFilesystemCaseSemantics(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "MixedCase")
	child := filepath.Join(root, "child")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	alternate := filepath.Join(parent, "mixedcase")
	if !PathIsUnder(filepath.ToSlash(child), root) {
		t.Fatal("alternate path separators changed containment")
	}
	_, err := os.Stat(alternate)
	if os.IsNotExist(err) {
		if err := os.Mkdir(alternate, 0o700); err != nil {
			t.Fatal(err)
		}
		if PathIsUnder(filepath.Join(alternate, "child"), root) {
			t.Fatal("a distinct directory differing in case expanded the root")
		}
	} else if err != nil {
		t.Fatal(err)
	} else {
		if !PathIsUnder(filepath.Join(alternate, "child"), root) {
			t.Fatal("a case variant of the same directory was excluded")
		}
		if !IsWithinRoots(strings.ToUpper(child), []string{root}, nil) {
			t.Fatal("an existing case variant of the child was excluded")
		}
	}
	if PathIsUnder(filepath.Join(parent, "MixedCaseSibling", "child"), root) {
		t.Fatal("a sibling sharing the root prefix was included")
	}
}
