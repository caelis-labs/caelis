package codemode

import (
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestPublicHostErrorRequiresExplicitSafePermissionDiagnostic(t *testing.T) {
	const diagnostic = "native command stdin is prohibited from JavaScript"
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{name: "plain", err: tool.NewError(tool.ErrorCodePermissionDenied, "credential=secret"), want: "Host request failed"},
		{name: "wrapped", err: fmt.Errorf("credential=secret: %w", tool.NewError(tool.ErrorCodePermissionDenied, "credential=secret")), want: "Host request failed"},
		{name: "wrapped_path_is_not_public", err: tool.WrapError(tool.ErrorCodePermissionDenied, &fs.PathError{Op: "read", Path: "credential=secret", Err: fs.ErrNotExist}, "credential=secret"), want: "Host request failed"},
		{name: "matching_text_is_not_trusted", err: tool.NewError(tool.ErrorCodePermissionDenied, diagnostic), want: "Host request failed"},
		{name: "explicit", err: NewPublicPermissionError(diagnostic), want: diagnostic},
		{name: "wrapped_explicit", err: fmt.Errorf("credential=secret: %w", NewPublicPermissionError(diagnostic)), want: diagnostic},
	} {
		t.Run(tc.name, func(t *testing.T) {
			public := PublicHostError("Task", tc.err)
			if public.Code != tool.ErrorCodePermissionDenied || public.Message != "Task [permission_denied]: "+tc.want || strings.Contains(public.Message, "secret") {
				t.Fatalf("unexpected public permission diagnostic: %+v", public)
			}
		})
	}
}
