// Package fs provides reusable filesystem skill discovery and loading for the
// Agent SDK.
//
// This package owns SKILL.md metadata parsing, directory scanning, plugin
// bundle merge/suppression, path resolution, and metadata caching. Caelis
// embedded system skill materialization and default ~/.caelis discovery roots
// remain in product-host bridge code.
package fs
