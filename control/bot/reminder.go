package bot

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

// ReminderGrant retains the real request behind a native resident schedule.
// Desktop owns scheduling; this record governs what a scheduled wake may do.
type ReminderGrant struct {
	ID             string           `json:"id"`
	Version        string           `json:"version"`
	ClientID       string           `json:"client_id"`
	PrincipalID    string           `json:"principal_id"`
	BotID          string           `json:"bot_id"`
	SourceID       string           `json:"source_id"`
	Arguments      DesktopArguments `json:"arguments"`
	Active         bool             `json:"active"`
	CreatedAt      time.Time        `json:"created_at"`
	LastOccurrence time.Time        `json:"last_occurrence"`
}

// ReminderFire is an accepted occurrence, distinct from an LLM execution.
// A claimed occurrence is never automatically replayed after uncertain dispatch.
type ReminderFire struct {
	ID          string    `json:"id"`
	GrantID     string    `json:"grant_id"`
	Version     string    `json:"version"`
	PrincipalID string    `json:"principal_id"`
	BotID       string    `json:"bot_id"`
	ClientID    string    `json:"client_id"`
	SourceID    string    `json:"source_id"`
	Due         time.Time `json:"due"`
	State       string    `json:"state"`
	Execution   Execution `json:"execution"`
}

func reminderGrantID(clientID, nativeID string) string {
	return opaqueID("bot-reminder-", clientID, nativeID)
}

// completeReminder atomically commits the native receipt and schedule grant.
// Failed receipts never authorize a schedule. Replayed receipts cannot replace
// a later grant because this transaction only accepts the exact claimed record.
func (s *WorkStore) completeReminder(ctx context.Context, previous, next desktopRecord, client Client) (bool, error) {
	before, err := json.Marshal(previous)
	if err != nil {
		return false, err
	}
	after, err := json.Marshal(next)
	if err != nil {
		return false, err
	}
	tx, err := s.db.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE bot_authority SET body=? WHERE kind='desktop' AND id=? AND body=? AND EXISTS (SELECT 1 FROM bot_authority c WHERE c.kind='client' AND c.id=? AND json_extract(c.body,'$.client.active')=1 AND json_extract(c.body,'$.client.activation_id')=? AND json_extract(c.body,'$.client.instance_id')=? AND julianday(json_extract(c.body,'$.client.expires_at'))>julianday('now'))`, string(after), next.Call.ID, string(before), client.ID, client.ActivationID, client.InstanceID)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return false, err
	}
	call := next.Call
	if !call.IsError && (call.Arguments.Operation == "save" || call.Arguments.Operation == "remove") {
		grant := ReminderGrant{ID: reminderGrantID(call.ClientID, call.Arguments.ID), Version: call.ID, ClientID: call.ClientID, PrincipalID: call.PrincipalID, BotID: call.BotID, SourceID: call.SourceID, Arguments: call.Arguments, Active: call.Arguments.Operation == "save", CreatedAt: time.Now().UTC()}
		data, err := json.Marshal(grant)
		if err != nil {
			return false, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO bot_authority(kind,id,body) VALUES('reminder',?,?) ON CONFLICT(kind,id) DO UPDATE SET body=excluded.body`, grant.ID, string(data)); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

// Reminders lists grants of the exact activated client for scheduler recovery.
func (s *WorkStore) Reminders(ctx context.Context, client Client) ([]ReminderGrant, error) {
	if _, err := s.ActiveClient(ctx, client.PrincipalID, client.BotID, client.ID); err != nil {
		return nil, err
	}
	rows, err := s.db.List(ctx, "reminder")
	if err != nil {
		return nil, err
	}
	out := []ReminderGrant{}
	for _, row := range rows {
		var grant ReminderGrant
		if err := json.Unmarshal(row, &grant); err != nil {
			return nil, err
		}
		if grant.ClientID == client.ID && grant.PrincipalID == client.PrincipalID && grant.BotID == client.BotID {
			out = append(out, grant)
		}
	}
	return out, nil
}

