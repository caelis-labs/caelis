package bot

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

// DesktopArguments is the complete fixed action vocabulary. It intentionally
// has no command, executable, environment, URL or arbitrary tool schema field.
type DesktopArguments struct {
	Operation    string `json:"operation,omitempty"`
	ID           string `json:"id,omitempty"`
	Label        string `json:"label,omitempty"`
	Prompt       string `json:"prompt,omitempty"`
	At           string `json:"at,omitempty"`
	EveryMinutes int    `json:"every_minutes,omitempty"`
	Daily        string `json:"daily,omitempty"`
	TimeZone     string `json:"time_zone,omitempty"`
	Gesture      string `json:"gesture,omitempty"`
}

// DesktopCall is a durable action request. Observation is not permission to
// execute: only a successful one-time claim returns a dispatch credential.
type DesktopCall struct {
	ID           string           `json:"id"`
	ClientID     string           `json:"client_id"`
	BotID        string           `json:"bot_id"`
	PrincipalID  string           `json:"principal_id"`
	ActivationID string           `json:"activation_id"`
	Execution    Execution        `json:"execution"`
	ItemID       string           `json:"item_id"`
	SourceID     string           `json:"source_id"`
	Action       string           `json:"action"`
	Arguments    DesktopArguments `json:"arguments"`
	State        string           `json:"state"`
	Result       json.RawMessage  `json:"result,omitempty"`
	IsError      bool             `json:"is_error,omitempty"`
}

type desktopRecord struct {
	Call      DesktopCall `json:"call"`
	ClaimHash string      `json:"claim_hash,omitempty"`
}

// DesktopClaim carries a one-time native dispatch credential, never model input.
type DesktopClaim struct {
	Call  DesktopCall `json:"call"`
	Token string      `json:"token"`
}

// DesktopReceipt targets the exact claimed action and activation. Result bytes
// are untrusted tool output and cannot authorize later actions.
type DesktopReceipt struct {
	Token   string          `json:"token"`
	Result  json.RawMessage `json:"result"`
	IsError bool            `json:"is_error,omitempty"`
}

// DesktopSnapshot is the pending action mailbox and its content cursor. An
// unchanged cursor needs no redispatch; individual receipts remain queryable.
type DesktopSnapshot struct {
	Cursor string        `json:"cursor"`
	Calls  []DesktopCall `json:"calls"`
}

// SnapshotDesktop projects the current activation without retaining completed
// result bodies in every event. The durable per-action record owns recovery.
func (s *WorkStore) SnapshotDesktop(ctx context.Context, client Client) (DesktopSnapshot, error) {
	calls, err := s.DesktopCalls(ctx, client)
	if err != nil {
		return DesktopSnapshot{}, err
	}
	data, err := json.Marshal(calls)
	if err != nil {
		return DesktopSnapshot{}, err
	}
	sum := sha256.Sum256(data)
	return DesktopSnapshot{Cursor: client.ActivationID + "." + hex.EncodeToString(sum[:]), Calls: calls}, nil
}

// GetDesktopCall reads one owned action, including previous activations. It
// does not grant dispatch or authorize a late receipt against a new activation.
func (s *WorkStore) GetDesktopCall(ctx context.Context, client Client, id string) (DesktopCall, error) {
	if _, err := s.ActiveClient(ctx, client.PrincipalID, client.BotID, client.ID); err != nil {
		return DesktopCall{}, err
	}
	var record desktopRecord
	if err := s.db.Get(ctx, "desktop", id, &record); err != nil {
		return DesktopCall{}, err
	}
	if record.Call.ClientID != client.ID || record.Call.BotID != client.BotID || record.Call.PrincipalID != client.PrincipalID {
		return DesktopCall{}, errorcode.New(errorcode.PermissionDenied, "bot: action is not owned")
	}
	return record.Call, nil
}

type desktopSignal struct {
	mu      sync.Mutex
	changed chan struct{}
}

func (s *desktopSignal) wait() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.changed == nil {
		s.changed = make(chan struct{})
	}
	return s.changed
}
func (s *desktopSignal) wake() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.changed != nil {
		close(s.changed)
	}
	s.changed = make(chan struct{})
}

