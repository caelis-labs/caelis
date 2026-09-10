package collaboration

// Tool projections retain addresses and cursors without exposing internal Task
// identifiers or repeating the recipient and body in send acknowledgements.
// Mail fields consumed by transcript rendering keep their existing names.
type toolThread struct {
	Handle    string `json:"handle"`
	Name      string `json:"name,omitempty"`
	State     string `json:"state"`
	Cursor    uint64 `json:"cursor"`
	Output    string `json:"output,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

func threadToolView(t Thread) toolThread {
	name := t.Name
	if name == t.Handle {
		name = ""
	}
	return toolThread{Handle: t.Handle, Name: name, State: t.State, Cursor: t.Revision}
}

func readToolView(r ThreadRead) toolThread {
	t := threadToolView(r.Thread)
	t.Cursor, t.Output, t.Truncated = r.Cursor, r.Output, r.Truncated
	return t
}

type toolMessage struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	Text    string `json:"message"`
	ReplyTo string `json:"reply_to,omitempty"`
}

type toolSendResult struct {
	ID       string        `json:"id"`
	Status   string        `json:"status"`
	Messages []toolMessage `json:"messages,omitempty"`
}

func messageToolViews(messages []Message) []toolMessage {
	out := make([]toolMessage, 0, len(messages))
	for _, m := range messages {
		out = append(out, toolMessage{ID: m.ID, From: m.From, Text: m.Text, ReplyTo: m.ReplyTo})
	}
	return out
}

func toolResultView(result any) any {
	switch r := result.(type) {
	case []Thread:
		out := make([]toolThread, 0, len(r))
		for _, t := range r {
			out = append(out, threadToolView(t))
		}
		return out
	case ThreadRead:
		return readToolView(r)
	case WaitResult:
		threads := make([]toolThread, 0, len(r.Threads))
		for _, t := range r.Threads {
			threads = append(threads, readToolView(t))
		}
		return struct {
			Reason   string        `json:"reason"`
			Messages []toolMessage `json:"messages,omitempty"`
			Threads  []toolThread  `json:"threads,omitempty"`
		}{r.Reason, messageToolViews(r.Messages), threads}
	default:
		return result
	}
}
