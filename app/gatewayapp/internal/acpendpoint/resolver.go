// Package acpendpoint resolves Host-managed adapter identities into ephemeral
// Caelis stdio proxy processes. It is private composition glue.
package acpendpoint

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	controladapterhost "github.com/caelis-labs/caelis/control/adapterhost"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/endpoint"
	"github.com/caelis-labs/caelis/internal/adapterhostclient"
)

// Config contains process authorities sampled at each child launch.
type Config struct {
	Service    controladapterhost.Service
	StoreDir   string
	Executable func() (string, error)
	ControlURL func() string
}

// Resolver implements the bridge endpoint seam without importing the Host
// composition root.
type Resolver struct {
	service    controladapterhost.Service
	grantDir   string
	executable func() (string, error)
	controlURL func() string
}

// New constructs one process-lifetime resolver.
func New(config Config) (*Resolver, error) {
	if config.Service == nil || config.ControlURL == nil {
		return nil, errors.New("gatewayapp acpendpoint: adapter service and Control URL source are required")
	}
	executable := config.Executable
	if executable == nil {
		executable = os.Executable
	}
	storeDir := strings.TrimSpace(config.StoreDir)
	if storeDir == "" {
		return nil, errors.New("gatewayapp acpendpoint: StoreDir is required")
	}
	return &Resolver{
		service: config.Service, grantDir: filepath.Join(storeDir, "runtime", "adapter-grants"),
		executable: executable, controlURL: config.ControlURL,
	}, nil
}

// ResolveACPProcess creates a one-use grant and a proxy process declaration.
func (r *Resolver) ResolveACPProcess(ctx context.Context, request endpoint.Request) (endpoint.Process, error) {
	controlURL := strings.TrimSpace(r.controlURL())
	if controlURL == "" {
		return endpoint.Process{}, errors.New("gatewayapp acpendpoint: listening Control Host is unavailable")
	}
	descriptor, err := r.service.Inspect(ctx, request.AdapterID)
	if err != nil {
		return endpoint.Process{}, err
	}
	if !descriptor.Available {
		return endpoint.Process{}, errors.New(strings.TrimSpace(descriptor.Diagnostic))
	}
	grant, err := r.service.IssueGrant(ctx, "host-runtime", request.AdapterID, controladapterhost.GrantRequest{
		ConnectionID:  request.ConnectionID,
		CWD:           request.CWD,
		AllowedRoots:  []string{request.CWD},
		WritableRoots: []string{request.CWD},
	})
	if err != nil {
		return endpoint.Process{}, err
	}
	grantFile, err := adapterhostclient.WriteChannelGrantFile(r.grantDir, adapterhostclient.ChannelGrantFile{
		Endpoint: controlURL, AdapterID: request.AdapterID, Token: grant.Token,
	})
	if err != nil {
		return endpoint.Process{}, err
	}
	executable, err := r.executable()
	if err != nil || strings.TrimSpace(executable) == "" {
		_ = os.Remove(grantFile)
		return endpoint.Process{}, errors.New("gatewayapp acpendpoint: resolve Caelis executable")
	}
	return endpoint.Process{
		Command: executable,
		Args:    []string{"acp", "--adapter", request.AdapterID, "--adapter-grant-file", grantFile},
		WorkDir: request.CWD,
		Release: func() { _ = os.Remove(grantFile) },
	}, nil
}

var _ endpoint.Resolver = (*Resolver)(nil)
