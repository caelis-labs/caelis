package display_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestUserVisibleErrorSummarizesCommittedPersistenceFailure(t *testing.T) {
	detail := `replace_document C:\Users\private\session-123.json: Access is denied.`
	err := &session.CommittedError{Err: errors.New(detail)}

	got := display.UserVisibleError(err)
	want := "The change was saved, but Caelis could not finish local storage maintenance."
	if got != want {
		t.Fatalf("UserVisibleError() = %q, want %q", got, want)
	}
	if strings.Contains(got, "private") || strings.Contains(got, "session-123") || strings.Contains(got, "Access is denied") {
		t.Fatalf("UserVisibleError() leaked persistence diagnostics: %q", got)
	}
	if !strings.Contains(err.Error(), detail) {
		t.Fatalf("CommittedError.Error() = %q, want internal detail preserved", err)
	}
}
