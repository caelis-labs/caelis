package appserver

import (
	"encoding/base64"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func BenchmarkDecodedPromptImageByteCount(b *testing.B) {
	sizes := []struct {
		name  string
		bytes int
	}{
		{name: "1MiB", bytes: 1 << 20},
		{name: "5MiB", bytes: 5 << 20},
		{name: "20MB", bytes: MaxPromptImageBytes},
	}
	for _, size := range sizes {
		b.Run(size.name, func(b *testing.B) {
			data := base64.StdEncoding.EncodeToString(make([]byte, size.bytes))
			b.ReportAllocs()
			b.SetBytes(int64(size.bytes))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				n, err := decodedPromptImageByteCount(data)
				if err != nil {
					b.Fatal(err)
				}
				if n != size.bytes {
					b.Fatalf("count = %d, want %d", n, size.bytes)
				}
			}
		})
	}
}

func BenchmarkValidatePromptContentImages(b *testing.B) {
	sizes := []struct {
		name  string
		bytes int
	}{
		{name: "1MiB", bytes: 1 << 20},
		{name: "5MiB", bytes: 5 << 20},
		{name: "20MB", bytes: MaxPromptImageBytes},
	}
	for _, size := range sizes {
		b.Run(size.name, func(b *testing.B) {
			parts := []model.ContentPart{{
				Type:     model.ContentPartImage,
				MimeType: "image/png",
				Data:     base64.StdEncoding.EncodeToString(make([]byte, size.bytes)),
				FileName: "bench.png",
			}}
			b.ReportAllocs()
			b.SetBytes(int64(size.bytes))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := validatePromptContent("prompt", "", parts); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
