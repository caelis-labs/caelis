package collaboration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// UserInput is a Control-authorized prompt bound to an immutable child instance.
// This queue is distinct from Agent mail: ReceiveMessages cannot consume a
// human prompt or convert its source into an Agent identity.
type UserInput struct {
	ID             string              `json:"id"`
	UserID         string              `json:"-"`
	SessionID      string              `json:"session_id"`
	ParticipantID  string              `json:"participant_id"`
	TaskID         string              `json:"task_id"`
	ChildSessionID string              `json:"child_session_id"`
	Generation     string              `json:"generation"`
	Text           string              `json:"text"`
	ContentParts   []model.ContentPart `json:"content_parts,omitempty"`
}

// UserInputStatus distinguishes queued input from proven admission and an
// uncertain dispatch. Sent means accepted by the endpoint, not applied by a model.
type UserInputStatus struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

// UserInputBackend dispatches through the existing delegated-child execution
// owner after rechecking the exact participant instance. It never waits for
// the resulting Turn to finish.
type UserInputBackend interface {
	DeliverUserInput(context.Context, UserInput) error
}

func prepareUserInputStore(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS collaboration_user_inputs (
	 seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, user_id TEXT NOT NULL,
	 session TEXT NOT NULL, participant TEXT NOT NULL, body TEXT NOT NULL,
	 state TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '', updated INTEGER NOT NULL
	); CREATE INDEX IF NOT EXISTS collaboration_user_input_pending ON collaboration_user_inputs(state,seq);
	UPDATE collaboration_user_inputs SET state='unknown', detail='Host restarted during delivery; input was not retried' WHERE state='sending'`)
	return err
}

// EnqueueUserInput is called only after product-principal authorization. The
// caller's operation ID makes transport retries idempotent. Target identity is
// captured before enqueue and verified again by the execution owner.
func (s *Service) EnqueueUserInput(ctx context.Context, id, userID, sessionID, participantID, taskID, text string, parts []model.ContentPart) (UserInputStatus, error) {
	if _, ok := s.backend.(UserInputBackend); !ok {
		return UserInputStatus{}, errors.New("user input is unavailable")
	}
	text = strings.TrimSpace(text)
	textBytes := 0
	for _, part := range parts {
		textBytes += len(part.Text)
	}
	if len(parts) == 0 {
		parts = nil
	}
	if id == "" || userID == "" || sessionID == "" || participantID == "" || taskID == "" || (text == "" && len(parts) == 0) || max(len(text), textBytes) > 65536 {
		return UserInputStatus{}, errorcode.New(errorcode.InvalidArgument, "User input requires an exact child, text or images, and at most 65536 text bytes")
	}
	if status, found, err := s.existingUserInput(ctx, id, userID, sessionID, participantID, taskID, text, parts); found || err != nil {
		return status, err
	}
	threads, err := s.backend.List(ctx, sessionID)
	if err != nil {
		return UserInputStatus{}, err
	}
	var target Thread
	for _, t := range threads {
		if t.ID == taskID && t.ParticipantID == participantID {
			target = t
			break
		}
	}
	if target.ID == "" || target.SessionID == "" {
		return UserInputStatus{}, errorcode.New(errorcode.Conflict, "The selected child is no longer attached")
	}
	if target.State == "unknown_outcome" {
		return UserInputStatus{}, errorcode.New(errorcode.UnknownOutcome, "Child execution is unresolved")
	}
	input := UserInput{ID: id, UserID: userID, SessionID: sessionID, ParticipantID: participantID, TaskID: taskID, ChildSessionID: target.SessionID, Generation: target.Generation, Text: text, ContentParts: parts}
	body, err := json.Marshal(input)
	if err != nil {
		return UserInputStatus{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO collaboration_user_inputs(id,user_id,session,participant,body,state,updated) VALUES(?,?,?,?,?,'queued',?) ON CONFLICT(id) DO NOTHING`, id, userID, sessionID, participantID, string(body), s.now().Unix())
	if err != nil {
		return UserInputStatus{}, err
	}
	status, _, err := s.existingUserInput(ctx, id, userID, sessionID, participantID, taskID, text, parts)
	return status, err
}

