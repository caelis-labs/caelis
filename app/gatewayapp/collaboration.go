package gatewayapp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	acp "github.com/caelis-labs/acp-go-sdk"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/collaboration"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	"github.com/caelis-labs/caelis/internal/kernel"
)

type collaborationBackend struct {
	sessions session.Service
	tasks    task.Store
	router   *hostedChildInputRouter
}

func (b *collaborationBackend) List(ctx context.Context, id string) ([]collaboration.Thread, error) {
	active, err := b.sessions.Session(ctx, session.SessionRef{SessionID: id})
	if err != nil {
		return nil, err
	}
	reader, ok := b.sessions.(session.StateReader)
	if !ok {
		return nil, errors.New("Session lifecycle unavailable")
	}
	closed, err := appserver.IsSessionClosed(ctx, reader, active.SessionRef)
	if err != nil {
		return nil, err
	}
	if closed {
		return nil, errors.Join(collaboration.ErrSessionClosed, appserver.ErrSessionClosed)
	}
	out := []collaboration.Thread{{ID: id, SessionID: id, Handle: "parent", Name: "Main Agent", State: "running"}}
	// Runtime acquisition is unnecessary for discovery. Automatic parent
	// delivery is allowed only while a registry already exists.
	b.router.mu.RLock()
	registry := b.router.runtimes
	b.router.mu.RUnlock()
	if registry != nil {
		rt, release, e := registry.acquireLoadedRuntime(id)
		if e == nil && rt == nil {
			out[0].State = "idle"
			out[0].CanDeliver = true
		}
		if e == nil && rt != nil && rt.instance != nil {
			if gw := rt.instance.currentGateway(); gw != nil {
				if turn, running := gw.ActiveTurn(id); !running {
					out[0].State = "idle"
					out[0].CanDeliver = true
				} else if turn.Kind == kernel.ActiveTurnKindKernel && turn.ParticipantID == "" {
					if active.Controller.Kind != session.ControllerKindACP {
						out[0].CanDeliver = true
					} else if status, found, err := rt.instance.ACPControllerStatus(ctx, active.SessionRef); err == nil && found {
						out[0].CanDeliver = status.SupportsSteering
					}
				}
			}
		}
		if release != nil {
			release()
		}
	}
	entries, err := b.tasks.ListSession(ctx, active.SessionRef)
	if err != nil {
		return nil, err
	}
	for _, p := range active.Participants {
		if p.Kind != session.ParticipantKindSubagent {
			continue
		}
		t := collaboration.Thread{ID: p.DelegationID, SessionID: p.SessionID, ParticipantID: p.ID, Generation: p.AttachmentGeneration, Handle: hostedChildHandle(p), Name: p.AgentName, State: "starting"}
		for _, entry := range entries {
			if entry.TaskID == p.DelegationID {
				t.State = string(entry.State)
				t.Revision = entry.Revision
				steer, _ := entry.Metadata["supports_steering"].(bool)
				t.CanDeliver = (task.IsTerminalState(entry.State) || steer) && entry.State != task.StateUnknownOutcome
				break
			}
		}
		out = append(out, t)
	}
	return out, nil
}

func (b *collaborationBackend) Deliver(ctx context.Context, id string, messages []collaboration.Message) error {
	b.router.mu.RLock()
	registry := b.router.runtimes
	b.router.mu.RUnlock()
	if registry == nil {
		return errors.New("collaboration runtime unavailable")
	}
	rt, active, release, err := registry.acquireControlRuntime(ctx, id, true)
	if err != nil {
		return err
	}
	if release != nil {
		defer func() { _ = release(context.WithoutCancel(ctx)) }()
	}
	if rt == nil || rt.instance == nil {
		return errors.New("collaboration runtime unavailable")
	}
	if len(messages) == 0 {
		return errors.New("collaboration batch is empty")
	}
	entries := make([]agent.AgentInputBatchEntry, len(messages))
	target := messages[0].To
	for i, m := range messages {
		if m.To != target {
			return errors.New("collaboration batch has multiple recipients")
		}
		entries[i].Message.Input = m.Text + "\n\nMessage-ID: " + m.ID
		if m.ReplyTo != "" {
			entries[i].Message.Input += "\nIn-Reply-To: " + m.ReplyTo
		}
		entries[i].Message.DisplayInput = m.Text
		if m.From == "parent" {
			entries[i].Message.Source = session.ControllerExecutor(active.Controller)
			continue
		}
		for _, p := range active.Participants {
			if hostedChildHandle(p) == m.From && p.Kind == session.ParticipantKindSubagent {
				binding := session.CloneParticipantBinding(p)
				entries[i].Participant = &binding
				break
			}
		}
		if entries[i].Participant == nil {
			return errors.New("collaboration sender detached")
		}
	}
	return rt.instance.engine.SubmitAgentInputBatch(ctx, active.SessionRef, target, entries, func(ctx context.Context, current session.Session, inputs []agent.AgentCommunicationInput) error {
		return routeHostedChildInputBatchToParent(ctx, &rt.instance.runtimeComposition, current, inputs)
	})
}

