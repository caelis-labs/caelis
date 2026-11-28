package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

func TestAttachmentModelViewNoop(t *testing.T) {
	for _, mode := range []string{"native", "goja"} {
		t.Run(mode, func(t *testing.T) {
			var views atomic.Int32
			var referencedViews atomic.Int32
			fixture := newAttachmentSettlementFixture(t, mode, []hook.ModelView{{
				Name: "noop",
				Apply: func(_ context.Context, _ hook.ToolContext, message model.Message) (model.Message, error) {
					views.Add(1)
					for _, part := range message.Parts[0].ToolResult.Content {
						if part.Media != nil && part.Media.Source.Kind == model.MediaSourceAttachment {
							referencedViews.Add(1)
						}
					}
					return message, nil
				},
			}})
			run := fixture.run(t, nil)
			if err := run.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			ref := fixture.checkReceipt(t)
			requests := fixture.model.requests
			if len(requests) != 2 {
				t.Fatalf("model requests = %d, want 2", len(requests))
			}
			visible := errorDeliveryModelResult(t, requests[1], "image-call")
			var images int
			for _, part := range visible.Content {
				if part.Media == nil {
					continue
				}
				images++
				if source := part.Media.Source; source.Kind != model.MediaSourceInline || source.Data != base64.StdEncoding.EncodeToString(fixture.png) {
					t.Fatalf("model saw different image: %+v", source)
				}
			}
			wantViews := int32(1)
			if mode == "goja" {
				wantViews = 2 // The nested receipt and the script's selected return.
				var selected int
				for _, event := range fixture.events(t) {
					if event.Type == session.EventTypeToolResult && event.Tool != nil && event.Tool.Name == codemode.ToolName {
						if event.Tool.ProcessingFailure != nil {
							t.Fatalf("outer receipt has a processing failure: %+v", event.Tool.ProcessingFailure)
						}
						for _, part := range event.Tool.Result.Content {
							if img, ok := part.(tool.Image); ok {
								selected++
								if img.Attachment != ref || len(img.Data) != 0 {
									t.Fatalf("outer receipt used a different source: %+v", img)
								}
							}
						}
					}
				}
				if selected != 1 {
					t.Fatalf("outer receipt images = %d, want one reference", selected)
				}
			}
			if visible.IsError || images != 1 || views.Load() != wantViews || referencedViews.Load() != wantViews {
				t.Fatalf("error=%v, model images=%d, views=%d, referenced views=%d, want %d views", visible.IsError, images, views.Load(), referencedViews.Load(), wantViews)
			}
		})
	}
}

func TestAttachmentSettlesAfterCancel(t *testing.T) {
	for _, mode := range []string{"native", "goja"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newAttachmentSettlementFixture(t, mode, nil)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			run := fixture.run(t, func(toolCtx context.Context) {
				close(entered)
				<-toolCtx.Done()
				close(cancelled)
				<-release
			})
			wait := func(signal <-chan struct{}, state string) {
				t.Helper()
				select {
				case <-signal:
				case <-ctx.Done():
					t.Fatalf("tool never %s: %v", state, ctx.Err())
				}
			}
			wait(entered, "entered")
			if result := run.Cancel(); !result.Cancelled() || result.Err != nil {
				t.Fatalf("Cancel = %+v", result)
			}
			wait(cancelled, "observed cancellation")
			close(release)
			if err := run.WaitCompletion(ctx); !errors.Is(err, context.Canceled) || !errorcode.Is(err, errorcode.Cancelled) {
				t.Fatalf("Run completion = %v, want cancelled", err)
			}
			fixture.checkReceipt(t)
			if len(fixture.model.requests) != 1 {
				t.Fatalf("model continued after cancellation: %d requests", len(fixture.model.requests))
			}
		})
	}
}

func TestAttachmentSettlementStoreFailure(t *testing.T) {
	for _, mode := range []string{"native", "goja"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newAttachmentSettlementFixture(t, mode, nil)
			if err := os.WriteFile(filepath.Join(fixture.config.RootDir, "attachments"), []byte("file in place of directory"), 0o600); err != nil {
				t.Fatal(err)
			}
			run := fixture.run(t, nil)
			err := run.WaitCompletion(t.Context())
			const diagnostic = "publish tool result attachments"
			if mode == "native" && (err == nil || !strings.Contains(err.Error(), diagnostic)) {
				t.Fatalf("Run completion = %v, want attachment persistence failure", err)
			}
			var scriptFailure, accepted int
			for _, event := range fixture.events(t) {
				if event.Tool == nil {
					continue
				}
				switch event.Tool.Name {
				case "Picture":
					if event.Type == session.EventTypeToolCall {
						accepted++
					}
					if event.Type == session.EventTypeToolResult && event.Tool.Outcome == session.ToolOutcomeSucceeded {
						t.Fatalf("image failure published a successful receipt: %+v", event.Tool)
					}
				case codemode.ToolName:
					if event.Type != session.EventTypeToolResult {
						continue
					}
					if event.Tool.Outcome == session.ToolOutcomeSucceeded || event.Tool.ErrorCode != string(errorcode.Unavailable) {
						t.Fatalf("script hid the storage failure: %+v", event.Tool)
					}
					scriptFailure++
				}
			}
			if mode == "goja" && (len(fixture.model.requests) != 2 || !errorDeliveryModelResult(t, fixture.model.requests[1], "image-call").IsError) {
				t.Fatalf("script did not report the settlement failure to the model")
			}
			if accepted != 1 || fixture.effects.Load() != 1 || (mode == "goja" && scriptFailure != 1) {
				t.Fatalf("accepted=%d, effects=%d, script failures=%d, Run completion=%v", accepted, fixture.effects.Load(), scriptFailure, err)
			}
		})
	}
}

