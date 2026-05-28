package file

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/session"
)

const (
	documentKind    = "caelis.sdk.session"
	documentVersion = 1
	indexKind       = "caelis.sdk.session_index"
	indexVersion    = 1
	indexFilename   = ".sessions.index.json"
	lockFilename    = ".sessions.lock"
)

var storeRootLocks sync.Map

type storeRootLockMode int

const (
	storeRootLockShared storeRootLockMode = iota
	storeRootLockExclusive
)

type storeRootLock struct {
	mu sync.RWMutex
}

// Config defines one single-file durable session store instance.
type Config struct {
	RootDir            string
	SessionIDGenerator func() string
	EventIDGenerator   func() string
	Clock              func() time.Time
}

// Store is the file-backed implementation of session.Store.
type Store struct {
	mu                 sync.Mutex
	rootDir            string
	sessionIDGenerator func() string
	eventIDGenerator   func() string
	clock              func() time.Time
	idCounter          atomic.Uint64
	pathCache          map[string]string
}

// Service is the file-backed implementation of session.Service.
type Service struct {
	store *Store
}

type persistedDocument struct {
	Kind    string           `json:"kind"`
	Version int              `json:"version"`
	Session session.Session  `json:"session"`
	Events  []*session.Event `json:"events,omitempty"`
	State   map[string]any   `json:"state"`
}

type persistedSessionIndex struct {
	Kind     string                       `json:"kind"`
	Version  int                          `json:"version"`
	Sessions []persistedSessionIndexEntry `json:"sessions,omitempty"`
}

type persistedSessionIndexEntry struct {
	Session session.SessionSummary `json:"session"`
	Path    string                 `json:"path"`
}
