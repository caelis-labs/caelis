package appserveradapter

import (
	"bytes"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func BenchmarkImageContentPartFromAttachment(b *testing.B) {
	sizes := []struct {
		name  string
		bytes int
	}{
		{name: "1MiB", bytes: 1 << 20},
		{name: "5MiB", bytes: 5 << 20},
		{name: "20MB", bytes: appserver.MaxPromptImageBytes},
	}
	for _, size := range sizes {
		b.Run(size.name, func(b *testing.B) {
			workspace := b.TempDir()
			imagePath := filepath.Join(workspace, "bench.png")
			if err := writeSizedPNG(imagePath, size.bytes); err != nil {
				b.Fatal(err)
			}
			item := controlprompt.Attachment{Name: "bench.png"}
			b.ReportAllocs()
			b.SetBytes(int64(size.bytes))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, _, err := imageContentPartFromAttachment(item, workspace); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkEncodeImageAttachmentBase64(b *testing.B) {
	sizes := []int{1 << 20, 5 << 20, appserver.MaxPromptImageBytes}
	for _, size := range sizes {
		b.Run(fmt.Sprintf("%dB", size), func(b *testing.B) {
			payload := make([]byte, size)
			copy(payload, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
			b.ReportAllocs()
			b.SetBytes(int64(size))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, _, err := encodeImageAttachmentBase64(bytes.NewReader(payload), "bench.png", int64(size)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
