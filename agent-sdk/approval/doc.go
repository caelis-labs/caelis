// Package approval defines approval review contracts for the Agent SDK.
//
// This package owns the public approval-domain contracts migrated from
// ports/approval: modes, payloads, review requests/results, option
// normalization, runtime response bridging, and session-state helpers.
// Concrete reviewer implementations remain in product host packages such as
// app/gatewayapp until a later horizontal move slice.
package approval
