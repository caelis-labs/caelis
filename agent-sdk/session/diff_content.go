package session

// ProtocolDiffChange describes an affected file independently of the rendered
// patch. Paths and patch text are preserved exactly; this is display evidence
// and does not authorize a filesystem operation.
type ProtocolDiffChange struct {
	Operation string         `json:"operation"`
	Path      string         `json:"path"`
	OldPath   *string        `json:"oldPath,omitempty"`
	FileType  *string        `json:"fileType,omitempty"`
	MimeType  *string        `json:"mimeType,omitempty"`
	Meta      map[string]any `json:"_meta,omitempty"`
}

// ProtocolDiffPatch is a rendered patch, never the complete new file contents.
type ProtocolDiffPatch struct {
	Format string `json:"format"`
	Text   string `json:"text"`
}

// CloneProtocolDiffChanges preserves an explicitly empty changes array.
func CloneProtocolDiffChanges(in *[]ProtocolDiffChange) *[]ProtocolDiffChange {
	if in == nil {
		return nil
	}
	out := make([]ProtocolDiffChange, len(*in))
	for i, item := range *in {
		out[i] = item
		out[i].OldPath = cloneDiffString(item.OldPath)
		out[i].FileType = cloneDiffString(item.FileType)
		out[i].MimeType = cloneDiffString(item.MimeType)
		out[i].Meta = CloneState(item.Meta)
	}
	return &out
}

func CloneProtocolDiffPatch(in *ProtocolDiffPatch) *ProtocolDiffPatch {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneDiffString(in *string) *string {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
