package cli

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	surfacemcp "github.com/caelis-labs/caelis/surfaces/mcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type collaborationWriter struct{ io.Writer }

func (collaborationWriter) Close() error { return nil }

func runCollaboration(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) != 2 || args[0] != "mcp" || args[1] != "--stdio" {
		return errors.New("usage: caelis collaboration mcp --stdio")
	}
	endpoint, token := os.Getenv("CAELIS_COLLABORATION_URL"), os.Getenv("CAELIS_COLLABORATION_TOKEN")
	if endpoint == "" || token == "" {
		return errors.New("collaboration requires a Host-issued endpoint and participant credential")
	}
	return surfacemcp.Run(ctx, httpclient.Collaboration(endpoint, token), &mcp.IOTransport{Reader: io.NopCloser(stdin), Writer: collaborationWriter{stdout}})
}