func validateDesktopArguments(action string, args DesktopArguments) error {
	switch action {
	case "clock":
		if args != (DesktopArguments{}) {
			return errorcode.New(errorcode.InvalidArgument, "bot: clock takes no arguments")
		}
	case "gesture":
		if args != (DesktopArguments{Gesture: args.Gesture}) || !slices.Contains([]string{"attention", "nod", "celebrate"}, args.Gesture) {
			return errorcode.New(errorcode.InvalidArgument, "bot: unsupported gesture")
		}
	case "reminders":
		if args.Gesture != "" {
			return errorcode.New(errorcode.InvalidArgument, "bot: gesture is not a reminder argument")
		}
		switch args.Operation {
		case "list":
			if args != (DesktopArguments{Operation: "list"}) {
				return errorcode.New(errorcode.InvalidArgument, "bot: reminder list takes no other arguments")
			}
		case "remove":
			if args != (DesktopArguments{Operation: "remove", ID: args.ID}) || !validReminderID(args.ID) {
				return errorcode.New(errorcode.InvalidArgument, "bot: reminder removal requires only an id")
			}
		case "save":
			if !validReminderID(args.ID) || strings.TrimSpace(args.Label) == "" || strings.TrimSpace(args.Prompt) == "" || len(args.Prompt) > 65536 {
				return errorcode.New(errorcode.InvalidArgument, "bot: reminder id, label and prompt are required")
			}
			schedules := 0
			if args.At != "" {
				schedules++
				if _, err := time.Parse(time.RFC3339, args.At); err != nil {
					return err
				}
			}
			if args.EveryMinutes != 0 {
				schedules++
				if args.EveryMinutes < 1 || args.EveryMinutes > 10080 {
					return errorcode.New(errorcode.InvalidArgument, "bot: reminder interval is outside 1–10080 minutes")
				}
			}
			if args.Daily != "" {
				schedules++
				if _, err := time.Parse("15:04", args.Daily); err != nil {
					return err
				}
				if args.TimeZone == "" {
					return errors.New("bot: daily reminder requires a time zone")
				}
			}
			if schedules != 1 {
				return errorcode.New(errorcode.InvalidArgument, "bot: reminder requires exactly one schedule")
			}
			if args.TimeZone != "" {
				if _, err := time.LoadLocation(args.TimeZone); err != nil {
					return err
				}
			}
		default:
			return errorcode.New(errorcode.InvalidArgument, "bot: unsupported reminder operation")
		}
	default:
		return errorcode.New(errorcode.Unsupported, "bot: unsupported desktop action")
	}
	return nil
}
func validReminderID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

// QueueDesktop binds a fixed routine action to current authenticated user input.
// Its content-derived ID prevents a new tool-call ID from duplicating an effect.
func (s *WorkStore) QueueDesktop(ctx context.Context, source RequestSource, itemID, action string, args DesktopArguments) (DesktopCall, error) {
	if err := validateDesktopArguments(action, args); err != nil {
		return DesktopCall{}, err
	}
	source, err := s.Request(ctx, source.PrincipalID, source.BotID, source.ID)
	if err != nil {
		return DesktopCall{}, err
	}
	if source.Execution.TurnID == "" {
		return DesktopCall{}, errorcode.New(errorcode.FailedPrecondition, "bot: source execution is unknown")
	}
	if action == "reminders" && args.Operation != "list" && source.Kind != "user_request" {
		return DesktopCall{}, errorcode.New(errorcode.PermissionDenied, "bot: reminder changes require a direct user request")
	}
	client, err := s.ActiveClient(ctx, source.PrincipalID, source.BotID, source.ClientID)
	if err != nil {
		return DesktopCall{}, err
	}
	if !slices.Contains(client.Actions, action) {
		return DesktopCall{}, errorcode.New(errorcode.PermissionDenied, "bot: desktop action is not approved")
	}
	data, _ := json.Marshal(args)
	id := opaqueID("bot-action-", source.ID, action, string(data))
	call := DesktopCall{ID: id, ClientID: client.ID, BotID: source.BotID, PrincipalID: source.PrincipalID, ActivationID: client.ActivationID, Execution: source.Execution, ItemID: itemID, SourceID: source.ID, Action: action, Arguments: args, State: "queued"}
	fresh, err := s.queueDesktop(ctx, client, call)
	if err != nil {
		return DesktopCall{}, err
	}
	if !fresh {
		var previous desktopRecord
		if err := s.db.Get(ctx, "desktop", id, &previous); err != nil {
			if errorcode.CodeOf(err) == errorcode.NotFound {
				return DesktopCall{}, errorcode.New(errorcode.Conflict, "bot: client activation changed before queuing action")
			}
			return DesktopCall{}, err
		}
		return previous.Call, nil
	}
	s.desktop.wake()
	return call, nil
}

