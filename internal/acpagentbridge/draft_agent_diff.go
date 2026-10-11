package acpagentbridge

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/aymanbagabas/go-udiff"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// Stable diffs contain whole-file old/new text. Only this draft wire boundary
// converts that representation; canonical and v1 payloads retain their meaning.
func draftDiffToolContent(raw json.RawMessage) (json.RawMessage, error) {
	var contents []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &contents); err != nil {
		return nil, err
	}
	for _, item := range contents {
		var kind string
		_ = json.Unmarshal(item["type"], &kind)
		if kind != "diff" {
			continue
		}
		if _, structured := item["changes"]; !structured {
			var legacy struct {
				Path    string  `json:"path"`
				OldText *string `json:"oldText"`
				NewText string  `json:"newText"`
			}
			if err := draftJSONConvert(item, &legacy); err != nil {
				return nil, err
			}
			if legacy.Path == "" {
				return nil, fmt.Errorf("ACP draft legacy diff has no affected path")
			}
			operation, old, oldLabel := "modify", "", legacy.Path
			if legacy.OldText == nil {
				operation, oldLabel = "add", "/dev/null"
			} else {
				old = *legacy.OldText
			}
			// Empty new text clears a file. Deletion requires an explicit
			// structured operation; old/new text alone cannot establish it.
			item["changes"] = draftRaw([]session.ProtocolDiffChange{{Operation: operation, Path: legacy.Path}})
			if _, supplied := item["patch"]; !supplied && old != legacy.NewText {
				patch := udiff.Unified(draftDiffPathLabel(oldLabel), draftDiffPathLabel(legacy.Path), old, legacy.NewText)
				if patch != "" {
					header := "diff --git " + draftDiffPathLabel(legacy.Path) + " " + draftDiffPathLabel(legacy.Path) + "\n"
					item["patch"] = draftRaw(session.ProtocolDiffPatch{Format: "git_patch", Text: header + patch})
				}
			}
		}
		delete(item, "path")
		delete(item, "oldText")
		delete(item, "newText")
	}
	return json.Marshal(contents)
}

func draftDiffPathLabel(path string) string {
	if strings.ContainsAny(path, " \t\n\r\"\\") {
		return strconv.Quote(path)
	}
	return path
}
