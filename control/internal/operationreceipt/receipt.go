// Package operationreceipt owns immutable writes to the retained scoped operation
// ledger. Its existing table shape is shared with released Hosts; it neither
// grants dispatch nor changes operation retention.
package operationreceipt

import (
	"context"
	"database/sql"
)

// Key identifies one admitted intent, including its immutable payload digest.
type Key struct {
	Principal, Application, Connection, Operation, Digest string
}

// Complete writes the original intent's receipt once, or accepts its exact
// existing bytes. A false return means the intent or receipt did not match.
// Authorization and admission belong to the caller. Completion deliberately
// does not require a live connection lease after the effect has already run.
func Complete(ctx context.Context, db *sql.DB, key Key, result []byte) (bool, error) {
	updated, err := db.ExecContext(ctx, `UPDATE app_operations SET result=?
 WHERE principal=? AND application=? AND connection=? AND operation=? AND digest=?
 AND (result IS NULL OR result=?)`, result, key.Principal, key.Application,
		key.Connection, key.Operation, key.Digest, result)
	if err != nil {
		return false, err
	}
	count, err := updated.RowsAffected()
	return count == 1, err
}
