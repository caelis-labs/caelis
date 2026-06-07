// Package file implements a file-backed session.Service.
//
// This is a Layer 4 sub-package of session/. It may import session/ and
// stdlib only.
//
// It stores canonical session records as JSON files and durable event streams
// as JSONL. ACP and model projections are computed by acp/ and session/
// replay helpers rather than stored in this package.
package file
