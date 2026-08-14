package model

import "github.com/caelis-labs/caelis/agent-sdk/errorcode"

// ErrorCodeOf returns one stable model error category without inspecting
// provider message text.
func ErrorCodeOf(err error) errorcode.Code { return errorcode.CodeOf(err) }
