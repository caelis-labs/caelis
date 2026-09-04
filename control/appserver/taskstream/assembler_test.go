package taskstream

import (
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestDeliveryAssemblerCommitsReplacementAtomically(t *testing.T) {
	t.Parallel()

	var assembler DeliveryAssembler
	begin := Delivery{Kind: DeliveryReplaceBegin, Source: SourceReplacement, SnapshotID: "snapshot-1"}
	if events, replacement, err := assembler.Accept(begin); err != nil || replacement || len(events) != 0 {
		t.Fatalf("begin = (%#v, %v, %v)", events, replacement, err)
	}
	page := Delivery{
		Kind: DeliveryReplacePage, Source: SourceReplacement, SnapshotID: "snapshot-1", Page: 0,
		Events: []eventstream.Envelope{{Kind: eventstream.KindNotice}},
	}
	if events, replacement, err := assembler.Accept(page); err != nil || replacement || len(events) != 0 {
		t.Fatalf("page = (%#v, %v, %v), replacement leaked before commit", events, replacement, err)
	}
	end := Delivery{Kind: DeliveryReplaceEnd, Source: SourceReplacement, SnapshotID: "snapshot-1", Page: 1}
	events, replacement, err := assembler.Accept(end)
	if err != nil || !replacement || len(events) != 1 {
		t.Fatalf("end = (%#v, %v, %v)", events, replacement, err)
	}
}

func TestDeliveryAssemblerRejectsMalformedReplacement(t *testing.T) {
	t.Parallel()

	var assembler DeliveryAssembler
	_, _, err := assembler.Accept(Delivery{
		Kind: DeliveryReplacePage, Source: SourceReplacement, SnapshotID: "snapshot-1", Page: 0,
	})
	if err == nil {
		t.Fatal("replacement page without begin was accepted")
	}
	if assembler.Pending() {
		t.Fatal("malformed replacement left a pending transaction")
	}
}

func TestDeliveryAssemblerEnforcesSourceResumeShape(t *testing.T) {
	t.Parallel()

	exact := eventstream.Envelope{
		Kind: eventstream.KindNotice, Cursor: "cursor-1", Notice: "exact",
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Position: &eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{
			Generation: "generation-1", Sequence: 1,
		}},
	}
	if events, replacement, err := new(DeliveryAssembler).Accept(Delivery{
		Kind: DeliveryAppendPage, Source: SourceExact,
		Events: []eventstream.Envelope{exact}, NextCursor: exact.Cursor,
	}); err != nil || replacement || len(events) != 1 {
		t.Fatalf("valid exact append = (%#v, %v, %v)", events, replacement, err)
	}
	if _, _, err := new(DeliveryAssembler).Accept(Delivery{
		Kind: DeliveryAppendPage, Source: SourceExact, Events: []eventstream.Envelope{exact},
	}); err == nil {
		t.Fatal("exact append without next cursor was accepted")
	}

	replacement := eventstream.CloneEnvelope(exact)
	replacement.Cursor = ""
	replacement.Position = nil
	assembler := new(DeliveryAssembler)
	_, _, _ = assembler.Accept(Delivery{Kind: DeliveryReplaceBegin, Source: SourceReplacement, SnapshotID: "snapshot-1"})
	if _, _, err := assembler.Accept(Delivery{
		Kind: DeliveryReplacePage, Source: SourceReplacement, SnapshotID: "snapshot-1",
		Events: []eventstream.Envelope{replacement},
	}); err != nil {
		t.Fatalf("cursorless replacement page: %v", err)
	}

	resumableReplacement := new(DeliveryAssembler)
	_, _, _ = resumableReplacement.Accept(Delivery{Kind: DeliveryReplaceBegin, Source: SourceReplacement, SnapshotID: "snapshot-2"})
	if _, _, err := resumableReplacement.Accept(Delivery{
		Kind: DeliveryReplacePage, Source: SourceReplacement, SnapshotID: "snapshot-2",
		Events: []eventstream.Envelope{exact},
	}); err == nil {
		t.Fatal("replacement page carrying resume identity was accepted")
	}
}