// ReminderOccurrences returns persisted receipts, including uncertain claims,
// so a native scheduler can recover without sending another fire request.
func (s *WorkStore) ReminderOccurrences(ctx context.Context, client Client) ([]ReminderFire, error) {
	if _, err := s.ActiveClient(ctx, client.PrincipalID, client.BotID, client.ID); err != nil {
		return nil, err
	}
	rows, err := s.db.List(ctx, "reminder_fire")
	if err != nil {
		return nil, err
	}
	out := []ReminderFire{}
	for _, row := range rows {
		var fire ReminderFire
		if err := json.Unmarshal(row, &fire); err != nil {
			return nil, err
		}
		if fire.ClientID == client.ID && fire.PrincipalID == client.PrincipalID && fire.BotID == client.BotID {
			out = append(out, fire)
		}
	}
	return out, nil
}

// QueueReminderOccurrence validates one scheduler notification without accepting
// any caller-supplied prompt or assignment. LastOccurrence coalesces missed
// schedules and is committed atomically with the durable pending occurrence.
func (s *WorkStore) QueueReminderOccurrence(ctx context.Context, client Client, grantID, version string, due time.Time) (ReminderFire, error) {
	client, err := s.ActiveClient(ctx, client.PrincipalID, client.BotID, client.ID)
	if err != nil {
		return ReminderFire{}, err
	}
	id := opaqueID("bot-reminder-fire-", grantID, version, due.UTC().Format(time.RFC3339Nano))
	var prior ReminderFire
	if err := s.db.Get(ctx, "reminder_fire", id, &prior); err == nil {
		if prior.ClientID != client.ID {
			return ReminderFire{}, errorcode.New(errorcode.PermissionDenied, "bot: occurrence is not owned")
		}
		return prior, nil
	} else if errorcode.CodeOf(err) != errorcode.NotFound {
		return ReminderFire{}, err
	}
	var grant ReminderGrant
	if err := s.db.Get(ctx, "reminder", grantID, &grant); err != nil {
		return ReminderFire{}, err
	}
	if !grant.Active || grant.Version != version || grant.ClientID != client.ID || grant.BotID != client.BotID || grant.PrincipalID != client.PrincipalID {
		return ReminderFire{}, errorcode.New(errorcode.PermissionDenied, "bot: reminder grant is not active")
	}
	now := time.Now().UTC()
	due = due.UTC()
	if due.After(now) || due.Before(client.ActivatedAt) || !due.After(grant.LastOccurrence) {
		return ReminderFire{}, errorcode.New(errorcode.Conflict, "bot: occurrence is future, stopped, or already consumed")
	}
	switch {
	case grant.Arguments.At != "":
		at, _ := time.Parse(time.RFC3339, grant.Arguments.At)
		if !due.Equal(at) {
			return ReminderFire{}, errors.New("bot: one-off occurrence does not match schedule")
		}
	case grant.Arguments.EveryMinutes > 0:
		interval := time.Duration(grant.Arguments.EveryMinutes) * time.Minute
		earliest := grant.CreatedAt.Add(interval)
		if !grant.LastOccurrence.IsZero() {
			earliest = grant.LastOccurrence.Add(interval)
		}
		if due.Before(earliest.Add(-time.Second)) {
			return ReminderFire{}, errors.New("bot: occurrence precedes the authorized interval")
		}
	case grant.Arguments.Daily != "":
		zone, err := time.LoadLocation(grant.Arguments.TimeZone)
		if err != nil {
			return ReminderFire{}, err
		}
		if due.Second() != 0 || due.Nanosecond() != 0 || due.In(zone).Format("15:04") != grant.Arguments.Daily || (!grant.LastOccurrence.IsZero() && due.In(zone).Format("2006-01-02") == grant.LastOccurrence.In(zone).Format("2006-01-02")) {
			return ReminderFire{}, errors.New("bot: occurrence does not match daily schedule")
		}
	}
	fire := ReminderFire{ID: id, GrantID: grantID, Version: version, PrincipalID: client.PrincipalID, BotID: client.BotID, ClientID: client.ID, SourceID: grant.SourceID, Due: due, State: "pending"}
	// One native catch-up notification represents the missed interval. Advancing
	// to now prevents a burst of old occurrences from creating repeated turns.
	next := grant
	next.LastOccurrence = now
	before, _ := json.Marshal(grant)
	after, _ := json.Marshal(next)
	data, _ := json.Marshal(fire)
	tx, err := s.db.db.BeginTx(ctx, nil)
	if err != nil {
		return ReminderFire{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE bot_authority SET body=? WHERE kind='reminder' AND id=? AND body=?`, string(after), grantID, string(before))
	if err != nil {
		return ReminderFire{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return ReminderFire{}, err
	}
	if count != 1 {
		return ReminderFire{}, errorcode.New(errorcode.Conflict, "bot: reminder occurrence changed")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO bot_authority(kind,id,body) VALUES('reminder_fire',?,?)`, id, string(data)); err != nil {
		return ReminderFire{}, err
	}
	return fire, tx.Commit()
}

// PendingReminders exposes finite authorized pending activity to the Host.
func (s *WorkStore) PendingReminders(ctx context.Context, principalID, botID string) ([]ReminderFire, error) {
	rows, err := s.db.List(ctx, "reminder_fire")
	if err != nil {
		return nil, err
	}
	out := []ReminderFire{}
	for _, row := range rows {
		var fire ReminderFire
		if err := json.Unmarshal(row, &fire); err != nil {
			return nil, err
		}
		if fire.PrincipalID == principalID && fire.BotID == botID && fire.State == "pending" {
			out = append(out, fire)
		}
	}
	return out, nil
}

// ClaimReminder fences dispatch and returns its stored authorized assignment.
func (s *WorkStore) ClaimReminder(ctx context.Context, fire ReminderFire) (ReminderGrant, bool, error) {
	client, err := s.ActiveClient(ctx, fire.PrincipalID, fire.BotID, fire.ClientID)
	if err != nil {
		return ReminderGrant{}, false, err
	}
	if fire.Due.Before(client.ActivatedAt) {
		next := fire
		next.State = "suppressed"
		_, err := s.db.compareActive(ctx, "reminder_fire", fire.ID, fire, next, client)
		return ReminderGrant{}, false, err
	}
	var grant ReminderGrant
	if err := s.db.Get(ctx, "reminder", fire.GrantID, &grant); err != nil {
		return grant, false, err
	}
	if !grant.Active || grant.Version != fire.Version {
		return grant, false, errorcode.New(errorcode.PermissionDenied, "bot: reminder grant was revoked")
	}
	next := fire
	next.State = "claimed"
	ok, err := s.db.compareActive(ctx, "reminder_fire", fire.ID, fire, next, client)
	return grant, ok, err
}

// AdmitReminder stores an exact scheduled execution target, without replay.
func (s *WorkStore) AdmitReminder(ctx context.Context, fire ReminderFire, target Execution) error {
	var previous ReminderFire
	if err := s.db.Get(ctx, "reminder_fire", fire.ID, &previous); err != nil {
		return err
	}
	if previous.State != "claimed" {
		return errors.New("bot: occurrence is not claimed")
	}
	next := previous
	next.State = "admitted"
	next.Execution = target
	changed, err := s.db.compare(ctx, "reminder_fire", fire.ID, previous, next)
	if err == nil && !changed {
		return errorcode.New(errorcode.Conflict, "bot: occurrence admission changed")
	}
	return err
}

// ReminderRequest binds a claimed occurrence to its original durable request.
// Reminder assignment text never becomes a new user request.
func (s *WorkStore) ReminderRequest(ctx context.Context, grant ReminderGrant, fire ReminderFire) (RequestSource, error) {
	var stored ReminderFire
	if err := s.db.Get(ctx, "reminder_fire", fire.ID, &stored); err != nil {
		return RequestSource{}, err
	}
	if stored.State != "claimed" || stored.GrantID != grant.ID || stored.Version != grant.Version {
		return RequestSource{}, errorcode.New(errorcode.PermissionDenied, "bot: reminder occurrence is not claimed")
	}
	original, err := s.Request(ctx, grant.PrincipalID, grant.BotID, grant.SourceID)
	if err != nil {
		return RequestSource{}, err
	}
	source, err := s.RecordRequest(ctx, grant.PrincipalID, grant.BotID, grant.ClientID, fire.ID, fire.Version, original.Text, original.ContentParts...)
	if err != nil {
		return source, err
	}
	source.Kind = "reminder"
	source.OriginalRequestID = original.ID
	if err := s.db.Replace(ctx, "source", source.ID, source); err != nil {
		return RequestSource{}, err
	}
	return source, nil
}
