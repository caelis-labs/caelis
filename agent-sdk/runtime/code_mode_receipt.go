package runtime

import (
	"encoding/json"
	"fmt"

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

type codeModeResultHandle struct {
	receipt   codeModeReplyReceipt
	block     int
	reference tool.ContentBlock
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
		if event != nil && event.ID == receipt.id && event.Seq == receipt.seq && event.Tool != nil && event.Tool.Result != nil {
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
