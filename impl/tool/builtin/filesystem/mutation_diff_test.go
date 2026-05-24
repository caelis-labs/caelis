package filesystem

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func TestBuildMutationDiffHunksSplitsSeparatedChanges(t *testing.T) {
	before := strings.Join([]string{
		"a1",
		"target-a",
		"a3",
		"a4",
		"a5",
		"a6",
		"a7",
		"target-b",
		"a9",
	}, "\n")
	after := strings.ReplaceAll(before, "target-", "changed-")

	hunks, truncated := BuildMutationDiffHunks(before, after, 1, 10, 100)
	if truncated {
		t.Fatal("BuildMutationDiffHunks() truncated unexpectedly")
	}
	if len(hunks) != 2 {
		t.Fatalf("len(hunks) = %d, want 2: %#v", len(hunks), hunks)
	}
	joinedFirst := strings.Join(hunks[0].Lines, "\n")
	joinedSecond := strings.Join(hunks[1].Lines, "\n")
	if !strings.Contains(joinedFirst, "-target-a") || !strings.Contains(joinedFirst, "+changed-a") {
		t.Fatalf("first hunk missing first replacement: %#v", hunks[0])
	}
	if !strings.Contains(joinedSecond, "-target-b") || !strings.Contains(joinedSecond, "+changed-b") {
		t.Fatalf("second hunk missing second replacement: %#v", hunks[1])
	}
	if strings.Contains(joinedFirst, "a6") || strings.Contains(joinedFirst, "target-b") {
		t.Fatalf("first hunk included distant unchanged content: %#v", hunks[0])
	}
}

func TestBuildMutationDiffHunksTruncatesBeforeCollectingAllLines(t *testing.T) {
	before := strings.Repeat("old\n", 600)
	after := strings.Repeat("new\n", 600)

	hunks, truncated := BuildMutationDiffHunks(before, after, 0, 10, 5)
	if !truncated {
		t.Fatal("BuildMutationDiffHunks() truncated = false, want true")
	}
	if len(hunks) != 1 {
		t.Fatalf("len(hunks) = %d, want 1", len(hunks))
	}
	if got := len(hunks[0].Lines); got != 4 {
		t.Fatalf("len(hunk.Lines) = %d, want capped payload of 4 lines", got)
	}
	if hunks[0].OldLines != 600 || hunks[0].NewLines != 600 {
		t.Fatalf("hunk counts = -%d +%d, want full changed range counts", hunks[0].OldLines, hunks[0].NewLines)
	}
}

func TestBuildMutationDiffHunksLargeRewriteShowsBothSidesWhenTruncated(t *testing.T) {
	beforeLines := make([]string, 600)
	afterLines := make([]string, 600)
	for i := range beforeLines {
		beforeLines[i] = "old-" + strconv.Itoa(i+1)
		afterLines[i] = "new-" + strconv.Itoa(i+1)
	}

	hunks, truncated := BuildMutationDiffHunks(strings.Join(beforeLines, "\n"), strings.Join(afterLines, "\n"), 0, 10, 9)
	if !truncated {
		t.Fatal("BuildMutationDiffHunks() truncated = false, want true")
	}
	if len(hunks) != 1 {
		t.Fatalf("len(hunks) = %d, want 1", len(hunks))
	}
	joined := strings.Join(hunks[0].Lines, "\n")
	if !strings.Contains(joined, "-old-1") {
		t.Fatalf("truncated rewrite missing removed side: %#v", hunks[0].Lines)
	}
	if !strings.Contains(joined, "+new-1") {
		t.Fatalf("truncated rewrite missing added side: %#v", hunks[0].Lines)
	}
}

func TestPatchToolAddsStructuredDiffHunksOnlyToMeta(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "repo.go")
	before := strings.Join([]string{
		"package demo",
		"type A = entity.GMLicense",
		"func keep1() {}",
		"func keep2() {}",
		"func keep3() {}",
		"func keep4() {}",
		"func keep5() {}",
		"type B = entity.GMLicense",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	patchTool, err := NewPatch(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewPatch() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{
		"path": "repo.go",
		"edits": []map[string]any{
			{
				"old":                   "entity.GMLicense",
				"new":                   "entity.GmLicense",
				"replace_all":           true,
				"expected_replacements": 2,
			},
		},
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	result, err := patchTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	caelis, _ := result.Metadata["caelis"].(map[string]any)
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	toolMeta, _ := runtimeMeta["tool"].(map[string]any)
	rawHunks, ok := toolMeta["diff_hunks"].([]MutationDiffHunk)
	if !ok {
		t.Fatalf("result.Metadata diff_hunks = %T, want []MutationDiffHunk", toolMeta["diff_hunks"])
	}
	if len(rawHunks) != 2 {
		t.Fatalf("len(diff_hunks) = %d, want 2: %#v", len(rawHunks), rawHunks)
	}
	modelVisible := string(result.Content[0].JSON.Value)
	if strings.Contains(modelVisible, "diff_hunks") {
		t.Fatalf("model-visible result leaked diff_hunks: %s", modelVisible)
	}
}
