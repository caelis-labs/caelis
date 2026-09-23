package cwdpath

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestCompare(t *testing.T) {
	sep := string(filepath.Separator)
	for _, test := range []struct {
		left, right string
		equal       bool
	}{
		{"", " ", true},
		{"", ".", false},
		{"repo" + sep, "repo", true},
		{"repo" + sep + "child" + sep + "..", "repo", true},
		{"repo", "repo-other", false},
		{"Repo", "repo", runtime.GOOS == "windows"},
		{"Äpp", "äpp", runtime.GOOS == "windows"},
		{`repo\child`, "repo/child", runtime.GOOS == "windows"},
	} {
		t.Run(test.left+"_"+test.right, func(t *testing.T) {
			got := Compare(test.left, test.right)
			if (got == 0) != test.equal {
				t.Fatalf("Compare(%q, %q) = %d, want equal=%v", test.left, test.right, got, test.equal)
			}
			if reverse := Compare(test.right, test.left); reverse != -got {
				t.Fatalf("asymmetric comparison: forward=%d, reverse=%d", got, reverse)
			}
		})
	}
}
