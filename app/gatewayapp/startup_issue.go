package gatewayapp

import (
	"errors"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

// StartupIssueCode identifies one product startup failure whose public meaning
// is stable across the process boundary between the CLI and managed Host.
type StartupIssueCode string

const (
	// StartupIssueWorkspaceIdentityConflict means one durable workspace key is
	// bound to more than one canonical working directory.
	StartupIssueWorkspaceIdentityConflict StartupIssueCode = "CAELIS_STARTUP_WORKSPACE_IDENTITY_CONFLICT"
)

type startupIssueError struct {
	code   StartupIssueCode
	detail string
}

func (e *startupIssueError) Error() string {
	return fmt.Sprintf("[%s] %s", e.code, strings.TrimSpace(e.detail))
}

func (e *startupIssueError) ErrorCode() errorcode.Code {
	return errorcode.FailedPrecondition
}

func (e *startupIssueError) StartupIssueCode() StartupIssueCode {
	return e.code
}

func newStartupIssueError(code StartupIssueCode, detail string) error {
	return &startupIssueError{code: code, detail: detail}
}

// StartupIssueCodeOf returns the product startup issue carried by err.
func StartupIssueCodeOf(err error) (StartupIssueCode, bool) {
	var issue interface {
		StartupIssueCode() StartupIssueCode
	}
	if !errors.As(err, &issue) {
		return "", false
	}
	code := issue.StartupIssueCode()
	return code, code != ""
}

// ParseStartupIssueCode recognizes a stable startup issue token in managed
// Host output. It intentionally does not infer codes from prose.
func ParseStartupIssueCode(text string) (StartupIssueCode, bool) {
	for _, code := range []StartupIssueCode{StartupIssueWorkspaceIdentityConflict} {
		if strings.Contains(text, "["+string(code)+"]") {
			return code, true
		}
	}
	return "", false
}

// StartupIssueDescription returns bounded public text for one startup issue.
func StartupIssueDescription(code StartupIssueCode) string {
	switch code {
	case StartupIssueWorkspaceIdentityConflict:
		return "stored Session workspace identities conflict"
	default:
		return "local Control Host could not start"
	}
}

// StartupIssueRepairable reports whether caelis doctor owns a repair for code.
func StartupIssueRepairable(code StartupIssueCode) bool {
	return code == StartupIssueWorkspaceIdentityConflict
}
