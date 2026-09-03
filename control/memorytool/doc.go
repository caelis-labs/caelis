// Package memorytool owns the Caelis model projection for the two Memory
// Appliance operations: Remember(text) and Recall(query). Remember commits
// explicit durable evidence. Recall runs a bounded lexical query over authorized
// receipt and derived-record projections; the current built-in path does not
// invoke the optional Steward model. All identity, audience, capability,
// consistency, source, and budget controls stay hidden.
package memorytool
