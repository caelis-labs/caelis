package stream

import "strings"

// FramesForSnapshot returns isolated incremental frames plus the standard
// terminal close frame for a completed snapshot. Consumers that read snapshots
// directly should use this helper instead of recreating terminal state, final
// text, and exit-code mapping.
func FramesForSnapshot(snapshot Snapshot) []Frame {
	frames := make([]Frame, 0, len(snapshot.Frames)+1)
	hasClosedFrame := false
	for _, frame := range snapshot.Frames {
		cloned := CloneFrame(frame)
		if !snapshot.Running && cloned.Closed {
			cloned = normalizeClosedFrame(snapshot, cloned)
		}
		frames = append(frames, cloned)
		hasClosedFrame = hasClosedFrame || cloned.Closed
	}
	if snapshot.Running || hasClosedFrame {
		return frames
	}

	closeText := ""
	if snapshot.ExitCode == nil {
		closeText = snapshot.FinalText
	}
	frames = append(frames, Frame{
		Ref:             NormalizeRef(snapshot.Ref),
		Text:            closeText,
		State:           closedState(snapshot),
		Cursor:          CloneCursor(snapshot.Cursor),
		TruncatedBefore: snapshot.TruncatedBefore,
		Running:         false,
		Closed:          true,
		ExitCode:        cloneExitCode(snapshot.ExitCode),
		UpdatedAt:       snapshot.UpdatedAt,
	})
	return frames
}

// closedState normalizes a terminal snapshot state to the stream lifecycle
// vocabulary. It preserves an explicit lifecycle state before inferring one
// from a command exit code.
func closedState(snapshot Snapshot) string {
	switch strings.ToLower(strings.TrimSpace(snapshot.State)) {
	case "completed":
		return "completed"
	case "failed":
		return "failed"
	case "interrupted":
		return "interrupted"
	case "cancelled", "canceled":
		return "cancelled"
	}
	if snapshot.ExitCode != nil && *snapshot.ExitCode != 0 {
		if *snapshot.ExitCode < 0 {
			return "cancelled"
		}
		return "failed"
	}
	return "completed"
}

func cloneExitCode(in *int) *int {
	if in == nil {
		return nil
	}
	code := *in
	return &code
}

func normalizeClosedFrame(snapshot Snapshot, frame Frame) Frame {
	if frame.Ref == (Ref{}) {
		frame.Ref = NormalizeRef(snapshot.Ref)
	}
	if frame.Text == "" && snapshot.ExitCode == nil {
		frame.Text = snapshot.FinalText
	}
	if strings.TrimSpace(frame.State) == "" {
		frame.State = closedState(snapshot)
	}
	if frame.ExitCode == nil {
		frame.ExitCode = cloneExitCode(snapshot.ExitCode)
	}
	if frame.UpdatedAt.IsZero() {
		frame.UpdatedAt = snapshot.UpdatedAt
	}
	if frame.TruncatedBefore == 0 {
		frame.TruncatedBefore = snapshot.TruncatedBefore
	}
	frame.Running = false
	frame.Closed = true
	return frame
}
