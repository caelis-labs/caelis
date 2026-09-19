package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/collaboration"
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
	definitions := collaboration.Definitions(false)
	if raw := os.Getenv("CAELIS_COLLABORATION_TOOLS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &definitions); err != nil {
			return fmt.Errorf("collaboration tool definitions: %w", err)
		}
	}
	return surfacemcp.Run(ctx, httpclient.Collaboration(endpoint, token), &mcp.IOTransport{Reader: io.NopCloser(stdin), Writer: collaborationWriter{stdout}}, definitions)
}
