package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/OnslaughtSnail/caelis/internal/displaypolicy"
	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/surfaces/plainactivity"
)

// plainactivityStreamer renders streaming events as append-only plain text to
// a terminal. It never uses ANSI cursor movement, so scrolling and scrollback
// behave naturally. Each new piece of content is printed exactly once.
type plainactivityStreamer struct {
	out              io.Writer
	buf              *bufio.Writer      // buffered wrapper around out
	lastKind         plainactivity.Kind // current section kind
	lastWasNL        bool               // last print ended with newline
	hadContent       bool               // any content printed yet
	textStarted      bool               // whether any text section has started (prefix printed)
	seenCalls        map[string]bool    // tool call IDs already printed
	reasoningEmitted bool               // reasoning was printed via streaming deltas
}

func newPlainactivityStreamer(w io.Writer) *plainactivityStreamer {
	return &plainactivityStreamer{
		out: w,
		buf: bufio.NewWriter(w),
	}
}

func (s *plainactivityStreamer) OnEvent(env gateway.EventEnvelope) {
	if env.Err != nil {
		s.emitLine(fmt.Sprintf("Error: %v", env.Err))
		return
	}

	switch env.Event.Kind {
	case gateway.EventKindAssistantMessage:
		if env.Event.Narrative == nil {
			return
		}
		// For Final events, emit reasoning if it hasn't been seen via streaming
		// deltas yet (some providers only include reasoning in the final response).
		if env.Event.Narrative.Final {
			if env.Event.Narrative.ReasoningText != "" && !s.reasoningEmitted {
				s.handleTextDelta(plainactivity.Reasoning, env.Event.Narrative.ReasoningText)
			}
			if env.Event.Narrative.Text != "" && !s.textStarted {
				s.handleTextDelta(plainactivity.Assistant, env.Event.Narrative.Text)
			}
			return
		}
		if env.Event.Narrative.ReasoningText != "" {
			s.reasoningEmitted = true
			s.handleTextDelta(plainactivity.Reasoning, env.Event.Narrative.ReasoningText)
		}
		if env.Event.Narrative.Text != "" {
			s.handleTextDelta(plainactivity.Assistant, env.Event.Narrative.Text)
		}

	case gateway.EventKindToolCall:
		tc := env.Event.ToolCall
		if tc == nil || tc.CallID == "" || tc.ToolName == "" {
			return
		}
		if s.seenCalls == nil {
			s.seenCalls = make(map[string]bool)
		}
		if s.seenCalls[tc.CallID] {
			return // already printed this tool call
		}
		s.seenCalls[tc.CallID] = true
		title := displaypolicy.SummarizeToolCallTitle(tc.ToolName, tc.RawInput)
		if title != "" {
			s.startNewSection()
			s.emitLine("• " + title)
			s.lastKind = plainactivity.ToolCall
			s.textStarted = false // reset so next text section gets its prefix
		}

	case gateway.EventKindToolResult:
		tr := env.Event.ToolResult
		if tr == nil || tr.CallID == "" {
			return
		}
		// Tool completion/failure: emit a status line when the tool finishes.
		if tr.Error {
			s.startNewSection()
			s.emitLine("✗ " + toolStatusLine(tr.ToolName, string(tr.Status)))
		} else if tr.Status != "" && !strings.EqualFold(strings.TrimSpace(string(tr.Status)), "running") {
			s.startNewSection()
			s.emitLine("✓ " + toolStatusLine(tr.ToolName, string(tr.Status)))
		}

	case gateway.EventKindLifecycle:
		lc := env.Event.Lifecycle
		if lc == nil {
			return
		}
		state := strings.ToLower(strings.TrimSpace(string(lc.Status)))
		switch state {
		case "completed", "failed", "interrupted", "cancelled", "canceled":
			s.startNewSection()
			s.emitLine("— " + state)
		}

	case gateway.EventKindPlanUpdate:
		if env.Event.Plan != nil && len(env.Event.Plan.Entries) > 0 {
			last := env.Event.Plan.Entries[len(env.Event.Plan.Entries)-1]
			if content := strings.TrimSpace(last.Content); content != "" {
				s.startNewSection()
				s.emitLine("☐ " + content)
				s.lastKind = plainactivity.ToolCall
				s.textStarted = false
			}
		}
	}
}

func toolStatusLine(toolName string, status string) string {
	if name := strings.TrimSpace(toolName); name != "" {
		return name + " " + strings.TrimSpace(status)
	}
	return strings.TrimSpace(status)
}

// handleTextDelta prints one reasoning or assistant text delta. When the kind
// changes (or on first text event) a new section prefix is emitted.
func (s *plainactivityStreamer) handleTextDelta(kind plainactivity.Kind, text string) {
	if !s.textStarted || kind != s.lastKind {
		s.startNewSection()
		s.emitRaw(kind.Prefix())
		s.lastKind = kind
		s.textStarted = true
	}
	s.emitRaw(text)
}

// startNewSection ensures the previous section is terminated with a newline.
func (s *plainactivityStreamer) startNewSection() {
	if s.hadContent && !s.lastWasNL {
		fmt.Fprintln(s.buf)
		s.lastWasNL = true
	}
}

// emitLine prints text followed by a newline.
func (s *plainactivityStreamer) emitLine(text string) {
	fmt.Fprintln(s.buf, text)
	s.lastWasNL = true
	s.hadContent = true
}

// emitRaw prints text as-is without appending a newline.
func (s *plainactivityStreamer) emitRaw(text string) {
	if text == "" {
		return
	}
	fmt.Fprint(s.buf, text)
	s.hadContent = true
	s.lastWasNL = text[len(text)-1] == '\n'
}

// finish ensures the output ends with a newline, then flushes.
func (s *plainactivityStreamer) finish() {
	if s.hadContent && !s.lastWasNL {
		fmt.Fprintln(s.buf)
	}
	_ = s.buf.Flush()
	if f, ok := s.out.(*os.File); ok {
		_ = f.Sync()
	}
}