type attachmentSettlementFixture struct {
	mode    string
	config  sessionfile.Config
	ref     session.SessionRef
	runtime *Runtime
	model   *mcpScriptModel
	png     []byte
	effects atomic.Int32
}

func newAttachmentSettlementFixture(t *testing.T, mode string, views []hook.ModelView) *attachmentSettlementFixture {
	t.Helper()
	config := sessionfile.Config{RootDir: t.TempDir(), Attachments: true}
	store := newLogicalFileStore(t, config)
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "user", Workspace: session.WorkspaceRef{CWD: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	rt, err := New(Config{Sessions: store, TaskStore: sessionfile.NewTaskStore(store), ImageCache: model.NewImagePreparationCache(), AgentFactory: chat.Factory{}, Hooks: hook.Set{ToolPolicy: mcpFixturePolicy(), ModelView: views}, DefaultPolicyMode: "mcp-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.ReleaseCodeModeEnvironments() })
	return &attachmentSettlementFixture{mode: mode, config: config, ref: active.SessionRef, runtime: rt, png: picture.Bytes()}
}

func (f *attachmentSettlementFixture) run(t *testing.T, afterEntry func(context.Context)) agent.Runner {
	t.Helper()
	firstCall := model.ToolCall{ID: "image-call", Name: "Picture", Args: `{}`}
	tools := []tool.Tool{tool.NamedTool{Def: tool.Definition{Name: "Picture"}, Invoke: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
		if f.effects.Add(1) == 1 && afterEntry != nil {
			afterEntry(ctx)
		}
		return tool.Result{Content: []tool.ContentBlock{tool.Image{Data: f.png, MIMEType: "image/png"}}}, nil
	}}}
	if f.mode == "goja" {
		firstCall.Name = codemode.ToolName
		firstCall.Args = `{"code":"return (await tools.call('Picture',{})).media[0];","yield_time_ms":60000}`
		tools = append(tools, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}})
	}
	f.model = &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{firstCall}
		}
		return nil
	}}
	run, err := f.runtime.Run(t.Context(), agent.RunRequest{SessionRef: f.ref, Input: "show the image", AgentSpec: agent.AgentSpec{Model: codeModeImageModel{f.model}, Tools: tools}})
	if err != nil {
		t.Fatal(err)
	}
	return run.Handle
}

func (f *attachmentSettlementFixture) events(t *testing.T) []*session.Event {
	t.Helper()
	reopened := newLogicalFileStore(t, f.config)
	page, err := reopened.EventsPage(t.Context(), session.EventPageRequest{SessionRef: f.ref, Visibility: session.EventPageAllDurable})
	if err != nil {
		t.Fatal(err)
	}
	return page.Events
}

func (f *attachmentSettlementFixture) checkReceipt(t *testing.T) model.AttachmentRef {
	t.Helper()
	var receipts, accepted int
	var ref model.AttachmentRef
	for _, event := range f.events(t) {
		if event.Tool == nil || event.Tool.Name != "Picture" {
			continue
		}
		if event.Type == session.EventTypeToolCall {
			accepted++
		}
		if event.Type != session.EventTypeToolResult {
			continue
		}
		receipts++
		if got := event.Tool; got.Outcome != session.ToolOutcomeSucceeded || got.Result == nil || len(got.Result.Content) != 1 {
			t.Fatalf("expected a successful image receipt, got %+v", got)
		}
		img, ok := event.Tool.Result.Content[0].(tool.Image)
		if !ok || img.Attachment.SessionID != f.ref.SessionID || len(img.Data) != 0 {
			t.Fatalf("expected a Session-owned image reference, got %#v", event.Tool.Result.Content)
		}
		ref = img.Attachment
		reopened := newLogicalFileStore(t, f.config)
		bytesOnDisk, err := model.LoadAttachment(t.Context(), reopened, f.ref.SessionID, ref)
		if err != nil || !bytes.Equal(bytesOnDisk, f.png) {
			t.Fatalf("reopened attachment = %d bytes, err=%v, want exact PNG", len(bytesOnDisk), err)
		}
	}
	if receipts != 1 || accepted != 1 || f.effects.Load() != 1 {
		t.Fatalf("receipts=%d accepted=%d effects=%d, want one of each", receipts, accepted, f.effects.Load())
	}
	return ref
}
