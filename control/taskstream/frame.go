package taskstream

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

// Frame is one Control-owned normalized Task delivery record. Spool offsets,
// not payload fields, define ordering and resume.
type Frame struct {
	TerminalID string         `json:"terminal_id,omitempty"`
	Text       string         `json:"text,omitempty"`
	State      string         `json:"state,omitempty"`
	ActivityID string         `json:"activity_id,omitempty"`
	Running    bool           `json:"running,omitempty"`
	Closed     bool           `json:"closed,omitempty"`
	ExitCode   *int           `json:"exit_code,omitempty"`
	Event      *session.Event `json:"event,omitempty"`
	// ScriptCompletion and ScriptSource describe the producer of a final Code Mode frame.
	ScriptCompletion string           `json:"script_completion,omitempty"`
	ScriptSource     *codemode.Source `json:"script_source,omitempty"`
	UpdatedAt        time.Time        `json:"updated_at,omitempty"`
}

func cloneFrame(in Frame) Frame {
	out := in
	out.TerminalID = strings.TrimSpace(in.TerminalID)
	out.ActivityID = strings.TrimSpace(in.ActivityID)
	if in.ExitCode != nil {
		code := *in.ExitCode
		out.ExitCode = &code
	}
	if in.ScriptSource != nil {
		source := *in.ScriptSource
		out.ScriptSource = &source
	}
	out.Event = session.CloneEvent(in.Event)
	return out
}

// Historical script Tasks carried these observations inside the stored result.
func (f *Frame) setScriptObservation(entry *task.Entry, completion string) {
	if entry.Kind != task.KindCodeMode || !f.Closed {
		return
	}
	f.ScriptCompletion = firstString(completion, mapString(entry.Metadata, codemode.MetadataScriptCompletion))
	if source, ok := entry.Metadata["code_mode_source"].(map[string]any); ok {
		script, _ := source["code"].(string)
		f.ScriptSource = &codemode.Source{Code: script, Path: mapString(source, "path"), SHA256: mapString(source, "sha256")}
	}
	if f.ScriptCompletion != "" {
		return
	}
	var old struct {
		Metadata struct {
			Completion string           `json:"code_mode_script_completion"`
			Source     *codemode.Source `json:"code_mode_source"`
		} `json:"metadata"`
	}
	if json.Unmarshal([]byte(f.Text), &old) == nil {
		f.ScriptCompletion = old.Metadata.Completion
		if f.ScriptSource == nil {
			f.ScriptSource = old.Metadata.Source
		}
	}
}
