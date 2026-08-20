package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	sdkcontroller "github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestTurnHandleSynchronizationWaitsForPriorConsumerWork(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(nil)
	message := model.NewTextMessage(model.RoleAssistant, "before steer")
	handle.publishEvent(&session.Event{Type: session.EventTypeAssistant, Message: &message})
	consumerStarted := make(chan struct{})
	releaseConsumer := make(chan struct{})
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for event, err := range handle.SourceEvents() {
			if err != nil || event.Canonical == nil {
				return
			}
			close(consumerStarted)
			<-releaseConsumer
		}
	}()
	select {
	case <-consumerStarted:
	case <-time.After(time.Second):
		t.Fatal("event consumer did not start")
	}
	synchronized := make(chan error, 1)
	go func() { synchronized <- handle.synchronize(context.Background()) }()
	select {
	case err := <-synchronized:
		t.Fatalf("synchronize() completed before prior consumer work: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseConsumer)
	if err := <-synchronized; err != nil {
		t.Fatalf("synchronize() error = %v", err)
	}
	handle.finish()
	select {
	case <-consumerDone:
	case <-time.After(time.Second):
		t.Fatal("event consumer did not stop")
	}
}

func TestTurnHandleCloseWakesSynchronizationWaiter(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(nil)
	synchronized := make(chan error, 1)
	go func() { synchronized <- handle.synchronize(context.Background()) }()
	deadline := time.Now().Add(time.Second)
	for {
		handle.mu.Lock()
		waiting := len(handle.barriers) == 1
		handle.mu.Unlock()
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("synchronization waiter was not registered")
		}
		time.Sleep(time.Millisecond)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-synchronized:
		if !errors.Is(err, sdkcontroller.ErrNotActive) {
			t.Fatalf("synchronize() error = %v, want ErrNotActive", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close() did not wake synchronization waiter")
	}
	if err := handle.synchronize(context.Background()); !errors.Is(err, sdkcontroller.ErrNotActive) {
		t.Fatalf("synchronize() after Close error = %v, want ErrNotActive", err)
	}
	handle.finish()
}

func TestTurnHandleStoppedConsumerClosesBarrierAdmission(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(nil)
	message := model.NewTextMessage(model.RoleAssistant, "stop here")
	handle.publishEvent(&session.Event{Type: session.EventTypeAssistant, Message: &message})
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for range handle.SourceEvents() {
			break
		}
	}()
	select {
	case <-consumerDone:
	case <-time.After(time.Second):
		t.Fatal("event consumer did not stop")
	}
	if err := handle.synchronize(context.Background()); !errors.Is(err, sdkcontroller.ErrNotActive) {
		t.Fatalf("synchronize() after consumer stop error = %v, want ErrNotActive", err)
	}
	handle.finish()
}
