package hostownership

import (
	"context"
	"testing"
	"time"
)

func TestAuthorityProvesMatchingLiveStoreGuard(t *testing.T) {
	storeDir := t.TempDir()
	authority, err := Acquire(context.Background(), storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if !authority.Authorizes(storeDir) {
		t.Fatal("Authority.Authorizes(matching store) = false")
	}
	if authority.Authorizes(t.TempDir()) {
		t.Fatal("Authority.Authorizes(other store) = true")
	}
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	if authority.Authorizes(storeDir) {
		t.Fatal("closed Authority still authorizes store")
	}
}

func TestAuthorityCopySharesClosedState(t *testing.T) {
	storeDir := t.TempDir()
	authority, err := Acquire(context.Background(), storeDir)
	if err != nil {
		t.Fatal(err)
	}
	copy := *authority
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	if copy.Authorizes(storeDir) {
		t.Fatal("copied Authority still authorizes after original Close")
	}
}

func TestAuthorityPinDefersClose(t *testing.T) {
	storeDir := t.TempDir()
	authority, err := Acquire(context.Background(), storeDir)
	if err != nil {
		t.Fatal(err)
	}
	release, ok := authority.Pin(storeDir)
	if !ok {
		t.Fatal("Authority.Pin(matching store) = false")
	}
	closed := make(chan error, 1)
	go func() { closed <- authority.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close() returned while ownership operation was pinned: %v", err)
	case <-time.After(40 * time.Millisecond):
	}
	release()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close() did not finish after ownership pin released")
	}
	if authority.Authorizes(storeDir) {
		t.Fatal("closed Authority still authorizes store")
	}
}

func TestAuthorityCannotBeAcquiredTwice(t *testing.T) {
	storeDir := t.TempDir()
	first, err := Acquire(context.Background(), storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Acquire(ctx, storeDir); err == nil {
		t.Fatal("second Acquire() error = nil")
	}
}
