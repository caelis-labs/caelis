package operationreceipt

import (
	"context"
	"database/sql"
	"errors"
)

type transactionKey struct{}
type TransactionCompletion func(context.Context, *sql.Tx, []byte) error

// WithTransaction binds completion of an already admitted Control operation.
// The domain commits its state and this exact receipt together; this callback
// cannot admit another operation or grant dispatch.
func WithTransaction(ctx context.Context, complete TransactionCompletion) context.Context {
	return context.WithValue(ctx, transactionKey{}, complete)
}

func HasTransaction(ctx context.Context) bool {
	complete, ok := ctx.Value(transactionKey{}).(TransactionCompletion)
	return ok && complete != nil
}

func CompleteTransaction(ctx context.Context, tx *sql.Tx, result []byte) error {
	complete, ok := ctx.Value(transactionKey{}).(TransactionCompletion)
	if !ok || complete == nil {
		return errors.New("control transaction receipt is unavailable")
	}
	return complete(ctx, tx, result)
}
