package gatewayapp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestGuardianEvidenceEscapedPagesStayRecoverable(t *testing.T) {
	for _, budget := range []int{1024, 64 * 1024} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			guardianCheckEvidencePages(t, budget)
		})
	}
}

func guardianCheckEvidencePages(t *testing.T, budget int) {
	t.Helper()
	q := &guardianQueries{pageBytes: budget}
	defer q.close()
	original := strings.Repeat("中文\"\\\n<>&\x01", 3000)
	ref, err := q.evidenceStore().save([]byte(original))
	if err != nil {
		t.Fatal(err)
	}
	var rebuilt strings.Builder
	for offset := 0; ; {
		args, _ := json.Marshal(map[string]any{"ref": ref, "offset": offset, "max_bytes": 40000})
		result, err := (guardianQueryTool{q, "ReadEvidence"}).Call(t.Context(), tool.Call{Input: args})
		if err != nil || result.IsError {
			t.Fatalf("page failed: %+v %v", result, err)
		}
		if tool.ResultNeedsTruncation(result, tool.DefaultTruncationPolicy()) {
			t.Fatal("Runtime would truncate the page after advancing its cursor")
		}
		var page struct {
			Ref     string `json:"ref"`
			Content string `json:"content"`
			Next    int    `json:"next_offset"`
			More    bool   `json:"has_more"`
		}
		if err := json.Unmarshal(result.Content[0].JSON.Value, &page); err != nil {
			t.Fatal(err)
		}
		if page.Ref != ref || !utf8.ValidString(page.Content) {
			t.Fatalf("page was replaced or invalid: %+v", page)
		}
		rebuilt.WriteString(page.Content)
		if !page.More {
			break
		}
		if page.Next <= offset {
			t.Fatal("page made no progress")
		}
		offset = page.Next
	}
	if rebuilt.String() != original || q.truncated != 0 {
		t.Fatal("escaped pagination lost bytes or created nested references")
	}
}
