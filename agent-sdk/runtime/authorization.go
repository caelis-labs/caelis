package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// PolicyConsentWriter commits an explicitly selected persistent Policy rule
// through the embedding's canonical configuration command owner. It retains the
// original decision ID and returns the exact committed rule/revision receipt.
type PolicyConsentWriter func(context.Context, string, session.AuthorizationScope) (ruleID string, revision uint64, err error)

type authorizationFlight struct {
	scope    session.AuthorizationScope
	cancel   context.CancelFunc
	done     chan struct{}
	waiters  int
	response approval.Response
	err      error
}

type policyDispatchKey struct{}
type policyDispatchPermit struct {
	mu       sync.Mutex
	used     bool
	runtime  *Runtime
	session  session.Session
	decision policy.Decision
	optionID string
}

func (r *Runtime) authorizationEvidence(ctx context.Context, active session.Session) (session.AuthorizationEvidence, error) {
	events, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		return session.AuthorizationEvidence{}, err
	}
	return session.ProjectAuthorizations(active, events), nil
}

func (r *Runtime) checkAuthorization(ctx context.Context, active session.Session, a *session.ApprovalAuthorization) (session.AuthorizationEvidence, error) {
	if a == nil {
		return session.AuthorizationEvidence{}, nil
	}
	if a.Scope.SessionID != "" {
		current, err := r.sessions.Session(ctx, active.SessionRef)
		if err != nil {
			return session.AuthorizationEvidence{}, err
		}
		if !a.Scope.MatchesSession(current) {
			return session.AuthorizationEvidence{}, errors.New("authorization has a different current Session incarnation or workspace")
		}
		active = current
	}
	evidence, err := r.authorizationEvidence(ctx, active)
	if err != nil {
		return session.AuthorizationEvidence{}, err
	}
	if evidence.Revocations[a.Scope.Key()] != a.Revocation {
		return session.AuthorizationEvidence{}, errors.New("authorization revoked while call was pending")
	}
	return evidence, nil
}

// RevokeAuthorization appends one revocation fact under the approval owner.
// The caller must retain the original request ID when an append is uncertain.
// It cancels pending consent, but never promises to undo an admitted effect.
func (r *Runtime) RevokeAuthorization(ctx context.Context, ref session.SessionRef, scope session.AuthorizationScope, requestID string) error {
	if requestID == "" || scope.SessionID != ref.SessionID {
		return errors.New("revocation requires the exact Session scope and request identity")
	}
	if err := scope.Validate(); err != nil {
		return err
	}
	r.authorizationMu.Lock()
	defer r.authorizationMu.Unlock()
	active, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return err
	}
	if !scope.MatchesSession(active) {
		return errors.New("revocation scope has a different Session incarnation or workspace")
	}
	events, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: ref, IncludeTransient: true})
	if err != nil {
		return err
	}
	confirmed, err := authorizationRevocationConfirmed(events, requestID, scope)
	if err != nil {
		return err
	}
	if confirmed {
		r.cancelAuthorizationFlights(scope)
		return nil
	}
	_, err = r.sessions.AppendEvent(ctx, session.AppendEventRequest{SessionRef: ref, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeApproval), Event: &session.Event{IdempotencyKey: "authorization-revoke:" + requestID, Type: session.EventTypeLifecycle, Visibility: session.VisibilityJournal, Time: r.now(), Lifecycle: &session.EventLifecycle{Status: "authorization_revoked"}, AuthorizationRevocation: &session.AuthorizationRevocation{Schema: session.AuthorizationSchemaVersion, Scope: scope}}})
	if err != nil {
		if session.IsCommitted(err) {
			recoveryCtx, cancel := boundedApprovalRecoveryContext(ctx)
			defer cancel()
			durable, loadErr := r.sessions.Events(recoveryCtx, session.EventsRequest{SessionRef: ref, IncludeTransient: true})
			if loadErr != nil {
				return errors.Join(err, loadErr)
			}
			confirmed, checkErr := authorizationRevocationConfirmed(durable, requestID, scope)
			if checkErr != nil {
				return errors.Join(err, checkErr)
			}
			if confirmed {
				r.cancelAuthorizationFlights(scope)
			}
		}
		// Preserve the original post-commit report and request identity even
		// when readback has confirmed the fact and cancelled its producer.
		return err
	}
	r.cancelAuthorizationFlights(scope)
	return nil
}

