package appserver

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestApprovalRecoveryDiagnosticsRecordSlowStartupPhaseWithoutSessionData(t *testing.T) {
	var output bytes.Buffer
	base := time.Unix(2_000, 0)
	diagnostics := &approvalRecoveryDiagnostics{
		logger: slog.New(slog.NewJSONHandler(&output, nil)),
		now:    func() time.Time { return base.Add(2 * time.Second) },
		slow:   time.Second,
	}
	diagnostics.observe(context.Background(), "startup_recovery", base, nil)
	got := output.String()
	for _, want := range []string{
		`"component":"session_fence"`,
		`"phase":"startup_recovery"`,
		`"elapsed_ms":2000`,
		`"outcome":"slow"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostics = %q, want %s", got, want)
		}
	}
}
