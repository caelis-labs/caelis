// Package filesystem implements the shared builtin file tools.
//
// File access goes through the sandbox FileSystem. Read, Grep, and ViewImage
// require regular opened files. The Unix host opener uses a nonblocking open
// and descriptor type check to reject FIFO replacement without waiting for a
// writer. Cancellation checks between reads do not interrupt arbitrary stalled
// kernel filesystem operations. Limits:
//
//   - Read defaults to a maximum of 400 lines. A single line over 8MiB is an error;
//     numbered content pages at whole-line boundaries up to 8MiB.
//   - Grep scans with an 8MiB line buffer, returns at most 100 hits, and
//     excerpts long matches to 2000 runes. Scanner overflow and cancellation
//     fail the search instead of reporting no matches.
//   - Glob and Grep read at most 1MiB of a regular .gitignore when building
//     workspace exclude rules. A missing, oversized, special, or unreadable file is
//     treated as no extra ignore rules.
package filesystem
