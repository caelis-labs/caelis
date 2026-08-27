// Package semantic adapts ACP permission coordination payloads to the
// normalized approval semantics owned by Agent SDK contracts.
//
// This package is a product transport adapter. It may depend on Agent SDK
// contracts; Agent SDK packages must not depend on this package or on ACP wire
// schemas. The adapter does not apply display or orchestration policy.
package semantic
