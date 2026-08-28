// Package acppermission adapts standard ACP request_permission payloads to the
// normalized approval semantics owned by Agent SDK contracts.
//
// This Control adapter owns no approval decision, display, or orchestration
// policy. Agent SDK packages must not depend on it or on ACP wire schemas.
package acppermission
