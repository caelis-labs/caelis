package runnerruntime

import (
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/cmdsession"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/textstream"
)

// UTF8OutputForwarder converts raw async pipe chunks into valid UTF-8 text
// without splitting a rune across callbacks. Stdout and stderr keep independent
// decoder state, while the raw cmdsession ring remains byte-exact.
func UTF8OutputForwarder(fn func(OutputChunk)) func(cmdsession.AsyncOutputChunk) {
	if fn == nil {
		return nil
	}
	var mu sync.Mutex
	decoders := map[string]*textstream.UTF8Decoder{}
	return func(chunk cmdsession.AsyncOutputChunk) {
		stream := strings.TrimSpace(chunk.Stream)
		mu.Lock()
		defer mu.Unlock()
		decoder := decoders[stream]
		if decoder == nil {
			decoder = &textstream.UTF8Decoder{}
			decoders[stream] = decoder
		}
		text := decoder.Decode(chunk.Data)
		if chunk.Final {
			text += decoder.Flush()
			delete(decoders, stream)
		}
		if text != "" {
			cursor := max(chunk.Cursor-int64(decoder.PendingBytes()), 0)
			if chunk.Final {
				cursor = chunk.Cursor
			}
			fn(OutputChunk{Stream: stream, Text: text, Cursor: cursor})
		}
	}
}
