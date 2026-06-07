package model

// Clone returns a deep copy of the message.
func (m Message) Clone() Message {
	cp := m
	if m.Content != nil {
		cp.Content = make([]Part, len(m.Content))
		for i, p := range m.Content {
			cp.Content[i] = p.Clone()
		}
	}
	return cp
}

// Clone returns a deep copy of the part.
func (p Part) Clone() Part {
	cp := p
	if p.InlineData != nil {
		id := *p.InlineData
		if id.Data != nil {
			id.Data = make([]byte, len(id.Data))
			copy(id.Data, p.InlineData.Data)
		}
		cp.InlineData = &id
	}
	if p.FileRef != nil {
		fr := *p.FileRef
		cp.FileRef = &fr
	}
	if p.ToolUse != nil {
		tu := *p.ToolUse
		if tu.Args != nil {
			tu.Args = make(map[string]any, len(tu.Args))
			for k, v := range p.ToolUse.Args {
				tu.Args[k] = v
			}
		}
		cp.ToolUse = &tu
	}
	if p.ToolResult != nil {
		tr := *p.ToolResult
		cp.ToolResult = &tr
	}
	return cp
}

// Normalize removes empty parts and collapses adjacent text parts.
// Empty parts act as separators between text segments.
func (m Message) Normalize() Message {
	if len(m.Content) == 0 {
		return m
	}
	out := make([]Part, 0, len(m.Content))
	lastWasEmpty := false
	for _, p := range m.Content {
		isEmpty := p.Text == "" && p.InlineData == nil && p.FileRef == nil &&
			p.ToolUse == nil && p.ToolResult == nil
		if isEmpty {
			lastWasEmpty = true
			continue
		}
		isTextOnly := p.Text != "" && p.InlineData == nil && p.FileRef == nil &&
			p.ToolUse == nil && p.ToolResult == nil
		if isTextOnly && len(out) > 0 && !lastWasEmpty {
			prev := out[len(out)-1]
			prevIsTextOnly := prev.Text != "" && prev.InlineData == nil && prev.FileRef == nil &&
				prev.ToolUse == nil && prev.ToolResult == nil
			if prevIsTextOnly {
				out[len(out)-1].Text += p.Text
				lastWasEmpty = false
				continue
			}
		}
		out = append(out, p.Clone())
		lastWasEmpty = false
	}
	return Message{Role: m.Role, Content: out}
}

// TextContent returns the concatenated text from all text parts.
func (m Message) TextContent() string {
	var buf []byte
	for _, p := range m.Content {
		buf = append(buf, p.Text...)
	}
	return string(buf)
}
