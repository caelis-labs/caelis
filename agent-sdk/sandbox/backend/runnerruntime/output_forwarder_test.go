package runnerruntime

import (
	"bytes"
	"testing"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/cmdsession"
)

func TestUTF8OutputForwarderKeepsRuneSplitAtReadBoundary(t *testing.T) {
	raw := append(bytes.Repeat([]byte("x"), 8191), []byte("中文\n")...)
	var got string
	forward := UTF8OutputForwarder(func(chunk OutputChunk) {
		if chunk.Stream != "stdout" {
			t.Fatalf("stream = %q, want stdout", chunk.Stream)
		}
		if !utf8.ValidString(chunk.Text) {
			t.Fatalf("callback emitted invalid UTF-8: %q", chunk.Text)
		}
		got += chunk.Text
	})
	forward(cmdsession.AsyncOutputChunk{Stream: "stdout", Data: raw[:8192]})
	forward(cmdsession.AsyncOutputChunk{Stream: "stdout", Data: raw[8192:]})
	forward(cmdsession.AsyncOutputChunk{Stream: "stdout", Final: true})

	if got != string(raw) {
		t.Fatalf("forwarded output differs at split rune: got suffix %q, want %q", got[len(got)-10:], string(raw[len(raw)-10:]))
	}
}

func TestUTF8OutputForwarderKeepsStreamDecoderStateSeparate(t *testing.T) {
	raw := []byte("中")
	got := map[string]string{}
	forward := UTF8OutputForwarder(func(chunk OutputChunk) { got[chunk.Stream] += chunk.Text })
	forward(cmdsession.AsyncOutputChunk{Stream: "stdout", Data: raw[:1]})
	forward(cmdsession.AsyncOutputChunk{Stream: "stderr", Data: raw[:2]})
	forward(cmdsession.AsyncOutputChunk{Stream: "stdout", Data: raw[1:]})
	forward(cmdsession.AsyncOutputChunk{Stream: "stderr", Data: raw[2:]})
	forward(cmdsession.AsyncOutputChunk{Stream: "stdout", Final: true})
	forward(cmdsession.AsyncOutputChunk{Stream: "stderr", Final: true})

	if got["stdout"] != "中" || got["stderr"] != "中" {
		t.Fatalf("forwarded streams = %#v, want independent complete runes", got)
	}
}
