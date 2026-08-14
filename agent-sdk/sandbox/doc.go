// Package sandbox defines sandbox runtime and execution-environment contracts
// for the Agent SDK.
//
// This package owns the public sandbox-domain contracts migrated from
// ports/sandbox. Sandbox backends and shared backend helpers live under
// agent-sdk/sandbox/{bwrap,landlock,seatbelt,host} and agent-sdk/sandbox/backend/*.
// The Windows backend lives under agent-sdk/sandbox/windows.
package sandbox
