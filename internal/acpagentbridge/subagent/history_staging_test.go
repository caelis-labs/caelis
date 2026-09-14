package subagent

import (
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

func TestHistoryStagingStreamsLongReplayAndPreservesInputSources(t *testing.T) {
	c := newHistoryCollector(&Runner{clock: time.Now}, delegation.Anchor{SessionID: "child-1", TaskID: "task"}, "helper")
	c.openStaging()
	defer c.closeStaging()
	if err := c.errSnapshot(); err != nil {
		t.Fatal(err)
	}
	path := c.staging.file.Name()
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows uses inherited ACLs rather than Unix permission bits.
	if runtime.GOOS != "windows" && stat.Mode().Perm() != 0o600 {
		t.Fatal("history staging is not private")
	}
	const count = 9000
	for i := range count {
		c.observe(contentUpdate(t, client.UpdateAgentMessage, fmt.Sprint(i)))
		if len(c.events) > 1 {
			t.Fatal("completed output accumulated in memory")
		}
	}
	c.observe(contentUpdate(t, client.UpdateUserMessage, "review "))
	c.observe(contentUpdate(t, client.UpdateUserMessage, "the change"))
	c.observe(contentUpdate(t, client.UpdateUserMessage, "\n\nFrom: reviewer"))
	c.observe(contentUpdate(t, client.UpdateAgentMessage, "done"))
	c.observe(contentUpdate(t, client.UpdateUserMessage, "human guidance"))
	seen := 0
	err = c.streamEvents(t.Context(), func(event *session.Event) error {
		if event.ID != fmt.Sprintf("subagent-load:task:%d", seen+1) {
			t.Fatalf("replay identity changed at %d", seen)
		}
		switch {
		case seen < count:
			if session.EventText(event) != fmt.Sprint(seen) {
				t.Fatalf("output lost or repeated at %d", seen)
			}
		case seen < count+2:
			if event.Actor.Name != "reviewer" {
				t.Fatal("chunked footer lost input provenance")
			}
		case seen == count+3:
			if event.Actor.Kind != session.ActorKindUser || session.EventText(event) != "human guidance" {
				t.Fatal("human input became Agent mail")
			}
		}
		seen++
		return nil
	})
	if err != nil || seen != count+4 {
		t.Fatalf("staged history=%d error=%v", seen, err)
	}
	c.closeStaging()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("history staging survived completion")
	}
}
