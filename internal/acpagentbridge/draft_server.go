package acpagentbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	sdkstdio "github.com/caelis-labs/acp-go-sdk/transport/stdio"
)

// DraftProductAgent is a pinned protocol facade over principal-bound Control.
type DraftProductAgent interface {
	v2.Agent
	BindDraftCallbacks(DraftCallbacks)
	Close() error
}

// ServeDraftProductStreams gives local product assembly the same complete ACP
// transport as stdio without depending on a presentation package. The v2 SDK
// owns wire directions and decoding, with no stable wire handler fallback.
func ServeDraftProductStreams(ctx context.Context, agent DraftProductAgent, in io.Reader, out io.Writer) error {
	if agent == nil || in == nil || out == nil {
		return errors.New("acp: draft agent and streams are required")
	}
	input, closeInput, err := draftOwnedStream(in)
	if err != nil {
		return err
	}
	output, closeOutput, err := draftOwnedStream(out)
	if err != nil {
		closeInput()
		return err
	}
	// Same decoded aggregate image budget as the product's stable endpoint.
	peer, err := v2.NewAgentSideConnectionWithOptions(agent, output.(io.Writer), input.(io.Reader), acp.ConnectionOptions{MaxFrameSize: 64 * 1024 * 1024})
	if err != nil {
		closeInput()
		closeOutput()
		return err
	}
	agent.BindDraftCallbacks(peer)
	defer agent.Close()
	defer peer.Close()
	if err := peer.Wait(ctx); err != nil {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if errors.Is(err, acp.ErrPeerClosed) || errors.Is(err, acp.ErrConnectionClosed) {
			return nil
		}
		return err
	}
	return nil
}

// File streams transfer independent descriptors to the SDK. Other streams
// retain the established ownership contract so Close interrupts blocked I/O.
func draftOwnedStream(stream any) (any, func(), error) {
	file, ok := stream.(*os.File)
	if !ok {
		return stream, func() {}, nil
	}
	duplicate, err := sdkstdio.DuplicateFile(file)
	if err != nil {
		return nil, nil, fmt.Errorf("acp: duplicate draft stream: %w", err)
	}
	return duplicate, func() { _ = duplicate.Close() }, nil
}
