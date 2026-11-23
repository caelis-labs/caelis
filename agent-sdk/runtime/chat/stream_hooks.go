package chat

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

type textTransform struct {
	registration hook.Stream
	value        hook.StreamTransformer
}
type textStream struct {
	evidence                                   hook.OutputContext
	transforms                                 []textTransform
	raw, accepted, pendingInput, pendingOutput map[int]string
	last                                       int
	started                                    bool
	finished                                   bool
}

func newTextStream(ctx context.Context, e hook.OutputContext) (*textStream, error) {
	s := &textStream{evidence: e, raw: map[int]string{}, accepted: map[int]string{}, pendingInput: map[int]string{}, pendingOutput: map[int]string{}}
	for _, h := range hookrun.FromContext(ctx).Hooks.Stream {
		value, err := hookrun.Transform(ctx, "stream_factory", h.Name, h.Timeout, func(ctx context.Context) (hook.StreamTransformer, error) { return h.Apply(ctx, e) }, func(v hook.StreamTransformer) hook.StreamTransformer { return v })
		if err != nil {
			return nil, err
		}
		if value == nil {
			return nil, &hook.Error{Phase: "stream_factory", Name: h.Name, Cause: fmt.Errorf("nil transformer")}
		}
		s.transforms = append(s.transforms, textTransform{registration: h, value: value})
	}
	return s, nil
}

func utf8Prefix(text string) (string, string, error) {
	for i := 0; i < len(text); {
		if !utf8.FullRuneInString(text[i:]) {
			return text[:i], text[i:], nil
		}
		r, n := utf8.DecodeRuneInString(text[i:])
		if r == utf8.RuneError && n == 1 {
			return "", "", fmt.Errorf("invalid UTF-8 stream")
		}
		i += n
	}
	return text, "", nil
}

func (s *textStream) transform(ctx context.Context, start int, text string) (string, error) {
	for _, t := range s.transforms[start:] {
		input := text
		var err error
		text, err = hookrun.Transform(ctx, "stream", t.registration.Name, t.registration.Timeout, func(ctx context.Context) (string, error) { return t.value.Transform(ctx, input) }, func(s string) string { return s })
		if err != nil {
			return "", err
		}
	}
	return text, nil
}

func (s *textStream) push(ctx context.Context, index int, text string) (string, error) {
	if index < 0 || s.started && text != "" && index < s.last {
		return "", fmt.Errorf("append-only text parts arrived out of order")
	}
	if text != "" {
		s.started = true
	}
	s.last = index
	s.raw[index] += text
	valid, pending, err := utf8Prefix(s.pendingInput[index] + text)
	if err != nil {
		return "", err
	}
	s.pendingInput[index] = pending
	transformed, err := s.transform(ctx, 0, valid)
	if err != nil {
		return "", err
	}
	return s.accept(index, transformed)
}

func (s *textStream) accept(index int, text string) (string, error) {
	valid, pending, err := utf8Prefix(s.pendingOutput[index] + text)
	if err != nil {
		return "", err
	}
	s.pendingOutput[index] = pending
	s.accepted[index] += valid
	return valid, nil
}

func (s *textStream) finish(ctx context.Context, message model.Message, incremental bool, publish func(int, string) error) (model.Message, error) {
	message = model.CloneMessage(message)
	// Some providers number their sole text stream from zero and place
	// reasoning before it in the final message. Keep the published stream
	// index for suffixes and transformer state when that mapping is unique.
	soleText := -1
	if _, zero := s.raw[0]; zero && len(s.raw) == 1 {
		for i, part := range message.Parts {
			if part.Text != nil {
				if soleText >= 0 {
					soleText = -1
					break
				}
				soleText = i
			}
		}
	}
	streamIndex := func(i int) int {
		if i == soleText {
			return 0
		}
		return i
	}
	for index, text := range s.raw {
		if index == 0 && soleText >= 0 {
			continue
		}
		if text != "" && (index < 0 || index >= len(message.Parts) || message.Parts[index].Text == nil) {
			return model.Message{}, fmt.Errorf("stream text has no matching final part")
		}
	}
	for index, p := range message.Parts {
		if p.Text == nil {
			continue
		}
		index = streamIndex(index)
		text := p.Text.Text
		if incremental {
			// Published fragments are authoritative. A final provider snapshot can
			// fill an unstreamed suffix, but cannot retract already accepted text.
			if strings.HasPrefix(text, s.raw[index]) {
				text = text[len(s.raw[index]):]
			} else {
				text = ""
			}
		}
		if incremental && text == "" {
			continue
		}
		out, err := s.push(ctx, index, text)
		if err != nil {
			return model.Message{}, err
		}
		if incremental && out != "" {
			if err := publish(index, out); err != nil {
				return model.Message{}, err
			}
		}
	}
	hasText := false
	for _, p := range message.Parts {
		hasText = hasText || p.Text != nil
	}
	if !hasText && len(s.transforms) > 0 {
		s.last = len(message.Parts)
		message.Parts = append(message.Parts, model.NewTextPart(""))
	}
	for i, t := range s.transforms {
		out, err := hookrun.Transform(ctx, "stream_finish", t.registration.Name, t.registration.Timeout, t.value.Finish, func(s string) string { return s })
		if err != nil {
			return model.Message{}, err
		}
		out, err = s.transform(ctx, i+1, out)
		if err != nil {
			return model.Message{}, err
		}
		out, err = s.accept(s.last, out)
		if err != nil {
			return model.Message{}, err
		}
		if incremental && out != "" {
			if err := publish(s.last, out); err != nil {
				return model.Message{}, err
			}
		}
	}
	for _, pending := range s.pendingInput {
		if pending != "" {
			return model.Message{}, fmt.Errorf("incomplete UTF-8 input at stream end")
		}
	}
	for _, pending := range s.pendingOutput {
		if pending != "" {
			return model.Message{}, fmt.Errorf("incomplete UTF-8 output at stream end")
		}
	}
	for index := range message.Parts {
		if message.Parts[index].Text != nil {
			message.Parts[index].Text.Text = s.accepted[streamIndex(index)]
		}
	}
	s.finished = true
	return message, nil
}

// finishOutput is the final text boundary for every candidate source. Buffered
// output reaches it after semantic replacements; Incremental output is already
// immutable and must not run its stateful transformer a second time.
func (a *Agent) finishOutput(ctx context.Context, message model.Message, messages []model.Message) (model.Message, error) {
	if a.outputStream == nil {
		return model.Message{}, fmt.Errorf("candidate has no output transformer")
	}
	if a.outputStream.finished {
		return message, nil
	}
	return a.outputStream.finish(ctx, normalizeAssistantCitations(message, messages), false, nil)
}
