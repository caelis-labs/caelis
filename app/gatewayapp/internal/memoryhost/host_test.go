package memoryhost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/memorybinding"
	v1alpha1 "github.com/caelis-labs/memory/api/memory/v1alpha1"
	"github.com/caelis-labs/memory/sdk/go/memory/sidecar"
)

func TestVerifyPinnedManifestRequiresEveryExactIdentity(t *testing.T) {
	manifest := sidecar.Manifest{
		FormatVersion:  sidecar.ManifestFormatVersion,
		ServiceVersion: "0.2.0-alpha.1", BuildRevision: strings.Repeat("a", 40),
		Protocol: v1alpha1.LocalTransportProtocol, APIVersion: v1alpha1.ProtocolVersion,
		CoreProfile: v1alpha1.CoreProfile, GOOS: "darwin", GOARCH: "arm64",
		Executable: "memoryd", SHA256: strings.Repeat("b", 64),
	}
	expected := memorybinding.APICompatibility{
		Protocol: manifest.Protocol, APIVersion: manifest.APIVersion,
		CoreProfile: manifest.CoreProfile, ServiceVersion: manifest.ServiceVersion,
		BuildRevision: manifest.BuildRevision, ArtifactSHA256: manifest.SHA256,
	}
	if err := verifyPinnedManifest(manifest, expected); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*memorybinding.APICompatibility){
		"protocol": func(pin *memorybinding.APICompatibility) { pin.Protocol = "other" },
		"API":      func(pin *memorybinding.APICompatibility) { pin.APIVersion = "other" },
		"profile":  func(pin *memorybinding.APICompatibility) { pin.CoreProfile = "other" },
		"version":  func(pin *memorybinding.APICompatibility) { pin.ServiceVersion = "other" },
		"revision": func(pin *memorybinding.APICompatibility) { pin.BuildRevision = strings.Repeat("c", 40) },
		"digest":   func(pin *memorybinding.APICompatibility) { pin.ArtifactSHA256 = strings.Repeat("d", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := expected
			mutate(&changed)
			if err := verifyPinnedManifest(manifest, changed); err == nil {
				t.Fatal("verifyPinnedManifest() accepted a mismatched pin")
			}
		})
	}
}

