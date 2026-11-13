package codemode

import (
	"errors"
	"io/fs"
	"os"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// ErrorCodeJavaScript identifies a script exception or invalid script result.
const ErrorCodeJavaScript tool.ErrorCode = "javascript_error"

// ExecutionError carries a Runtime-normalized failure. Its tool result already
// contains public diagnostics and must be preserved rather than rebuilt from Err,
// whose underlying Host causes may contain private transport details.
type ExecutionError struct {
	// Message is the public script diagnostic, excluding private Host causes.
	Message string
	Code    tool.ErrorCode
	Err     error
}

func (e *ExecutionError) Error() string             { return e.Message }
func (e *ExecutionError) ErrorCode() errorcode.Code { return e.Code }
func (e *ExecutionError) Unwrap() error             { return e.Err }

// NewPublicPermissionError marks a fixed Runtime-owned permission diagnostic as
// safe for JavaScript, model and Task output. The message must not include tool
// arguments, credentials, transport details or other untrusted data.
func NewPublicPermissionError(message string) error {
	return &publicPermissionError{message: message}
}

type publicPermissionError struct {
	message string
}

func (e *publicPermissionError) Error() string { return e.message }
func (e *publicPermissionError) ErrorCode() errorcode.Code {
	return tool.ErrorCodePermissionDenied
}

// PublicHostError projects a Host failure for JavaScript, model and Task output.
// Only typed input details, explicitly safe permission diagnostics and fixed
// recovery guidance cross this boundary; transport bodies, credentials and
// arbitrary internal error chains stay private.
func PublicHostError(name string, err error) *tool.ToolError {
	code := errorcode.CodeOf(err)
	if payload := tool.ErrorPayload(err); payload != nil {
		if value, ok := payload["error_code"].(string); ok {
			code = errorcode.Code(value)
		}
	}
	if len(name) > 192 {
		name = name[:192]
	}
	label := string(code)
	if len(label) > 64 {
		label = "unknown"
		code = errorcode.Unknown
	}
	message := name + " [" + label + "]"
	switch code {
	case tool.ErrorCodeStaleRevision:
		return tool.NewError(code, message+": tool directory refreshed; binding is stale; use tools.search(name) to reload its schema/binding before calling. Old arguments were not executed.")
	case errorcode.UnknownOutcome:
		return tool.NewError(code, message+": tool outcome is unknown; do not replay the effect.")
	}
	var pathError *os.PathError
	if code == tool.ErrorCodeNotFound && errors.Is(err, fs.ErrNotExist) && errors.As(err, &pathError) {
		path := pathError.Path
		if len(path) > 512 {
			path = path[:512]
		}
		return tool.NewError(code, message+": $.path: file does not exist: "+path)
	}
	var permission *publicPermissionError
	if code == tool.ErrorCodePermissionDenied && errors.As(err, &permission) {
		detail := permission.message
		if len(detail) > 768 {
			detail = detail[:768]
		}
		return tool.NewError(code, message+": "+detail)
	}
	var validation *tool.ToolError
	if code == tool.ErrorCodeInvalidInput && errors.As(err, &validation) && validation.Err == nil {
		detail := validation.Message
		if len(detail) > 768 {
			detail = detail[:768]
		}
		return tool.NewError(code, message+": "+detail)
	}
	return tool.NewError(code, message+": Host request failed")
}
