package gatewayapp

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/streamspool"
)

func TestHostSpoolAdmitsFeedEnvelopeLimitAndRetainsSuffix(t *testing.T) {
	host, _ := newLocalStateTestStack(t)
	spool := host.composition.authorities.streamSpool
	if spool == nil {
		t.Fatal("Host did not open its default file spool")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	writer, err := spool.Register(ctx, streamspool.LogicalKey{
		Namespace: streamspool.NamespaceSession,
		Digest:    streamspool.DigestStrings(t.Name()),
	}, streamspool.WriterOptions{OriginComplete: true})
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	// Spool payloads are opaque. Exercise the full admitted Envelope byte budget,
	// including room beyond the payload for record and allocation accounting.
	payload := bytes.Repeat([]byte("x"), appserver.MaxFeedReplacementPageBytes)
	const recordCount = 3
	for i := range recordCount {
		offset, err := writer.Append(ctx, 1, time.Time{}, payload)
		if err != nil || offset != streamspool.Offset(i) {
			t.Fatalf("maximum-size Append(%d) = (%d, %v)", i, offset, err)
		}
	}
	bounds, err := writer.Bounds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bounds.State != streamspool.StateOpen || bounds.High != recordCount || bounds.Low == 0 || bounds.Low >= bounds.High {
		t.Fatalf("expected healthy rolling retention after maximum-size records, got %+v", bounds)
	}

	reader, err := spool.Reader(ctx, writer.Key(), bounds.High-1)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	record, err := reader.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if record.Offset != bounds.High-1 || !bytes.Equal(record.Payload, payload) {
		t.Fatalf("retained record = offset %d, %d bytes", record.Offset, len(record.Payload))
	}

	later := []byte("later transient delta")
	offset, err := writer.Append(ctx, 1, time.Time{}, later)
	if err != nil || offset != recordCount {
		t.Fatalf("Append after retention = (%d, %v)", offset, err)
	}
	record, err = reader.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if record.Offset != offset || !bytes.Equal(record.Payload, later) {
		t.Fatalf("later record = offset %d, %q", record.Offset, record.Payload)
	}
}
