package file

import (
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const (
	documentKind                      = "caelis.sdk.session"
	documentVersion                   = 1
	indexVersion                      = 3
	indexFilename                     = ".sessions.index.sqlite"
	lockFilename                      = ".sessions.lock"
	transactionRecoveryMarkerFilename = ".sessions.transactions.pending"
)

var storeRootLocks sync.Map

type storeRootLockMode int

const (
	storeRootLockShared storeRootLockMode = iota
	storeRootLockExclusive
)

type storeRootLock struct {
	mu                  contextMutex
	recoveryInitialized bool
}

// Config defines one single-file durable session store instance.
type Config struct {
	RootDir            string
	SessionIDGenerator func() string
	EventIDGenerator   func() string
	Clock              func() time.Time
}

// Store is the file-backed implementation of session.Service.
type Store struct {
	mu                      contextMutex
	rootDir                 string
	sessionIDGenerator      func() string
	eventIDGenerator        func() string
	clock                   func() time.Time
	pathCache               map[string]string
	eventPageIndexes        map[string]*eventPageIndex
	eventPageIndexClock     uint64
	eventLogCaches          map[string]*eventLogCache
	eventLogCacheBytes      int64
	eventLogCacheClock      uint64
	writeDocumentFault      func() error
	transactionFault        func(string) error
	transactionRecoveryScan func()
	// approvalRecoverySessionDone is an optional test seam invoked after one
	// Session recovery transaction has released all Store and root locks.
	approvalRecoverySessionDone func(session.SessionRef)
	// eventPageLineRead is an optional test seam for proving bounded physical
	// paging without exposing checkpoint internals through the SDK contract.
	eventPageLineRead func(path string, lineNo int, offset int64)
	// eventLogLineRead is an optional test seam for measuring incremental
	// cached history reads used by append preparation.
	eventLogLineRead func(path string, lineNo int, offset int64)
}

// TaskStore is the task.Store facade backed by the same file store index.
type TaskStore struct {
	store *Store
}

type persistedDocument struct {
	Kind                      string                    `json:"kind"`
	Version                   int                       `json:"version"`
	Session                   session.Session           `json:"session"`
	State                     map[string]any            `json:"state"`
	PendingApprovals          map[string]*session.Event `json:"pending_approvals"`
	AppliedTransactions       map[string]bool           `json:"applied_transactions,omitempty"`
	AppliedTransactionDigests map[string]string         `json:"applied_transaction_digests,omitempty"`
	Lease                     *session.SessionLease     `json:"lease,omitempty"`
	LeaseEpoch                uint64                    `json:"lease_epoch,omitempty"`
}
