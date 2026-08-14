// Package semantic translates between ACP wire payloads and the normalized
// protocol semantics owned by agent-sdk/session.
//
// This package is a product transport adapter. It may depend on Agent SDK
// semantic DTOs; Agent SDK packages must not depend on this package or on ACP
// wire schemas. The codecs intentionally do not apply display policy,
// orchestration policy, or Caelis-specific metadata extensions.
package semantic
