package codex

import (
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/adapters/codex/internal/appserver"
)

func TestBufferedRouteSplicesOnlyPostBarrierNotifications(t *testing.T) {
	route := &sessionRoute{mode: routeBuffering, closed: make(chan struct{}), seenItem: make(map[string]bool)}
	route.enqueue(appserver.Notification{Sequence: 1, Method: "before"})
	route.enqueue(appserver.Notification{Sequence: 2, Method: "after"})
	post := route.acceptStableBarrier(1)
	if len(post) != 1 || post[0].Sequence != 2 {
		t.Fatalf("post-barrier batch = %#v", post)
	}

	route.enqueue(appserver.Notification{Sequence: 3, Method: "during-drain"})
	batch, live, err := route.takeBufferedOrSwitchLive()
	if err != nil {
		t.Fatal(err)
	}
	if live || len(batch) != 1 || batch[0].Sequence != 3 {
		t.Fatalf("drain batch = %#v, live=%t", batch, live)
	}
	batch, live, err = route.takeBufferedOrSwitchLive()
	if err != nil {
		t.Fatal(err)
	}
	if !live || len(batch) != 0 || route.mode != routeLive {
		t.Fatalf("final drain = %#v, live=%t, mode=%v", batch, live, route.mode)
	}
}

func TestClosedBufferedRouteReturnsItsFailure(t *testing.T) {
	route := &sessionRoute{mode: routeBuffering, closed: make(chan struct{}), seenItem: make(map[string]bool), state: &sessionState{}}
	want := errors.New("route failed")
	route.close(want)

	_, live, err := route.takeBufferedOrSwitchLive()
	if live || !errors.Is(err, want) {
		t.Fatalf("closed drain: live=%t err=%v, want %v", live, err, want)
	}
}