func TestStartRejectsTamperedArtifactBeforeLaunch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed local Alpha sidecar uses the Unix Socket host profile")
	}
	directory := t.TempDir()
	executable := filepath.Join(directory, "memoryd-test")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 99\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest, err := sidecar.CreateManifest(executable, "0.2.0-alpha.1", strings.Repeat("a", 40), runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(directory, "memoryd.manifest.json")
	writeHostManifest(t, manifestPath, manifest)
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 98\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = Start(context.Background(), Config{
		ManifestPath: manifestPath,
		DataDir:      filepath.Join(directory, "data"),
		Endpoint:     testHostEndpoint(manifest),
		Credentials:  func(context.Context, string) (string, error) { return "unused", nil },
		StartTimeout: 10 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("Start(tampered artifact) error = %v", err)
	}
}

func TestStartReportsVerifiedProcessExitBeforeReadiness(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed local Alpha sidecar uses the Unix Socket host profile")
	}
	directory := t.TempDir()
	executable := filepath.Join(directory, "memoryd-test")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 23\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest, err := sidecar.CreateManifest(executable, "0.2.0-alpha.1", strings.Repeat("a", 40), runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(directory, "memoryd.manifest.json")
	writeHostManifest(t, manifestPath, manifest)
	_, err = Start(context.Background(), Config{
		ManifestPath: manifestPath,
		DataDir:      filepath.Join(directory, "data"),
		Endpoint:     testHostEndpoint(manifest),
		Credentials:  func(context.Context, string) (string, error) { return "unused", nil },
		StartTimeout: 10 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "exited before readiness") {
		t.Fatalf("Start(exited process) error = %v", err)
	}
}

func testHostEndpoint(manifest sidecar.Manifest) memorybinding.EndpointConfig {
	return memorybinding.EndpointConfig{
		ID: "memory-default", Deployment: memorybinding.DeploymentModeManagedLocal,
		Compatibility: memorybinding.APICompatibility{
			Protocol: manifest.Protocol, APIVersion: manifest.APIVersion, CoreProfile: manifest.CoreProfile,
			ServiceVersion: manifest.ServiceVersion, BuildRevision: manifest.BuildRevision, ArtifactSHA256: manifest.SHA256,
		},
	}
}

func TestValidateBindingRejectsEndpointOrArtifactDrift(t *testing.T) {
	endpoint := memorybinding.EndpointConfig{
		ID: "memory-default", Deployment: memorybinding.DeploymentModeManagedLocal,
		Compatibility: memorybinding.APICompatibility{
			Protocol: "memory.local.v1alpha1", APIVersion: "memory.v1alpha1", CoreProfile: "memory.core.v1alpha1",
			ServiceVersion: "0.2.0-alpha.1", BuildRevision: strings.Repeat("a", 40), ArtifactSHA256: strings.Repeat("b", 64),
		},
	}
	host := &Host{endpoint: endpoint}
	binding := memorybinding.RuntimeMemoryBindingSnapshot{Endpoint: endpoint}
	if err := host.ValidateBinding(binding); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*memorybinding.RuntimeMemoryBindingSnapshot){
		"endpoint": func(snapshot *memorybinding.RuntimeMemoryBindingSnapshot) { snapshot.Endpoint.ID = "memory-other" },
		"artifact": func(snapshot *memorybinding.RuntimeMemoryBindingSnapshot) {
			snapshot.Endpoint.Compatibility.ArtifactSHA256 = strings.Repeat("c", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := binding
			mutate(&changed)
			if err := host.ValidateBinding(changed); err == nil {
				t.Fatal("ValidateBinding() accepted endpoint drift")
			}
		})
	}
}

func TestCompatibilityMismatchRetainsTypedIdentity(t *testing.T) {
	err := fmt.Errorf("probe: %w", &v1alpha1.ServiceError{
		Code: v1alpha1.ErrorCodeIncompatible, Message: "wrong build",
	})
	if !permanentCompatibilityError(err) {
		t.Fatal("incompatible handshake was treated as transient readiness")
	}
	if permanentCompatibilityError(&v1alpha1.ServiceError{Code: v1alpha1.ErrorCodeUnavailable}) {
		t.Fatal("transient unavailable handshake was treated as a permanent mismatch")
	}
}

func writeHostManifest(t *testing.T, path string, manifest sidecar.Manifest) {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCapabilitySourceCachesAndRenewsOneImmutableBindingPerOperation(t *testing.T) {
	now := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	var credentials int
	var requests []v1alpha1.CapabilityIssueRequest
	host := &Host{
		socketPath: "/owner/memoryd.sock",
		credentials: func(context.Context, string) (string, error) {
			credentials++
			return "issuer-secret", nil
		},
		issue: func(_ context.Context, socket, credential string, request v1alpha1.CapabilityIssueRequest) (v1alpha1.RuntimeCapability, error) {
			if socket != "/owner/memoryd.sock" || credential != "issuer-secret" {
				return v1alpha1.RuntimeCapability{}, fmt.Errorf("unexpected issuer authority")
			}
			requests = append(requests, request)
			return v1alpha1.RuntimeCapability{
				Token:     v1alpha1.CapabilityToken(fmt.Sprintf("capability-%d", len(requests))),
				ExpiresAt: now.Add(defaultCapabilityTTL),
			}, nil
		},
	}
	binding := memorybinding.RuntimeMemoryBindingSnapshot{
		RuntimeActorRef: "actor-a", PrincipalRef: "principal:a",
		IssuerCredentialRef: "memory-issuer:" + strings.Repeat("a", 32),
		ViewRef:             "view-a", GrantRef: "grant-a",
		Audience: memorybinding.OutputAudiencePrivate, BindingVersion: 1,
	}
	source := &capabilitySource{host: host, binding: binding, now: func() time.Time { return now }, byOperation: map[v1alpha1.Operation]v1alpha1.RuntimeCapability{}}
	first, err := source.Authorization(context.Background(), v1alpha1.OperationRemember)
	if err != nil {
		t.Fatal(err)
	}
	second, err := source.Authorization(context.Background(), v1alpha1.OperationRemember)
	if err != nil {
		t.Fatal(err)
	}
	if first.Capability != second.Capability || len(requests) != 1 || credentials != 1 {
		t.Fatalf("cached capability = %#v/%#v requests=%d credentials=%d", first, second, len(requests), credentials)
	}
	if _, err := source.Authorization(context.Background(), v1alpha1.OperationRecall); err != nil {
		t.Fatal(err)
	}
	now = now.Add(defaultCapabilityTTL - capabilityRenewalAge)
	renewed, err := source.Authorization(context.Background(), v1alpha1.OperationRemember)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Capability == first.Capability || len(requests) != 3 || credentials != 3 {
		t.Fatalf("renewed capability = %#v requests=%d credentials=%d", renewed, len(requests), credentials)
	}
	for index, request := range requests {
		if request.PrincipalRef != binding.PrincipalRef || request.GrantRef != v1alpha1.GrantID(binding.GrantRef) ||
			request.ActorRef != string(binding.RuntimeActorRef) || request.Audience != v1alpha1.Audience(binding.Audience) ||
			len(request.Operations) != 1 || request.TTLSeconds != int64(defaultCapabilityTTL/time.Second) {
			t.Fatalf("issue request[%d] = %#v", index, request)
		}
	}
}
