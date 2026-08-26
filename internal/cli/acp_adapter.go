package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	controladapterhost "github.com/caelis-labs/caelis/control/adapterhost"
	"github.com/caelis-labs/caelis/internal/adapterhostclient"
)

func runAdapterACPProxy(
	ctx context.Context,
	cfg gatewayapp.Config,
	options productClientOptions,
	adapterID string,
	grantFile string,
	input io.Reader,
	output io.Writer,
) error {
	adapterID = strings.ToLower(strings.TrimSpace(adapterID))
	if adapterID != controladapterhost.CodexAdapterID {
		return fmt.Errorf("cli: unknown built-in ACP adapter %q", adapterID)
	}
	if strings.TrimSpace(grantFile) != "" {
		grant, err := adapterhostclient.ConsumeChannelGrantFile(grantFile)
		if err != nil {
			return fmt.Errorf("cli: consume built-in adapter grant: %w", err)
		}
		if !strings.EqualFold(grant.AdapterID, adapterID) {
			return errors.New("cli: built-in adapter grant identity does not match --adapter")
		}
		client, err := adapterhostclient.NewChannel(grant.Endpoint, options.HTTPClient)
		if err != nil {
			return err
		}
		return client.Proxy(ctx, adapterID, grant.Token, input, output)
	}
	baseURL := strings.TrimSpace(options.ControlURL)
	if baseURL == "" {
		record, err := controlserver.LoadDiscoveryRecord(controlserver.DefaultDiscoveryFile(options.StoreDir))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return errors.New("cli: local Caelis Host is not running; start it before using --adapter codex")
			}
			return fmt.Errorf("cli: locate local Caelis Host: %w", err)
		}
		baseURL = record.Endpoint
	}
	token, err := resolveControlToken(options)
	if err != nil {
		return err
	}
	client, err := adapterhostclient.New(adapterhostclient.Config{BaseURL: baseURL, BearerToken: token, HTTPClient: options.HTTPClient})
	if err != nil {
		return err
	}
	descriptor, err := client.Inspect(ctx, adapterID)
	if err != nil {
		return fmt.Errorf("cli: inspect built-in %s adapter: %w", adapterID, err)
	}
	if !descriptor.Available {
		return fmt.Errorf("cli: built-in %s adapter is unavailable: %s", adapterID, descriptor.Diagnostic)
	}
	grant, err := client.IssueGrant(ctx, adapterID, controladapterhost.GrantRequest{
		ConnectionID: adapterID, CWD: cfg.WorkspaceCWD,
		AllowedRoots: []string{cfg.WorkspaceCWD}, WritableRoots: []string{cfg.WorkspaceCWD},
	})
	if err != nil {
		return fmt.Errorf("cli: authorize built-in %s adapter: %w", adapterID, err)
	}
	return client.Proxy(ctx, adapterID, grant.Token, input, output)
}
