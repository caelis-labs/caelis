// Package controller defines the runtime-facing control-plane contract for
// ACP-backed main controllers and sidecar participants.
//
// This package owns the public controller-domain contracts migrated from
// ports/controller: backend interfaces, attach/detach/handoff/turn/participant
// request DTOs, turn handles, cancel results, controller approval bridge
// payloads, remote controller status/config DTOs, and normalization helpers.
package controller
