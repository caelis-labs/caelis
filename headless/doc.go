// Package headless implements one-shot CLI output mode. It consumes
// gateway.Service for a single turn and returns structured or plain-text
// output to stdout.
//
// This is a Layer 2 (Presentation) package. It imports gateway/ and acp/
// types. It must not import Layer 4 runtime internals.
//
// Phase 1: skeleton only. No behavior.
package headless
