package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/internal/updater"
)

type updateProgressRenderer struct {
	writer      io.Writer
	interactive bool
	lineWidth   int
}

func newUpdateProgressRenderer(writer io.Writer) *updateProgressRenderer {
	file, _ := writer.(*os.File)
	return &updateProgressRenderer{
		writer:      writer,
		interactive: isTTY(file),
	}
}

func (r *updateProgressRenderer) Report(event updater.ProgressEvent) {
	if r == nil || r.writer == nil {
		return
	}
	if r.interactive {
		r.renderInteractive(event)
		return
	}
	r.renderPlain(event)
}

func (r *updateProgressRenderer) Fail() {
	if r == nil || r.writer == nil || !r.interactive {
		return
	}
	text := "✗ Update failed"
	padding := max(r.lineWidth-utf8.RuneCountInString(text), 0)
	_, _ = fmt.Fprintf(r.writer, "\r%s%s\n", text, strings.Repeat(" ", padding))
	r.lineWidth = 0
}

func (r *updateProgressRenderer) renderInteractive(event updater.ProgressEvent) {
	text := formatUpdateProgress(event)
	if text == "" {
		return
	}
	width := utf8.RuneCountInString(text)
	padding := max(r.lineWidth-width, 0)
	if event.Stage == updater.ProgressInstalling && !event.Done {
		// The official installer (raw) or npm (foreground/handoff) writes its own
		// output, so finish our status line before handing the terminal back.
		_, _ = fmt.Fprintf(r.writer, "\r%s%s\n", text, strings.Repeat(" ", padding))
		r.lineWidth = 0
		return
	}
	if event.Done {
		_, _ = fmt.Fprintf(r.writer, "\r%s%s\n", text, strings.Repeat(" ", padding))
		r.lineWidth = 0
		return
	}
	_, _ = fmt.Fprintf(r.writer, "\r%s%s", text, strings.Repeat(" ", padding))
	r.lineWidth = width
}

func (r *updateProgressRenderer) renderPlain(event updater.ProgressEvent) {
	text := formatUpdateProgress(event)
	if text != "" {
		_, _ = fmt.Fprintln(r.writer, text)
	}
}

func formatUpdateProgress(event updater.ProgressEvent) string {
	if event.Done {
		switch event.Stage {
		case updater.ProgressChecking:
			return "✓ Checked for updates"
		case updater.ProgressInstalling:
			if event.Deferred {
				return "✓ Prepared update for installation"
			}
			if strings.EqualFold(strings.TrimSpace(event.Detail), updater.MethodNPM) {
				return "✓ npm install completed"
			}
			return "✓ Installed " + firstNonEmptyString(strings.TrimSpace(event.Detail), "update")
		default:
			return ""
		}
	}

	switch event.Stage {
	case updater.ProgressChecking:
		return "Checking for updates…"
	case updater.ProgressInstalling:
		if event.Deferred {
			return "Preparing update for installation…"
		}
		if strings.EqualFold(strings.TrimSpace(event.Detail), updater.MethodNPM) {
			return "Installing update with npm…"
		}
		return "Installing update…"
	default:
		return ""
	}
}
