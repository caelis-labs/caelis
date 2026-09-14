package history

import (
	"fmt"
	"testing"
)

func TestIndexCompleteWindowsAndInterleavedTurns(t *testing.T) {
	var index Index
	for n := range 40 {
		index.Observe(uint64(2*n), fmt.Sprint(n))
		index.Observe(uint64(2*n+1), fmt.Sprint(n))
	}
	for _, tc := range []struct {
		before uint64
		want   uint64
	}{{80, 48}, {48, 16}, {16, 0}} {
		if got := index.Start(tc.before, 16); got != tc.want {
			t.Fatalf("before %d: %d want %d", tc.before, got, tc.want)
		}
	}
	index.Observe(80, "1")
	if got := index.Start(81, 16); got != 2 {
		t.Fatalf("interleaved Turn prefix lost: %d", got)
	}
	if got := index.Start(80, 16); got != 48 {
		t.Fatalf("later append changed captured window: %d", got)
	}
}

func TestHistoryTokenPurposeAddressAndSource(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	p := Position{SessionID: "s", TaskID: "t", Source: "incarnation", Before: 120}
	token := Encode(secret, p)
	if got, err := Decode(secret, token, "s", "t"); err != nil || got != p {
		t.Fatalf("round trip %v %v", got, err)
	}
	for _, address := range [][2]string{{"other", "t"}, {"s", "other"}, {"s", ""}} {
		if _, err := Decode(secret, token, address[0], address[1]); err == nil {
			t.Fatal("foreign address accepted")
		}
	}
	if _, err := Decode(secret, token+"a", "s", "t"); err == nil {
		t.Fatal("modified token accepted")
	}
	if ValidateRequest("live", token, 16) == nil {
		t.Fatal("mixed direction accepted")
	}
	if Encode(secret, Position{SessionID: "s"}) != "" {
		t.Fatal("origin must terminate traversal")
	}
}
