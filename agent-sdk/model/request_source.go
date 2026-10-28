package model

// RequestSource binds a request to the canonical source observed during message
// assembly. A later Store read or response cannot advance this coverage. Hashes
// are SHA-256 of the exact JSON source prefix and messages, respectively.
// Model-owned image preparation carries a matching message hash forward to its
// prepared wire messages while retaining the canonical source prefix and range.
// Requests without a binding remain valid for ordinary model generation.
type RequestSource struct {
	SourceThroughSeq uint64
	SourcePrefixHash string
	MessagesHash     string
}