// DeliverUserInput retains the exact queued recipient and lets Runtime share
// ordinary child admission and output observation with Agent communication.
func (b *collaborationBackend) DeliverUserInput(ctx context.Context, input collaboration.UserInput) error {
	b.router.mu.RLock()
	registry := b.router.runtimes
	b.router.mu.RUnlock()
	if registry == nil {
		return errorcode.New(errorcode.Unavailable, "Child runtime unavailable")
	}
	rt, active, release, err := registry.acquireControlRuntime(ctx, input.SessionID, true)
	if err != nil {
		return err
	}
	if release != nil {
		defer func() { _ = release(context.WithoutCancel(ctx)) }()
	}
	if rt == nil || rt.instance == nil {
		return errorcode.New(errorcode.Unavailable, "Child runtime unavailable")
	}
	for _, binding := range active.Participants {
		if binding.ID == input.ParticipantID && binding.Kind == session.ParticipantKindSubagent && binding.DelegationID == input.TaskID && binding.SessionID == input.ChildSessionID && binding.AttachmentGeneration == input.Generation {
			_, err := rt.instance.engine.SubmitUserChildInput(ctx, active.SessionRef, binding, input.UserID, input.Text, input.ContentParts)
			return err
		}
	}
	return errorcode.New(errorcode.Conflict, "Child detached or replaced before delivery")
}

// CollaborationService exposes the focused Host-owned mailbox service.
func (s *Stack) CollaborationService() *collaboration.Service {
	return s.composition.authorities.collaboration
}

func (s *runtimeComposition) collaborationTools(active session.Session) []tool.Tool {
	service := s.authorities.collaboration
	if service == nil {
		return nil
	}
	return collaboration.Tools(!sessionvisibility.IsSpawnedSubagentSession(active), func(ctx context.Context, req collaboration.Request) (json.RawMessage, error) {
		identity := collaboration.Identity{Session: active.SessionID, Member: "parent"}
		if sessionvisibility.IsSpawnedSubagentSession(active) {
			parentID := hostedChildMetadataString(active.Metadata, sessionvisibility.MetadataSystemManagedParent)
			parent, err := s.sessions.Session(ctx, session.SessionRef{SessionID: parentID})
			if err != nil {
				return nil, err
			}
			binding, ok := hostedChildParticipant(parent, active)
			if !ok {
				return nil, errors.New("participant is no longer attached")
			}
			identity = collaboration.Identity{Session: parentID, Member: hostedChildHandle(binding)}
		}
		return service.Call(ctx, identity, req)
	})
}

func (s *runtimeComposition) collaborationServers(spawn subagent.SpawnContext) ([]acp.McpServer, *collaboration.Grant, error) {
	if s.authorities.collaboration == nil {
		return nil, nil, nil
	}
	process := s.runtimeProcessSnapshot()
	if process.childControlURL == "" {
		return nil, nil, nil
	}
	command, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	grant := s.authorities.collaboration.Prepare(collaboration.Identity{Session: spawn.SessionRef.SessionID, Member: strings.TrimPrefix(spawn.Handle, "@")}, spawn.TaskID)
	return []acp.McpServer{{Stdio: &acp.McpServerStdio{Name: "caelis-collaboration", Command: filepath.Clean(command), Args: []string{"collaboration", "mcp", "--stdio"}, Env: []acp.EnvVariable{{Name: "CAELIS_COLLABORATION_URL", Value: process.childControlURL}, {Name: "CAELIS_COLLABORATION_TOKEN", Value: grant.Token()}}}}}, grant, nil
}

func (b *collaborationBackend) Read(ctx context.Context, id, handle string, after uint64) (collaboration.ThreadRead, error) {
	threads, err := b.List(ctx, id)
	if err != nil {
		return collaboration.ThreadRead{}, err
	}
	for _, thread := range threads {
		if thread.Handle != handle || handle == "parent" {
			continue
		}
		result := collaboration.ThreadRead{Thread: thread, Cursor: thread.Revision}
		entry, err := b.tasks.Get(ctx, thread.ID)
		if err != nil {
			return result, err
		}
		if entry != nil {
			result.Cursor = entry.Revision
			result.Thread.Revision = entry.Revision
			result.Thread.State = string(entry.State)
		}
		if entry != nil && entry.Revision > after && !entry.Running {
			result.Output, _ = entry.Result["final_message"].(string)
			if result.Output == "" {
				result.Output, _ = entry.Result["result"].(string)
			}
			if result.Output == "" {
				result.Output = entry.FailureDiagnostic
			}
		}
		if len(result.Output) > 65536 {
			result.Output = string([]rune(result.Output)[:min(len([]rune(result.Output)), 16384)])
			result.Truncated = true
		}
		return result, nil
	}
	return collaboration.ThreadRead{}, errors.New("participant not found")
}

func (b *collaborationBackend) Remove(ctx context.Context, id, handle string) error {
	active, err := b.sessions.Session(ctx, session.SessionRef{SessionID: id})
	if err != nil {
		return err
	}
	for _, p := range active.Participants {
		if p.Kind != session.ParticipantKindSubagent || hostedChildHandle(p) != handle {
			continue
		}
		b.router.mu.RLock()
		registry := b.router.runtimes
		b.router.mu.RUnlock()
		if registry == nil {
			return errors.New("participant runtime unavailable")
		}
		rt, _, release, err := registry.acquireControlRuntime(ctx, id, true)
		if err != nil {
			return err
		}
		if release != nil {
			defer func() { _ = release(context.WithoutCancel(ctx)) }()
		}
		if rt == nil || rt.instance == nil {
			return errors.New("participant runtime unavailable")
		}
		_, err = rt.instance.engine.DetachParticipant(ctx, agent.DetachParticipantRequest{SessionRef: active.SessionRef, ParticipantID: p.ID, Source: "collaboration", RequireSettled: true, ExpectedDelegationID: p.DelegationID, ExpectedAttachmentGeneration: p.AttachmentGeneration})
		return err
	}
	return errors.New("participant not found")
}
