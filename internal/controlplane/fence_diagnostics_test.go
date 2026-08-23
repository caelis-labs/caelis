package controlplane

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestFencePhaseDiagnosticsRecordOnlyPhaseDurationAndClassification(t *testing.T) {
	var output bytes.Buffer
	base := time.Unix(1_000, 0)
	now := base
	diagnostics := &fencePhaseDiagnostics{
		logger: slog.New(slog.NewJSONHandler(&output, nil)),
		now:    func() time.Time { return now },
		slow:   time.Second,
	}

	diagnostics.observe(context.Background(), "acquire", base, nil)
	if output.Len() != 0 {
		t.Fatalf("fast successful phase diagnostic = %q, want none", output.String())
	}
	now = base.Add(1500 * time.Millisecond)
	diagnostics.observe(context.Background(), "acquire", base, nil)
	diagnostics.observe(context.Background(), "release_reconcile", now, errors.Join(session.ErrFenceConflict, errors.New("secret detail")))

	got := output.String()
	for _, want := range []string{
		`"component":"session_fence"`,
		`"phase":"acquire"`,
		`"elapsed_ms":1500`,
		`"outcome":"slow"`,
		`"phase":"release_reconcile"`,
		`"outcome":"fence_conflict"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostics = %q, want %s", got, want)
		}
	}
	if strings.Contains(got, "secret detail") {
		t.Fatalf("diagnostics leaked error text: %q", got)
	}
}