func authorizationRevocationConfirmed(events []*session.Event, requestID string, scope session.AuthorizationScope) (bool, error) {
	for _, event := range events {
		if event != nil && event.IdempotencyKey == "authorization-revoke:"+requestID {
			revocation := event.AuthorizationRevocation
			if revocation == nil || revocation.Schema != session.AuthorizationSchemaVersion || revocation.Scope != scope {
				return false, errors.New("revocation request identity already has different content")
			}
			return true, nil
		}
	}
	return false, nil
}

// cancelAuthorizationFlights requires authorizationMu and is shared by every
// path that confirms the original durable revocation, including readback.
func (r *Runtime) cancelAuthorizationFlights(scope session.AuthorizationScope) {
	for _, flight := range r.authorizationFlights {
		if flight.scope == scope {
			flight.cancel()
		}
	}
}

func (t policyAdmission) resolvePolicyApproval(ctx context.Context, request approval.Request) (approval.Response, error) {
	r := t.approval.runtime
	a := request.Authorization
	share := a != nil && !a.PerCall
	if a != nil {
		for _, choice := range a.Choices {
			if choice.Persistence != session.AuthorizationSession {
				share = false
			}
		}
	}
	if r == nil || !share {
		return t.resolveApproval(ctx, request)
	}
	r.authorizationMu.Lock()
	evidence, err := r.checkAuthorization(ctx, t.session, a)
	if err != nil {
		r.authorizationMu.Unlock()
		return approval.Response{}, err
	}
	for _, scope := range evidence.Consents {
		if scope == a.Scope {
			r.authorizationMu.Unlock()
			return approval.Response{Outcome: "selected", OptionID: a.Choices[0].OptionID, Approved: true}, nil
		}
	}
	key := fmt.Sprintf("%s:%d:%v", a.Scope.Key(), a.Revocation, a.Choices)
	flight := r.authorizationFlights[key]
	if flight == nil {
		owner := t.approval.ctx
		if owner == nil {
			owner = ctx
		}
		work, cancel := context.WithCancel(owner)
		var finish func()
		if scope := taskScopeFromContext(owner); scope != nil {
			id := r.nextID("approval-producer", nil)
			continuation, err := scope.register(owner, id, scope.currentGeneration(), taskContinuationOrdinary)
			if err != nil {
				cancel()
				r.authorizationMu.Unlock()
				return approval.Response{}, err
			}
			cancel()
			work = continuation.ctx
			cancel = func() { continuation.cancel(context.Canceled) }
			finish = func() { scope.complete(id, continuation, nil) }
		}
		flight = &authorizationFlight{scope: a.Scope, cancel: cancel, done: make(chan struct{})}
		if r.authorizationFlights == nil {
			r.authorizationFlights = map[string]*authorizationFlight{}
		}
		r.authorizationFlights[key] = flight
		go func() {
			flight.response, flight.err = t.resolveApproval(work, request)
			if finish != nil {
				finish()
			}
			close(flight.done)
		}()

	}
	flight.waiters++
	r.authorizationMu.Unlock()
	defer func() {
		r.authorizationMu.Lock()
		defer r.authorizationMu.Unlock()
		flight.waiters--
		if flight.waiters == 0 {
			flight.cancel()
			if r.authorizationFlights[key] == flight {
				delete(r.authorizationFlights, key)
			}
		}
	}()
	select {
	case <-ctx.Done():
		return approval.Response{}, ctx.Err()
	case <-flight.done:
		if err := ctx.Err(); err != nil {
			return approval.Response{}, err
		}
		return flight.response, flight.err
	}
}

func (p *policyDispatchPermit) consume(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.used {
		return errors.New("policy dispatch permission was already consumed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r := p.runtime
	if r != nil {
		r.authorizationMu.Lock()
		defer r.authorizationMu.Unlock()
		if _, err := r.checkAuthorization(ctx, p.session, p.decision.Authorization); err != nil {
			return err
		}
	}
	if p.decision.Validate != nil {
		release, err := p.decision.Validate(ctx, p.optionID)
		if err != nil {
			return err
		}
		if release != nil {
			defer release()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.used = true
	return nil
}

func consumePolicyDispatch(ctx context.Context) error {
	permit, _ := ctx.Value(policyDispatchKey{}).(*policyDispatchPermit)
	if permit == nil {
		return nil
	}
	return permit.consume(ctx)
}
