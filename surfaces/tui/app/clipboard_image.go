package tuiapp

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const clipboardImageCommandTimeout = 8 * time.Second

func defaultPasteClipboardImage() ([]string, string, error) {
	if isWSL() {
		return pasteWindowsClipboardImage(true)
	}
	switch clipboardGOOS {
	case "windows":
		return pasteWindowsClipboardImage(false)
	case "darwin":
		return pasteMacClipboardImage()
	case "linux":
		return pasteLinuxClipboardImage()
	default:
		return nil, "", fmt.Errorf("clipboard image paste is unsupported on %s", clipboardGOOS)
	}
}

func pasteWindowsClipboardImage(wsl bool) ([]string, string, error) {
	const script = `
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
$image = [System.Windows.Forms.Clipboard]::GetImage()
if ($null -eq $image) { exit 0 }
$dir = [System.IO.Path]::Combine([System.IO.Path]::GetTempPath(), 'caelis-clipboard')
[System.IO.Directory]::CreateDirectory($dir) | Out-Null
$name = ('clipboard-{0:yyyyMMdd-HHmmss-ffff}-{1}.png' -f [DateTime]::Now, [System.Diagnostics.Process]::GetCurrentProcess().Id)
$path = [System.IO.Path]::Combine($dir, $name)
try {
  $image.Save($path, [System.Drawing.Imaging.ImageFormat]::Png)
} finally {
  $image.Dispose()
}
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new()
[Console]::WriteLine($path)
`
	out, err := runClipboardOutputCommand(clipboardCommand{
		name:    "powershell.exe",
		args:    []string{"-NoProfile", "-NonInteractive", "-STA", "-ExecutionPolicy", "Bypass", "-Command", script},
		label:   "Windows clipboard image reader",
		timeout: clipboardImageCommandTimeout,
	})
	if err != nil {
		return nil, "", err
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return nil, "", nil
	}
	if wsl {
		converted, err := runClipboardOutputCommand(clipboardCommand{name: "wslpath", args: []string{"-u", path}, label: "WSL clipboard image path converter", timeout: clipboardImageCommandTimeout})
		if err != nil {
			return nil, "", err
		}
		path = strings.TrimSpace(string(converted))
	}
	if path == "" {
		return nil, "", nil
	}
	return []string{path}, path, nil
}

func pasteMacClipboardImage() ([]string, string, error) {
	path, err := newClipboardImagePath(".png")
	if err != nil {
		return nil, "", err
	}
	const script = `
on run argv
  set outPath to item 1 of argv
  try
    set pngData to the clipboard as «class PNGf»
  on error
    return ""
  end try
  set outFile to POSIX file outPath
  set fileRef to open for access outFile with write permission
  try
    set eof of fileRef to 0
    write pngData to fileRef
  on error errMsg number errNum
    close access fileRef
    error errMsg number errNum
  end try
  close access fileRef
  return outPath
end run
`
	out, err := runClipboardOutputCommand(clipboardCommand{name: "osascript", args: []string{"-e", script, path}, label: "macOS clipboard image reader", timeout: clipboardImageCommandTimeout})
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(string(out)) == "" {
		_ = os.Remove(path)
		return nil, "", nil
	}
	return []string{path}, path, nil
}

func pasteLinuxClipboardImage() ([]string, string, error) {
	if clipboardGetenv("WAYLAND_DISPLAY") != "" && commandAvailable("wl-paste") {
		if mimeType, ok := firstClipboardImageMime(wlPasteTargets); ok {
			out, err := runClipboardOutputCommand(clipboardCommand{name: "wl-paste", args: []string{"--type", mimeType, "--no-newline"}, label: "Wayland clipboard image reader", timeout: clipboardImageCommandTimeout})
			if err != nil {
				return nil, "", err
			}
			return writeClipboardImageBytes(out, mimeType)
		}
		return nil, "", nil
	}
	if commandAvailable("xclip") {
		if mimeType, ok := firstClipboardImageMime(xclipTargets); ok {
			out, err := runClipboardOutputCommand(clipboardCommand{name: "xclip", args: []string{"-selection", "clipboard", "-t", mimeType, "-o"}, label: "X11 clipboard image reader", timeout: clipboardImageCommandTimeout})
			if err != nil {
				return nil, "", err
			}
			return writeClipboardImageBytes(out, mimeType)
		}
		return nil, "", nil
	}
	return nil, "", fmt.Errorf("clipboard image paste requires wl-paste or xclip")
}

func wlPasteTargets() (string, error) {
	out, err := runClipboardOutputCommand(clipboardCommand{name: "wl-paste", args: []string{"--list-types"}, label: "Wayland clipboard target reader", timeout: clipboardImageCommandTimeout})
	return string(out), err
}

func xclipTargets() (string, error) {
	out, err := runClipboardOutputCommand(clipboardCommand{name: "xclip", args: []string{"-selection", "clipboard", "-t", "TARGETS", "-o"}, label: "X11 clipboard target reader", timeout: clipboardImageCommandTimeout})
	return string(out), err
}

func firstClipboardImageMime(targets func() (string, error)) (string, bool) {
	raw, err := targets()
	if err != nil {
		return "", false
	}
	available := map[string]struct{}{}
	for _, line := range strings.Fields(raw) {
		available[strings.ToLower(strings.TrimSpace(line))] = struct{}{}
	}
	for _, mimeType := range []string{"image/png", "image/jpeg", "image/webp", "image/gif"} {
		if _, ok := available[mimeType]; ok {
			return mimeType, true
		}
	}
	return "", false
}

func writeClipboardImageBytes(data []byte, mimeType string) ([]string, string, error) {
	if len(data) == 0 {
		return nil, "", nil
	}
	path, err := newClipboardImagePath(imageExtensionForMime(mimeType))
	if err != nil {
		return nil, "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, "", err
	}
	return []string{path}, path, nil
}

func newClipboardImagePath(ext string) (string, error) {
	ext = strings.TrimSpace(ext)
	if ext == "" {
		ext = ".png"
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	dir := filepath.Join(os.TempDir(), "caelis-clipboard")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("clipboard-%s-%d%s", time.Now().UTC().Format("20060102-150405.000000000"), os.Getpid(), ext)
	return filepath.Join(dir, name), nil
}

func imageExtensionForMime(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".png"
	}
}

func commandAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
