package session

import (
	"fmt"
	"strings"
)

// AgentCommunicationPromptHeader returns the trusted sender header placed in
// provider-visible context before an Agent-to-Agent message.
func AgentCommunicationPromptHeader(actor ActorRef) string {
	actor = CloneActorRef(actor)
	name := singleLineAgentIdentity(firstNonEmpty(actor.Name, actor.ID, string(actor.Kind), "agent"))
	fields := []string{
		"[Internal agent message]",
		"Sender: " + name,
	}
	if actor.Kind != "" {
		fields = append(fields, "Kind: "+singleLineAgentIdentity(string(actor.Kind)))
	}
	if actor.Role != "" {
		fields = append(fields, "Role: "+singleLineAgentIdentity(actor.Role))
	}
	if actor.ID != "" && !strings.EqualFold(actor.ID, name) {
		fields = append(fields, "Sender ID: "+singleLineAgentIdentity(actor.ID))
	}
	fields = append(fields, "Message:")
	return strings.TrimSpace(strings.Join(fields, "\n"))
}

// ValidateAgentCommunicationActor rejects an untrusted or user-authored source
// before Agent communication enters model context.
func ValidateAgentCommunicationActor(actor ActorRef) error {
	actor = CloneActorRef(actor)
	if !ActorRefHasIdentity(actor) {
		return fmt.Errorf("agent communication requires source identity")
	}
	if actor.ID == "" && actor.Name == "" {
		return fmt.Errorf("agent communication requires a source ID or name")
	}
	if strings.EqualFold(strings.TrimSpace(string(actor.Kind)), string(ActorKindUser)) {
		return fmt.Errorf("agent communication source cannot be a user")
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "kind", value: string(actor.Kind)},
		{name: "ID", value: actor.ID},
		{name: "name", value: actor.Name},
		{name: "role", value: actor.Role},
	} {
		if strings.ContainsAny(field.value, "\r\n") {
			return fmt.Errorf("agent communication source %s must be single-line", field.name)
		}
	}
	return nil
}

func singleLineAgentIdentity(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
