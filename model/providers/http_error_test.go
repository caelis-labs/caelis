package providers

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/model"
)

func TestStatusErrorDetectsContextOverflow(t *testing.T) {
	err := statusError(&http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"maximum context length exceeded"}}`)),
	})
	if !model.IsContextOverflow(err) {
		t.Fatalf("statusError() = %v, want ContextOverflowError", err)
	}
}

func TestStatusErrorDoesNotTreatAuthAsContextOverflow(t *testing.T) {
	err := statusError(&http.Response{
		StatusCode: http.StatusUnauthorized,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"unauthorized"}}`)),
	})
	if model.IsContextOverflow(err) {
		t.Fatalf("statusError() = %v, want non-overflow auth error", err)
	}
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("statusError() = %v, want status and body", err)
	}
}
