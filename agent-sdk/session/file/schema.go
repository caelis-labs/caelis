package file

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const (
	documentKind    = "caelis.sdk.session"
	documentVersion = 1
	indexVersion    = 3
	indexFilename   = ".sessions.index.sqlite"
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
	writeDocumentFault func() error
	transactionFault   func(string) error
}

// Service is the file-backed implementation of session.Service.
type Service struct {
	store *Store
}

// TaskStore is the task.Store facade backed by the same file store index.
type TaskStore struct {
	store *Store
}

type persistedDocument struct {
	Kind                string          `json:"kind"`
	Version             int             `json:"version"`
	Session             session.Session `json:"session"`
	State               map[string]any  `json:"state"`
	AppliedTransactions map[string]bool `json:"applied_transactions,omitempty"`
}
