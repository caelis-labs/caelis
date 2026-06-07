// Package commands owns command semantics shared by Presentation surfaces:
// doctor, model selection, sandbox lifecycle, agent profiles, and slash
// command effects.
//
// This is a Layer 3 sub-package of app/. It may import app/, gateway/,
// and provider-neutral Layer 4 types. It must not import tui/, headless/,
// acp/server/, or runtime internals.
//
// Phase 1: skeleton only. No behavior.
package commands
