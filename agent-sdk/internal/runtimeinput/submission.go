// Package runtimeinput owns submission kinds that are reserved for trusted
// Runtime orchestration and must never be accepted through the public Runner
// submission API.
package runtimeinput

import agent "github.com/caelis-labs/caelis/agent-sdk"

// ModelContext injects Runtime-authored context at the next safe model boundary
// without creating client-visible dialogue.
const ModelContext agent.SubmissionKind = "runtime_model_context"
