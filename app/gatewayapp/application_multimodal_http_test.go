package gatewayapp_test

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// multimodalHTTPModel observes real adapter requests. In Chat Completions an
// image from a tool is bridged into a user message after its tool_call_id; the
// fixture terminates when it sees the tool message, not when the last role is tool.
func tinyApplicationPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(1, 0, color.RGBA{B: 255, A: 255})
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