// UserInputStatuses returns receipts only for the authorized user and Session.
func (s *Service) UserInputStatuses(ctx context.Context, userID, sessionID string, ids []string) ([]UserInputStatus, error) {
	if len(ids) > 64 {
		return nil, errorcode.New(errorcode.InvalidArgument, "Too many input receipts")
	}
	statuses := make([]UserInputStatus, 0, len(ids))
	for _, id := range ids {
		status := UserInputStatus{ID: id}
		err := s.db.QueryRowContext(ctx, `SELECT state,detail FROM collaboration_user_inputs WHERE id=? AND user_id=? AND session=?`, id, userID, sessionID).Scan(&status.State, &status.Detail)
		if errors.Is(err, sql.ErrNoRows) {
			status.State = "unknown"
			status.Detail = "Input receipt is unavailable"
		} else if err != nil {
			return nil, err
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func (s *Service) runUserInputs(ctx context.Context, report func(error)) {
	backend, ok := s.backend.(UserInputBackend)
	if !ok {
		return
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var workers sync.WaitGroup
	defer workers.Wait()
	active := map[string]bool{}
	finished := make(chan string, 64)
	for {
		select {
		case <-ctx.Done():
			return
		case key := <-finished:
			delete(active, key)
		case <-ticker.C:
			inputs, err := s.pendingUserInputs(ctx)
			if err != nil {
				if report != nil {
					report(err)
				}
				continue
			}
			for _, input := range inputs {
				key := input.SessionID + ":" + input.ParticipantID
				if active[key] {
					continue
				}
				active[key] = true
				workers.Add(1)
				go func() {
					defer workers.Done()
					attempt, cancel := context.WithTimeout(ctx, deliveryTimeout)
					defer cancel()
					if err := s.deliverUserInput(attempt, backend, input); err != nil && report != nil {
						report(err)
					}
					select {
					case finished <- key:
					case <-ctx.Done():
					}
				}()
			}
		}
	}
}

func (s *Service) pendingUserInputs(ctx context.Context) ([]UserInput, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT body,user_id FROM collaboration_user_inputs WHERE seq IN (SELECT MIN(seq) FROM collaboration_user_inputs WHERE state='queued' GROUP BY session,participant) ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var inputs []UserInput
	for rows.Next() {
		var body, userID string
		if err := rows.Scan(&body, &userID); err != nil {
			return nil, err
		}
		var input UserInput
		if err := json.Unmarshal([]byte(body), &input); err != nil {
			return nil, err
		}
		input.UserID = userID
		inputs = append(inputs, input)
	}
	return inputs, rows.Err()
}

func (s *Service) deliverUserInput(ctx context.Context, backend UserInputBackend, input UserInput) error {
	threads, err := s.backend.List(ctx, input.SessionID)
	if errors.Is(err, ErrSessionClosed) {
		return s.finishUserInput(ctx, input.ID, "failed", "Session closed before delivery")
	}
	if err != nil {
		return err
	}
	var target *Thread
	for i := range threads {
		t := &threads[i]
		if t.ID == input.TaskID && t.ParticipantID == input.ParticipantID && t.SessionID == input.ChildSessionID && t.Generation == input.Generation {
			target = t
			break
		}
	}
	if target == nil {
		return s.finishUserInput(ctx, input.ID, "failed", "Child detached before delivery")
	}
	if target.State == "unknown_outcome" {
		return s.finishUserInput(ctx, input.ID, "unknown", "Child execution is unresolved; input was not sent")
	}
	if !target.CanDeliver {
		return nil
	}
	result, err := s.db.ExecContext(ctx, `UPDATE collaboration_user_inputs SET state='sending',updated=? WHERE id=? AND state='queued'`, s.now().Unix(), input.ID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return err
	}
	err = backend.DeliverUserInput(ctx, input)
	if errors.Is(err, agent.ErrChildInputNotReady) {
		return s.finishUserInput(ctx, input.ID, "queued", "")
	}
	state, detail := "sent", ""
	if err != nil {
		state, detail = "unknown", "Delivery could not be confirmed; input was not retried"
		switch errorcode.CodeOf(err) {
		case errorcode.InvalidArgument, errorcode.PermissionDenied, errorcode.NotFound, errorcode.Conflict, errorcode.Unsupported, errorcode.FailedPrecondition:
			state, detail = "failed", err.Error()
		}
	}
	return s.finishUserInput(ctx, input.ID, state, detail)
}

func (s *Service) finishUserInput(ctx context.Context, id, state, detail string) error {
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(finish, `UPDATE collaboration_user_inputs SET state=?,detail=?,updated=? WHERE id=?`, state, detail, s.now().Unix(), id)
	return err
}

func (s *Service) existingUserInput(ctx context.Context, id, userID, sessionID, participantID, taskID, text string, parts []model.ContentPart) (UserInputStatus, bool, error) {
	var body, storedUser string
	status := UserInputStatus{ID: id}
	err := s.db.QueryRowContext(ctx, `SELECT body,user_id,state,detail FROM collaboration_user_inputs WHERE id=?`, id).Scan(&body, &storedUser, &status.State, &status.Detail)
	if errors.Is(err, sql.ErrNoRows) {
		return UserInputStatus{}, false, nil
	}
	if err != nil {
		return UserInputStatus{}, false, err
	}
	var input UserInput
	if err = json.Unmarshal([]byte(body), &input); err != nil {
		return UserInputStatus{}, true, err
	}
	if storedUser != userID || input.SessionID != sessionID || input.ParticipantID != participantID || input.TaskID != taskID || input.Text != text || !reflect.DeepEqual(input.ContentParts, parts) {
		return UserInputStatus{}, true, errorcode.New(errorcode.Conflict, "Input operation was reused with different content")
	}
	return status, true, nil
}
