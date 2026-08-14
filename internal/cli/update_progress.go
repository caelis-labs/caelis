package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/internal/updater"
)

const updateProgressBarWidth = 24

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
	text := formatUpdateProgress(event, true)
	if text == "" {
		return
	}
	width := utf8.RuneCountInString(text)
	padding := max(r.lineWidth-width, 0)
	if event.Stage == updater.ProgressInstalling &&
		strings.EqualFold(strings.TrimSpace(event.Detail), updater.MethodNPM) &&
		!event.Done {
		// npm writes its own foreground output, so finish our status line before
		// handing the terminal to the child process.
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
	if event.Current > 0 && !event.Done {
		return
	}
	text := formatUpdateProgress(event, false)
	if text != "" {
		_, _ = fmt.Fprintln(r.writer, text)
	}
}

func formatUpdateProgress(event updater.ProgressEvent, interactive bool) string {
	if event.Done {
		switch event.Stage {
		case updater.ProgressChecking:
			return "✓ Checked for updates"
		case updater.ProgressDownloading:
			if event.Total > 0 {
				return "✓ Downloaded " + formatUpdateBytes(event.Total)
			}
			if event.Current > 0 {
				return "✓ Downloaded " + formatUpdateBytes(event.Current)
			}
			return "✓ Downloaded update"
		case updater.ProgressVerifying:
			return "✓ Checksum verified"
		case updater.ProgressExtracting:
			return "✓ Extracted " + firstNonEmptyString(strings.TrimSpace(event.Detail), "update")
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
	case updater.ProgressDownloading:
		if event.Current <= 0 {
			return "Downloading update…"
		}
		if event.Total <= 0 {
			return "Downloading update… " + formatUpdateBytes(event.Current)
		}
		percent := min(float64(event.Current)/float64(event.Total), 1)
		if interactive {
			return fmt.Sprintf(
				"Downloading  %s  %s / %s  %d%%",
				updateProgressBar(percent),
				formatUpdateBytes(event.Current),
				formatUpdateBytes(event.Total),
				int(percent*100),
			)
		}
		return "Downloading update…"
	case updater.ProgressVerifying:
		return "Verifying checksum…"
	case updater.ProgressExtracting:
		return "Extracting update…"
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

func updateProgressBar(percent float64) string {
	percent = min(max(percent, 0), 1)
	filled := int(percent * updateProgressBarWidth)
	return strings.Repeat("█", filled) + strings.Repeat("░", updateProgressBarWidth-filled)
}

func formatUpdateBytes(value int64) string {
	switch {
	case value >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(value)/(1<<30))
	case value >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(value)/(1<<20))
	case value >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(value)/(1<<10))
	default:
		return fmt.Sprintf("%d B", value)
	}
}
