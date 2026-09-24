package controlserver

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	"github.com/caelis-labs/caelis/control/modelconfig"
)

// These exchanges only bridge live HTTP presentation to the existing Control
// authentication callbacks. Configuration and durable outcome remain owned by
// ConnectModel. Disconnect cancels this interactive call; retry safety remains
// the command ledger's responsibility, never an in-memory success inference.
type modelAuthKey struct{ principal, operation string }
type modelAuthInput struct {
	id     string
	values chan string
	ctx    context.Context
}
type modelAuthExchange struct {
	mu       sync.Mutex
	snapshot appserver.ModelAuthenticationSnapshot
	changed  chan struct{}
	input    *modelAuthInput
}

func (x *modelAuthExchange) update(change func(*appserver.ModelAuthenticationSnapshot)) {
	x.mu.Lock()
	defer x.mu.Unlock()
	change(&x.snapshot)
	x.publishLocked()
}
func (x *modelAuthExchange) publishLocked() {
	x.snapshot.Sequence++
	select {
	case x.changed <- struct{}{}:
	default:
	}
}
func (x *modelAuthExchange) read() appserver.ModelAuthenticationSnapshot {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.snapshot
}
func (x *modelAuthExchange) requestInput(ctx context.Context, request modelconfig.AuthInputRequest) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	input := &modelAuthInput{id: rand.Text(), values: make(chan string, 1), ctx: ctx}
	x.mu.Lock()
	if x.input != nil || x.snapshot.Result != nil {
		x.mu.Unlock()
		return "", fmt.Errorf("authentication input is already pending or finished")
	}
	x.input = input
	x.snapshot.ChallengeID = input.id
	x.snapshot.Prompt = request.Prompt
	x.publishLocked()
	x.mu.Unlock()
	defer func() {
		x.mu.Lock()
		defer x.mu.Unlock()
		// A submitted input may already have yielded to a new challenge.
		// Only the exact outstanding challenge owns its cleanup.
		if x.input == input {
			x.input = nil
			x.snapshot.ChallengeID = ""
			x.snapshot.Prompt = ""
			x.publishLocked()
		}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case value := <-input.values:
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return value, nil
	}
}
func (x *modelAuthExchange) submit(input appserver.ModelAuthenticationInput) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.input == nil || x.input.ctx.Err() != nil || x.snapshot.Result != nil || input.ChallengeID != x.input.id {
		return false
	}
	x.input.values <- input.Input
	x.input = nil
	x.snapshot.ChallengeID = ""
	x.snapshot.Prompt = ""
	x.publishLocked()
	return true
}
func (s *Server) modelAuthenticationInput(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	if err := (appserver.ProductCommandAuthorizer{}).Authorize(r.Context(), principal, appserver.ActionModelConnect, ""); err != nil {
		writeMappedError(w, err)
		return
	}
	// Keep this secret-bearing request smaller than ordinary prompt bodies.
	contentType := strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])
	if !strings.EqualFold(contentType, "application/json") {
		writeError(w, http.StatusBadRequest, "Content-Type must be application/json")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
	var input appserver.ModelAuthenticationInput
	if err != nil || wirev1.DecodeRequest(body, &input) != nil {
		// Decoder errors may include unknown field names supplied in this
		// secret-bearing body. Never echo those names or decoder text.
		writeError(w, http.StatusBadRequest, "invalid authentication response")
		return
	}
	if input.ChallengeID == "" || strings.TrimSpace(input.Input) == "" || len(input.Input) > 16384 {
		writeError(w, http.StatusBadRequest, "invalid authentication response")
		return
	}
	s.authMu.Lock()
	exchange := s.authExchanges[modelAuthKey{principal.ID, r.PathValue("operation_id")}]
	s.authMu.Unlock()
	if exchange == nil || !exchange.submit(input) {
		writeError(w, http.StatusConflict, "authentication challenge is no longer active")
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}
func (s *Server) connectModelStream(w http.ResponseWriter, r *http.Request, principal appserver.Principal, request appserver.ConnectModelRequest) {
	if err := (appserver.ProductCommandAuthorizer{}).Authorize(r.Context(), principal, appserver.ActionModelConnect, ""); err != nil {
		writeMappedError(w, err)
		return
	}
	if request.OperationID == "" {
		writeError(w, http.StatusBadRequest, "operation_id is required")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	exchange := &modelAuthExchange{snapshot: appserver.ModelAuthenticationSnapshot{OperationID: request.OperationID, Phase: "starting"}, changed: make(chan struct{}, 1)}
	key := modelAuthKey{principal.ID, request.OperationID}
	s.authMu.Lock()
	if s.authExchanges == nil {
		s.authExchanges = map[modelAuthKey]*modelAuthExchange{}
	}
	if s.authExchanges[key] != nil {
		s.authMu.Unlock()
		writeError(w, http.StatusConflict, "operation already has an authentication stream")
		return
	}
	s.authExchanges[key] = exchange
	s.authMu.Unlock()
	defer func() { s.authMu.Lock(); delete(s.authExchanges, key); s.authMu.Unlock() }()
	ctx = modelconfig.WithAuthProgress(ctx, func(progress modelconfig.AuthProgress) {
		exchange.update(func(snapshot *appserver.ModelAuthenticationSnapshot) {
			snapshot.Phase = string(progress.Phase)
			snapshot.VerificationURL = progress.VerificationURL
			snapshot.UserCode = progress.UserCode
		})
	})
	ctx = modelconfig.WithAuthInput(ctx, exchange.requestInput)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	exchange.update(func(*appserver.ModelAuthenticationSnapshot) {})
	go func() {
		result, err := s.config.Services.Configuration.ConnectModel(ctx, principal, request)
		if result.OperationID == "" {
			result.OperationID = request.OperationID
		}
		if result.Outcome == "" {
			result.Outcome = appserver.OutcomeUnknown
		}
		// Never copy provider errors or credentials into progress. Typed outcome and
		// error identity are sufficient to reconcile or correct the operation.
		result.Detail = ""
		if err != nil && result.ErrorKind == "" {
			result.ErrorKind = appserver.ErrorKindOf(err)
		}
		exchange.update(func(snapshot *appserver.ModelAuthenticationSnapshot) {
			snapshot.Phase = "finished"
			snapshot.ChallengeID = ""
			snapshot.Prompt = ""
			snapshot.VerificationURL = ""
			snapshot.UserCode = ""
			snapshot.Result = &result
		})
	}()
	ticker := time.NewTicker(s.config.Heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-exchange.changed:
			snapshot := exchange.read()
			body, err := wirev1.Marshal(snapshot)
			if err != nil {
				return
			}
			if _, err = fmt.Fprintf(w, "event: model_authentication\ndata: %s\n\n", body); err != nil {
				return
			}
			flusher.Flush()
			if snapshot.Result != nil {
				return
			}
		}
	}
}
