package runtime

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

type codeModeReplyReceipt struct {
	id  string
	seq uint64
	// Standalone Host fixtures can project without a Session receipt.
	inMemory []codemode.Part
}

// A generation owns one key. Tokens name durable receipts without keeping an
// index of objects issued to the VM. The VM still checks actual object identity.
func newCodeModeOrigins() cipher.AEAD {
	var key [32]byte
	_, _ = rand.Read(key[:])
	block, err := aes.NewCipher(key[:])
	if err != nil {
		panic(err)
	}
	sealer, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		panic(err)
	}
	return sealer
}

const codeModeOriginPrefix = "receipt."
const maxCodeModeReceiptID = 256
const codeModeOriginPositionBytes = 12
const codeModeOriginOverhead = 12 + 16 // GCM nonce and tag

func codeModeOriginBytes(id string) int {
	return len(codeModeOriginPrefix) + base64.RawURLEncoding.EncodedLen(codeModeOriginPositionBytes+len(id)+codeModeOriginOverhead)
}

// The ordinal is one-based for a block. Zero selects the entire receipt.
func (h *codeModeHost) sealCodeModeOrigin(receipt codeModeReplyReceipt, ordinal int) string {
	if h.origins == nil {
		h.origins = newCodeModeOrigins()
	}
	var position [codeModeOriginPositionBytes]byte
	binary.BigEndian.PutUint64(position[:8], receipt.seq)
	binary.BigEndian.PutUint32(position[8:], uint32(ordinal))
	raw := append(position[:], receipt.id...)
	return codeModeOriginPrefix + base64.RawURLEncoding.EncodeToString(h.origins.Seal(nil, nil, raw, nil))
}

func (h *codeModeHost) openCodeModeOrigin(token string) (codeModeReplyReceipt, int, bool) {
	var receipt codeModeReplyReceipt
	encoded, ok := strings.CutPrefix(token, codeModeOriginPrefix)
	if h.origins == nil || !ok || len(encoded) > base64.RawURLEncoding.EncodedLen(codeModeOriginPositionBytes+maxCodeModeReceiptID+h.origins.Overhead()) {
		return receipt, 0, false
	}
	sealed, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return receipt, 0, false
	}
	raw, err := h.origins.Open(nil, nil, sealed, nil)
	if err != nil || len(raw) <= codeModeOriginPositionBytes {
		return receipt, 0, false
	}
	receipt.id = string(raw[codeModeOriginPositionBytes:])
	receipt.seq = binary.BigEndian.Uint64(raw[:8])
	return receipt, int(binary.BigEndian.Uint32(raw[8:12])), receipt.seq != 0
}

func codeModePart(block tool.ContentBlock) codemode.Part {
	switch part := block.(type) {
	case tool.Text:
		return codemode.Part{Kind: "text", Text: string(part)}
	case tool.StructuredContent:
		return codemode.Part{Kind: "json", JSON: append(json.RawMessage(nil), part.Value...)}
	case tool.Image:
		return codemode.Part{Kind: "media", MimeType: part.MIMEType}
	case tool.Resource:
		return codemode.Part{Kind: "file_ref", MimeType: part.MIMEType}
	default:
		return codemode.Part{}
	}
}

func (h *codeModeHost) readCodeModeResult(receipt codeModeReplyReceipt) (tool.Result, error) {
	var events []*session.Event
	if reader, ok := h.owner.sessions.(session.EventReceiptReader); ok {
		event, err := reader.EventReceipt(h.receiptCtx, h.owner.ref, receipt.id)
		if err != nil {
			return tool.Result{}, fmt.Errorf("read original script receipt: %w", err)
		}
		events = []*session.Event{event}
	} else if reader, ok := h.owner.sessions.(session.PagedReader); ok {
		page, err := reader.EventsPage(h.receiptCtx, session.EventPageRequest{
			SessionRef: h.owner.ref, AfterSeq: receipt.seq - 1, ThroughSeq: receipt.seq, Limit: 1, Visibility: session.EventPageAllDurable,
		})
		if err != nil {
			return tool.Result{}, fmt.Errorf("read original script receipt: %w", err)
		}
		events = page.Events
	} else {
		// Older embedding stores still support explicit result selection.
		var err error
		events, err = h.owner.sessions.Events(h.receiptCtx, session.EventsRequest{SessionRef: h.owner.ref, IncludeTransient: true})
		if err != nil {
			return tool.Result{}, fmt.Errorf("read original script receipt: %w", err)
		}
	}
	for _, event := range events {
		if event != nil && event.ID == receipt.id && event.Seq == receipt.seq && event.Type == session.EventTypeToolResult && event.Visibility == session.VisibilityMirror && event.Tool != nil && event.Tool.Result != nil {
			return *event.Tool.Result, nil
		}
	}
	return tool.Result{}, fmt.Errorf("original script receipt is unavailable")
}

func (h *codeModeHost) wholeCodeModeResult(result tool.Result) ([]tool.ContentBlock, error) {
	output := codemode.Output{Reply: true}
	images := 0
	for _, block := range result.Content {
		part := codeModePart(block)
		if part.Kind == "media" || part.Kind == "file_ref" {
			images++
			// Size the same wire projection without reissuing a handle.
			part.Handle = "00000000-0000-0000-0000-000000000000"
		}
		output.Parts = append(output.Parts, part)
	}
	if images > h.limits.Calls {
		return nil, fmt.Errorf("returned media budget exceeded")
	}
	if !validCodeModeOutput(output, h.limits.OutputBytes) {
		return nil, fmt.Errorf("complete original result exceeds output budget")
	}
	return result.Content, nil
}
