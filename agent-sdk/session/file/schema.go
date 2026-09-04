package file

import (
	"log/slog"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const (
	documentKind                          = "caelis.sdk.session"
	documentVersion                       = 2
	indexVersion                          = 5
	indexFilename                         = ".sessions.index.sqlite"
	lockFilename                          = ".sessions.lock"
	transactionRecoveryMarkerFilename     = ".sessions.transactions.pending"
	generatedTitleMigrationMarkerFilename = ".sessions.generated-title-unicode-v1"
	workspaceKeyRepairMarkerFilename      = ".sessions.workspace-key-repair-v1.json"
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
	// Diagnostics receives bounded runtime/environment diagnostics. The file
	// store never includes paths, Session identities, events, or business data.
	Diagnostics *slog.Logger
}

// Store is the file-backed implementation of session.Service.
type Store struct {
	mu                  contextMutex
	legacyMigrationMu   sync.Mutex
	legacyMigration     MigrationReport
	rootDir             string
	sessionIDGenerator  func() string
	eventIDGenerator    func() string
	clock               func() time.Time
	diagnostics         *slog.Logger
	pathCache           map[string]string
	eventPageIndexes    map[string]*eventPageIndex
	eventPageIndexClock uint64
	eventLogCaches      map[string]*eventLogCache
	eventLogCacheBytes  int64
	eventLogCacheClock  uint64
	// eventLogCacheMaxBytes is an internal test seam for exercising the
	// oversized-log path without allocating a production-sized fixture.
	eventLogCacheMaxBytes   int64
	eventAppendPaths        sync.Map
	eventAppendIndexMu      contextMutex
	eventAppendIndexes      map[string]*eventAppendIndex
	eventAppendIndexBytes   int64
	eventAppendIndexClock   uint64
	durability              durabilityOps
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
	Fence                     *persistedSessionFence    `json:"lease,omitempty"`
	FenceEpoch                uint64                    `json:"lease_epoch,omitempty"`
}

// persistedSessionFence retains the current version-2 document encoding while
// the unpublished SDK surface uses fence terminology. Changing this storage
// shape requires an explicit document-version migration, not an SDK alias.
type persistedSessionFence struct {
	SessionRef  session.SessionRef `json:"session_ref"`
	FenceID     string             `json:"lease_id,omitempty"`
	OwnerID     string             `json:"owner_id,omitempty"`
	ClaimDigest string             `json:"claim_digest,omitempty"`
	// Revision is retained only in the version-2 storage encoding. Session
	// fences have no renewable public revision; new documents write 1 so the
	// existing on-disk shape remains stable and older values stay readable.
	Revision     uint64    `json:"revision,omitempty"`
	FencingToken uint64    `json:"fencing_token,omitempty"`
	AcquiredAt   time.Time `json:"acquired_at,omitempty"`
}

func persistedFenceFromSession(fence session.SessionFence) *persistedSessionFence {
	return &persistedSessionFence{
		SessionRef: fence.SessionRef, FenceID: fence.FenceID, OwnerID: fence.OwnerID,
		ClaimDigest: session.SessionFenceClaimDigest(fence), Revision: 1,
		FencingToken: fence.FencingToken, AcquiredAt: fence.AcquiredAt,
	}
}

func sessionFenceFromPersisted(fence *persistedSessionFence) session.SessionFence {
	if fence == nil {
		return session.SessionFence{}
	}
	return session.RestoreSessionFenceClaimDigest(session.SessionFence{
		SessionRef: fence.SessionRef, FenceID: fence.FenceID, OwnerID: fence.OwnerID,
		FencingToken: fence.FencingToken, AcquiredAt: fence.AcquiredAt,
	}, fence.ClaimDigest)
}
