package toolpresentation

import "context"

// DiffHunk is a unified diff hunk for a filesystem mutation.
type DiffHunk struct {
	Header   string   `json:"header"`
	OldStart int      `json:"old_start"`
	OldLines int      `json:"old_lines"`
	NewStart int      `json:"new_start"`
	NewLines int      `json:"new_lines"`
	Lines    []string `json:"lines"`
}

// FileChange describes the committed mutation for the display.
type FileChange struct {
	Created, PreviousEmpty   bool
	AddedLines, RemovedLines int
	Revision, Hunk           string
	Hunks                    []DiffHunk
	Truncated                bool
}

func (change FileChange) fields() map[string]any {
	fields := map[string]any{"created": change.Created, "previous_empty": change.PreviousEmpty, "added_lines": change.AddedLines, "removed_lines": change.RemovedLines, "revision": change.Revision}
	if change.Hunk != "" {
		fields["hunk"] = change.Hunk
	}
	if len(change.Hunks) > 0 {
		fields["diff_hunks"] = change.Hunks
		if change.Hunk == "" {
			fields["hunk"] = change.Hunks[0].Header
		}
	}
	if change.Truncated {
		fields["diff_truncated"] = true
	}
	return fields
}

// Write records the committed file and its size.
func Write(ctx context.Context, change FileChange, bytesWritten, lines int) {
	fields := change.fields()
	fields["bytes_written"] = bytesWritten
	fields["line_count"] = lines
	publish(ctx, fields)
}

// Patch records the committed patch.
func Patch(ctx context.Context, change FileChange) { publish(ctx, change.fields()) }
