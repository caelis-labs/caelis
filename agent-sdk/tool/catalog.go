package tool

import (
	"context"
	"errors"
)

// ErrCatalogSnapshotStale means a catalog identity cannot be held because it is
// initializing, changing, contended or superseded. It grants no tool authority.
var ErrCatalogSnapshotStale = errors.New("tool catalog snapshot is stale")

// CatalogSource optionally guards a Source's directory identity through a short
// persistence transaction. ValidateCatalog returns a non-nil release on success
// and ErrCatalogSnapshotStale for an unusable identity. Callers must release on
// every path and must not hold the guard during generation, approval or remote
// execution. Permission and cancellation errors remain distinct from staleness.
type CatalogSource interface {
	Source
	CatalogRevision() string
	ValidateCatalog(context.Context, string) (func(), error)
}