// queueDesktop cannot insert an old activation's action after exit or
// replacement has suppressed its mailbox and released pending reminder IDs.
func (s *WorkStore) queueDesktop(ctx context.Context, client Client, call DesktopCall) (bool, error) {
	data, err := json.Marshal(desktopRecord{Call: call})
	if err != nil {
		return false, err
	}
	result, err := s.db.db.ExecContext(ctx, `INSERT INTO bot_authority(kind,id,body)
 SELECT 'desktop',?,? WHERE EXISTS (`+activeClientSQL+`) ON CONFLICT(kind,id) DO NOTHING`, call.ID, string(data), client.ID, client.PrincipalID, client.BotID, client.ActivationID, client.InstanceID)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

// DesktopCalls returns the client's action mailbox. It includes claimed calls
// after reconnection so native code can reconcile receipts without dispatching.
func (s *WorkStore) DesktopCalls(ctx context.Context, client Client) ([]DesktopCall, error) {
	current, err := s.ActiveClient(ctx, client.PrincipalID, client.BotID, client.ID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.List(ctx, "desktop")
	if err != nil {
		return nil, err
	}
	out := []DesktopCall{}
	for _, row := range rows {
		var value desktopRecord
		if err := json.Unmarshal(row, &value); err != nil {
			return nil, err
		}
		if value.Call.ClientID == current.ID && value.Call.ActivationID == current.ActivationID && (value.Call.State == "queued" || value.Call.State == "claimed") {
			out = append(out, value.Call)
		}
	}
	return out, nil
}

// DesktopChanged is a transport notification only; callers resnapshot after
// observing it and on reconnect. No model is awakened by an empty mailbox.
func (s *WorkStore) DesktopChanged() <-chan struct{} { return s.desktop.wait() }

// ClaimDesktop authorizes one native dispatch. Retrying a claimed request never
// reissues the token; an unknown claim reply requires native receipt recovery.
func (s *WorkStore) ClaimDesktop(ctx context.Context, client Client, id string) (DesktopClaim, error) {
	current, err := s.ActiveClient(ctx, client.PrincipalID, client.BotID, client.ID)
	if err != nil {
		return DesktopClaim{}, err
	}
	var value desktopRecord
	if err := s.db.Get(ctx, "desktop", id, &value); err != nil {
		return DesktopClaim{}, err
	}
	if value.Call.ClientID != current.ID || value.Call.ActivationID != current.ActivationID {
		return DesktopClaim{}, errorcode.New(errorcode.PermissionDenied, "bot: action activation does not match")
	}
	if value.Call.State != "queued" {
		return DesktopClaim{}, errorcode.New(errorcode.UnknownOutcome, "bot: action already claimed; recover the native receipt without dispatching again")
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return DesktopClaim{}, err
	}
	token := hex.EncodeToString(nonce[:])
	sum := sha256.Sum256([]byte(token))
	next := value
	next.Call.State = "claimed"
	next.ClaimHash = hex.EncodeToString(sum[:])
	changed, err := s.db.compareActive(ctx, "desktop", id, value, next, current)
	if err != nil {
		return DesktopClaim{}, err
	}
	if !changed {
		return DesktopClaim{}, errorcode.New(errorcode.Conflict, "bot: action claim changed")
	}
	s.desktop.wake()
	return DesktopClaim{Call: next.Call, Token: token}, nil
}

// CompleteDesktop accepts only the current exact native claim. Repeated equal
// receipts are idempotent; conflicting or expired activation receipts fail closed.
func (s *WorkStore) CompleteDesktop(ctx context.Context, client Client, id string, receipt DesktopReceipt) (DesktopCall, error) {
	current, err := s.ActiveClient(ctx, client.PrincipalID, client.BotID, client.ID)
	if err != nil {
		return DesktopCall{}, err
	}
	if len(receipt.Result) > 65536 || !json.Valid(receipt.Result) {
		return DesktopCall{}, errorcode.New(errorcode.InvalidArgument, "bot: action receipt must be JSON of at most 64 KiB")
	}
	var value desktopRecord
	if err := s.db.Get(ctx, "desktop", id, &value); err != nil {
		return DesktopCall{}, err
	}
	sum := sha256.Sum256([]byte(receipt.Token))
	expected, _ := hex.DecodeString(value.ClaimHash)
	if value.Call.ClientID != current.ID || value.Call.ActivationID != current.ActivationID || subtle.ConstantTimeCompare(sum[:], expected) != 1 {
		return DesktopCall{}, errorcode.New(errorcode.PermissionDenied, "bot: action receipt does not match its claim")
	}
	if value.Call.State == "completed" {
		if string(value.Call.Result) != string(receipt.Result) || value.Call.IsError != receipt.IsError {
			return DesktopCall{}, errorcode.New(errorcode.Conflict, "bot: action receipt conflicts")
		}
		return value.Call, nil
	}
	if value.Call.State != "claimed" {
		return DesktopCall{}, errorcode.New(errorcode.Conflict, "bot: action is not awaiting a receipt")
	}
	next := value
	next.Call.State = "completed"
	next.Call.Result = append(json.RawMessage(nil), receipt.Result...)
	next.Call.IsError = receipt.IsError
	var changed bool
	if next.Call.Action == "reminders" {
		changed, err = s.completeReminder(ctx, value, next, current)
	} else {
		changed, err = s.db.compareActive(ctx, "desktop", id, value, next, current)
	}
	if err != nil {
		return DesktopCall{}, err
	}
	if !changed {
		return DesktopCall{}, errorcode.New(errorcode.Conflict, "bot: action receipt changed")
	}
	s.desktop.wake()
	return next.Call, nil
}

// AwaitDesktop waits for bounded native activity, never for idle model polling.
func (s *WorkStore) AwaitDesktop(ctx context.Context, id string) (DesktopCall, error) {
	for {
		changed := s.DesktopChanged()
		var value desktopRecord
		if err := s.db.Get(ctx, "desktop", id, &value); err != nil {
			return DesktopCall{}, err
		}
		if value.Call.State == "completed" || value.Call.State == "suppressed" {
			return value.Call, nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return value.Call, nil
		}
	}
}
