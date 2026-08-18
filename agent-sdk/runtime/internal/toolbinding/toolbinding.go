// Package toolbinding identifies Runtime-owned tool wrappers without using
// model-visible tool names as authority.
package toolbinding

// Token keeps the marker interface inside agent-sdk/runtime: packages outside
// that tree cannot import this internal type and impersonate a Runtime wrapper.
type Token struct{}

type taskResultSource interface {
	RuntimeTaskResultSource(Token) bool
}

// IsTaskResultSource reports whether candidate may author canonical Task
// synchronization facts.
func IsTaskResultSource(candidate any) bool {
	source, ok := candidate.(taskResultSource)
	return ok && source.RuntimeTaskResultSource(Token{})
}

const (
	MetadataSection    = "binding"
	MetadataTaskResult = "task_result"
)
