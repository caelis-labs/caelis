// Package streamspool defines Control-owned transient stream storage.
//
// A spool improves delivery when a Surface is absent or slow. It is never the
// authority for execution, Session history, or Task results.
package streamspool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Namespace separates independently interpreted Control stream families.
type Namespace uint8

const (
	NamespaceTask Namespace = iota + 1
	NamespaceSession
)

func (n Namespace) String() string {
	switch n {
	case NamespaceTask:
		return "tasks"
	case NamespaceSession:
		return "sessions"
	default:
		return "unknown"
	}
}

// Valid reports whether the namespace is supported by this format version.
func (n Namespace) Valid() bool {
	return n == NamespaceTask || n == NamespaceSession
}

// Digest is the path-safe identity of one logical product stream.
type Digest [sha256.Size]byte

// Epoch distinguishes process lifetimes. Old epochs are never assumed exact.
type Epoch [16]byte

// Incarnation distinguishes physical histories for one logical key and epoch.
type Incarnation [16]byte

// LogicalKey identifies one product stream without exposing user-controlled
// identity in a filesystem path.
type LogicalKey struct {
	Namespace Namespace
	Digest    Digest
}

// Key identifies one physical partition.
type Key struct {
	LogicalKey
	Epoch       Epoch
	Incarnation Incarnation
}

// DigestStrings hashes length-delimited identity components. It must be used
// instead of concatenation so distinct tuples cannot alias.
func DigestStrings(parts ...string) Digest {
	h := sha256.New()
	for _, part := range parts {
		part = strings.TrimSpace(part)
		_, _ = fmt.Fprintf(h, "%08x", len(part))
		_, _ = h.Write([]byte(part))
	}
	var out Digest
	copy(out[:], h.Sum(nil))
	return out
}

// Hex returns the fixed path-safe digest encoding.
func (d Digest) Hex() string { return hex.EncodeToString(d[:]) }

// Offset is a zero-based logical record offset. A consumer cursor normally
// carries the next offset it expects to read.
type Offset uint64

// State is the local availability state of one partition.
type State uint8

const (
	StatePending State = iota + 1
	StateOpen
	StateEmptyTerminal
	StateSealed
	StatePoisoned
	StateStoreClosed
)

// Bounds is one atomic partition availability snapshot.
type Bounds struct {
	Low            Offset
	High           Offset
	OriginComplete bool
	State          State
}

// WriterOptions fixes immutable partition metadata at registration time.
type WriterOptions struct {
	OriginComplete bool
}

// Record is one validated raw record. Interpretation belongs to its Control
// namespace owner.
type Record struct {
	Offset     Offset
	Type       uint16
	OccurredAt time.Time
	Payload    []byte
}

// Writer is the sole append authority for one physical partition.
type Writer interface {
	Key() Key
	Append(context.Context, uint16, time.Time, []byte) (Offset, error)
	Bounds(context.Context) (Bounds, error)
	// FinishEmpty wakes readers when a producer terminates without one accepted
	// record. A later writer incarnation must be registered explicitly.
	FinishEmpty(context.Context) error
	// Seal permanently closes this physical partition after all accepted records.
	Seal(context.Context) error
	Close() error
}

// Reader owns one independent cursor. Closing it never changes producer or
// Task lifecycle.
type Reader interface {
	Key() Key
	Next(context.Context) (Record, error)
	Close() error
}

// Store is a Control cache. It contains no Task, Session, ACP, or Surface
// projection semantics.
type Store interface {
	Register(context.Context, LogicalKey, WriterOptions) (Writer, error)
	Resolve(context.Context, LogicalKey) (Key, Bounds, error)
	Reader(context.Context, Key, Offset) (Reader, error)
	Bounds(context.Context, Key) (Bounds, error)
	Remove(context.Context, Key) error
	Close() error
}
