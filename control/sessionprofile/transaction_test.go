package sessionprofile

import (
	"context"
	"database/sql"
	"github.com/caelis-labs/caelis/control/internal/operationreceipt"
)

// Domain tests exercise CAS/catalog rules inside the command-owned transaction.
// Real admission, persistence and replay are tested through appserver and HTTP;
// this fixture deliberately has no independent operation store or replay logic.
func mutationContext(ctx context.Context) context.Context {
	return operationreceipt.WithTransaction(ctx, func(context.Context, *sql.Tx, []byte) error { return nil })
}
