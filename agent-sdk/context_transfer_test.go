package agentsdk

import (
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestMergeContextTransfersPreservesOffsetOrder(t *testing.T) {
	t.Parallel()

	first := ContextTransfer{Turns: []ContextTurn{{
		Executor:     session.ActorRef{Kind: session.ActorKindController, Name: "codex"},
		UserMessages: []string{"first user"}, AssistantSummary: "first assistant",
	}}}
	second := ContextTransfer{Turns: []ContextTurn{{
		Executor:     session.ActorRef{Kind: session.ActorKindParticipant, Name: "claude(aria)"},
		UserMessages: []string{"second user"}, AssistantSummary: "second assistant",
	}}}
	want := ContextTransfer{Turns: append(append([]ContextTurn(nil), first.Turns...), second.Turns...)}
	if got := MergeContextTransfers(first, second); !reflect.DeepEqual(got, want) {
		t.Fatalf("MergeContextTransfers() = %#v, want %#v", got, want)
	}
}

func TestMergeContextTransfersUsesNewestOpaqueCompactBaseline(t *testing.T) {
	t.Parallel()

	earlier := ContextTransfer{
		Summary: "old baseline",
		Turns:   []ContextTurn{{UserMessages: []string{"old user"}, AssistantSummary: "old assistant"}},
	}
	later := ContextTransfer{
		Summary: "new opaque baseline",
		Turns:   []ContextTurn{{UserMessages: []string{"new user"}, AssistantSummary: "new assistant"}},
	}
	if got := MergeContextTransfers(earlier, later); !reflect.DeepEqual(got, later) {
		t.Fatalf("MergeContextTransfers() = %#v, want newer compact offset %#v", got, later)
	}
}

func TestCloneContextTransferDropsIncompleteTurns(t *testing.T) {
	t.Parallel()

	got := CloneContextTransfer(ContextTransfer{Turns: []ContextTurn{
		{UserMessages: []string{"pending user"}},
		{AssistantSummary: "orphan assistant"},
	}})
	if !ContextTransferEmpty(got) || got.Turns != nil {
		t.Fatalf("CloneContextTransfer() = %#v, want no incomplete public Turns", got)
	}
}

func TestCloneContextTransferPreservesOrderedUserMessages(t *testing.T) {
	t.Parallel()

	in := ContextTransfer{Turns: []ContextTurn{{
		UserMessages:     []string{" initial request ", " steer while running "},
		AssistantSummary: " final answer ",
	}}}
	got := CloneContextTransfer(in)
	want := ContextTransfer{Turns: []ContextTurn{{
		UserMessages:     []string{"initial request", "steer while running"},
		AssistantSummary: "final answer",
	}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CloneContextTransfer() = %#v, want %#v", got, want)
	}
	in.Turns[0].UserMessages[0] = "mutated"
	if !reflect.DeepEqual(got, want) {
		t.Fatal("CloneContextTransfer() retained the input user-message slice")
	}
}
