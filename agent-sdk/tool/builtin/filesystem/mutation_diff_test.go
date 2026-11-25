package filesystem

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/internal/toolpresentation"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestMutationDiffHunksSplitsSeparatedChanges(t *testing.T) {
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

	hunks, truncated := compareLines(before, after).hunks(1, 10, 100)
	if truncated {
		t.Fatal("hunks() truncated unexpectedly")
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

func TestMutationDiffHunksTruncatesBeforeCollectingAllLines(t *testing.T) {
	before := strings.Repeat("old\n", 600)
	after := strings.Repeat("new\n", 600)

	hunks, truncated := compareLines(before, after).hunks(0, 10, 5)
	if !truncated {
		t.Fatal("hunks() truncated = false, want true")
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

func TestMutationDiffHunksLargeRewriteShowsBothSidesWhenTruncated(t *testing.T) {
	beforeLines := make([]string, 600)
	afterLines := make([]string, 600)
	for i := range beforeLines {
		beforeLines[i] = "old-" + strconv.Itoa(i+1)
		afterLines[i] = "new-" + strconv.Itoa(i+1)
	}

	hunks, truncated := compareLines(strings.Join(beforeLines, "\n"), strings.Join(afterLines, "\n")).hunks(0, 10, 9)
	if !truncated {
		t.Fatal("hunks() truncated = false, want true")
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
				"old":         "entity.GMLicense",
				"new":         "entity.GmLicense",
				"replace_all": true,
			},
		},
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var toolMeta map[string]any
	ctx := toolpresentation.With(t.Context(), func(fields map[string]any) { toolMeta = fields })
	result, err := patchTool.Call(ctx, tool.Call{Input: input})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	rawHunks, ok := toolMeta["diff_hunks"].([]toolpresentation.DiffHunk)
	if !ok {
		t.Fatalf("result.Metadata diff_hunks = %T, want []toolpresentation.DiffHunk", toolMeta["diff_hunks"])
	}
	if toolMeta["added_lines"] != 2 || toolMeta["removed_lines"] != 2 {
		t.Fatalf("display counts = +%v -%v", toolMeta["added_lines"], toolMeta["removed_lines"])
	}
	if len(rawHunks) != 2 {
		t.Fatalf("len(diff_hunks) = %d, want 2: %#v", len(rawHunks), rawHunks)
	}
	structured, _ := result.Structured()
	modelVisible := string(structured)
	if strings.Contains(modelVisible, "diff_hunks") {
		t.Fatalf("model-visible result leaked diff_hunks: %s", modelVisible)
	}
}

func TestSparseLargeMutationDiffRetainsUnchangedMiddle(t *testing.T) {
	oldLines := make([]string, 1600)
	for i := range oldLines {
		oldLines[i] = "// shared context"
	}
	oldLines[3], oldLines[1500] = "old first", "old last"
	newLines := append([]string(nil), oldLines...)
	newLines[3], newLines[1500] = "new first", "new last"
	before, after := strings.Join(oldLines, "\n"), strings.Join(newLines, "\n")
	change := (fileMutationPlan{before: before, after: after}).describeChange()
	if change.AddedLines != 2 || change.RemovedLines != 2 {
		t.Fatalf("sparse diff counts = +%d -%d", change.AddedLines, change.RemovedLines)
	}
	hunks, truncated := change.Hunks, change.Truncated
	if truncated || len(hunks) != 2 {
		t.Fatalf("sparse diff produced %d hunks, truncated=%v", len(hunks), truncated)
	}
	if hunks[0].OldStart != 2 || hunks[1].OldStart != 1499 {
		t.Fatalf("hunk positions = %+v", hunks)
	}
	for _, hunk := range hunks {
		for _, line := range hunk.Lines {
			if line == "-// shared context" || line == "+// shared context" {
				t.Fatal("unchanged middle rendered as replacement")
			}
		}
	}
}
