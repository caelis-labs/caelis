package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/app/gatewayapp"
)

func TestDoctorAttachmentPrunePreviewAndApply(t *testing.T) {
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions"), Attachments: true})
	ref, err := store.StoreAttachment(t.Context(), "session", []byte("fixture"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := gatewayapp.Config{AppName: "caelis", UserID: "local-user", StoreDir: root, WorkspaceKey: t.TempDir(), WorkspaceCWD: t.TempDir()}
	options := productClientOptions{Mode: productClientModeManaged, AppName: cfg.AppName, UserID: cfg.UserID, StoreDir: root, WorkspaceKey: cfg.WorkspaceKey, WorkspaceCWD: cfg.WorkspaceCWD}
	cutoff := time.Now().Add(time.Hour).Format(time.RFC3339)
	for _, apply := range []bool{false, true} {
		var out bytes.Buffer
		if err := runDoctorAttachmentPrune(t.Context(), cfg, options, "session", cutoff, apply, &out); err != nil {
			t.Fatal(err)
		}
		var report struct {
			Applied bool
			Files   int
			Bytes   int64
		}
		if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.Applied != apply || report.Files != 1 || report.Bytes != ref.Size {
			t.Fatalf("doctor output=%s err=%v", out.Bytes(), err)
		}
		f, err := store.OpenAttachment(t.Context(), "session", ref)
		if f != nil {
			_ = f.Close()
		}
		if apply && !errors.Is(err, model.ErrAttachmentUnavailable) || !apply && err != nil {
			t.Fatalf("open after apply=%v: %v", apply, err)
		}
	}
}
