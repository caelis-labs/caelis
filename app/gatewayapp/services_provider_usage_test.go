package gatewayapp

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	model "github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelconfig/providerusage"
)

type grokProviderUsageReader struct {
	calls atomic.Int32
}

func (r *grokProviderUsageReader) SubscriptionUsage(context.Context) (providerusage.Snapshot, error) {
	r.calls.Add(1)
	return providerusage.Snapshot{Provider: "xai"}, nil
}

type blockingProviderUsageReader struct {
	started chan struct{}
	release chan struct{}
	done    chan struct{}
}

func (r *blockingProviderUsageReader) SubscriptionUsage(context.Context) (providerusage.Snapshot, error) {
	defer close(r.done)
	close(r.started)
	<-r.release
	return providerusage.Snapshot{Provider: "xai"}, nil
}

func TestModelServiceProviderUsageSupportsGrokOAuthCredential(t *testing.T) {
	t.Parallel()

	lookup, err := newModelLookup(nil, 500000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lookup.Upsert(ModelConfig{
		Provider:      "xai",
		API:           model.APIXAIResponses,
		Model:         "grok-4.5",
		BaseURL:       modelconfig.GrokOAuthBaseURL,
		CredentialRef: modelconfig.GrokOAuthCredentialRef,
		AuthType:      model.AuthOAuthToken,
	}); err != nil {
		t.Fatal(err)
	}
	reader := &grokProviderUsageReader{}
	service := ModelService{stack: &Stack{
		lookup: lookup,
		providerUsage: providerusage.NewRegistry(map[string]providerusage.Reader{
			"xai": reader,
		}),
	}}

	snapshot, found, err := service.ProviderUsage(context.Background(), lookup.DefaultAlias())
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("ProviderUsage() did not find the Grok OAuth usage adapter")
	}
	deadline := time.Now().Add(time.Second)
	for snapshot.Provider != "xai" {
		if time.Now().After(deadline) {
			t.Fatalf("ProviderUsage() did not publish asynchronous snapshot: %#v", snapshot)
		}
		time.Sleep(time.Millisecond)
		snapshot, found, err = service.ProviderUsage(context.Background(), lookup.DefaultAlias())
		if err != nil || !found {
			t.Fatalf("ProviderUsage() refresh read = snapshot:%#v found:%v error:%v", snapshot, found, err)
		}
	}
	if calls := reader.calls.Load(); calls != 1 {
		t.Fatalf("ProviderUsage() refresh calls = %d, want 1", calls)
	}
}

func TestModelServiceProviderUsageDoesNotWaitForProvider(t *testing.T) {
	t.Parallel()

	lookup, err := newModelLookup(nil, 500000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lookup.Upsert(ModelConfig{
		Provider:      "xai",
		API:           model.APIXAIResponses,
		Model:         "grok-4.5",
		BaseURL:       modelconfig.GrokOAuthBaseURL,
		CredentialRef: modelconfig.GrokOAuthCredentialRef,
		AuthType:      model.AuthOAuthToken,
	}); err != nil {
		t.Fatal(err)
	}
	reader := &blockingProviderUsageReader{
		started: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(reader.release) }) }
	defer release()
	service := ModelService{stack: &Stack{
		lookup: lookup,
		providerUsage: providerusage.NewRegistry(map[string]providerusage.Reader{
			"xai": reader,
		}),
	}}

	returned := make(chan struct{})
	go func() {
		_, _, _ = service.ProviderUsage(context.Background(), lookup.DefaultAlias())
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("ProviderUsage blocked on the provider account API")
	}
	select {
	case <-reader.started:
	case <-time.After(time.Second):
		t.Fatal("ProviderUsage did not start an asynchronous refresh")
	}
	release()
	select {
	case <-reader.done:
	case <-time.After(time.Second):
		t.Fatal("provider usage refresh did not stop after release")
	}
}
