package appserver

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/internal/operationreceipt"
)

type admittedApplicationOperationKey struct{}

type admittedApplicationOperation struct {
	principal Principal
	scope     application.Scope
	operation application.Operation
	store     *application.Store
}

// Only the fresh permanent-intent owner can enter this path. Public principals
// or an existing incomplete receipt never grant redispatch.
func withAdmittedApplicationOperation(ctx context.Context, principal Principal, scope application.Scope, operation application.Operation, store *application.Store) context.Context {
	return context.WithValue(ctx, admittedApplicationOperationKey{}, admittedApplicationOperation{principal, scope, operation, store})
}

func admittedApplicationOperationFrom(ctx context.Context, principal Principal, id string) (admittedApplicationOperation, bool) {
	admitted, ok := ctx.Value(admittedApplicationOperationKey{}).(admittedApplicationOperation)
	return admitted, ok && admitted.principal.ID == principal.ID && admitted.principal.ApplicationID == principal.ApplicationID && admitted.principal.ConnectionID == principal.ConnectionID && slices.Equal(admitted.principal.Roles, principal.Roles) && admitted.operation.ID == id
}

func (s *CommandService) completeCommandReceipt(ctx context.Context, intent OperationIntent, result CommandResult, admitted admittedApplicationOperation, permanent bool) error {
	if !permanent {
		_, err := s.config.Operations.Complete(ctx, intent, result)
		return err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if writer, ok := s.config.Operations.(interface {
		completeAdmittedApplicationOperation(context.Context, admittedApplicationOperation, []byte) error
	}); ok {
		return writer.completeAdmittedApplicationOperation(ctx, admitted, data)
	}
	return admitted.store.CompleteOperation(ctx, admitted.scope, admitted.operation.ID, data)
}

// Use the command ledger's independently owned connection, preserving receipt
// recovery when the admission Store closes after the effect. There remains only
// one permanent row; the ordinary expiring ledger receives no duplicate entry.
func (s *SQLiteOperationStore) completeAdmittedApplicationOperation(ctx context.Context, admitted admittedApplicationOperation, data []byte) error {
	matched, err := operationreceipt.Complete(ctx, s.db, operationreceipt.Key{
		Principal: admitted.scope.PrincipalID, Application: admitted.scope.ApplicationID,
		Connection: admitted.scope.ConnectionID, Operation: admitted.operation.ID, Digest: admitted.operation.Digest,
	}, data)
	if err == nil && !matched {
		return application.ErrConflict
	}
	return err
}
