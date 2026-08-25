package tui

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/context-labs/loopy/internal/config"
)

// imageExts are the clipboard image formats we accept, in preference order.
var imageExts = []string{"png", "jpg", "jpeg", "gif", "webp", "bmp"}

// readClipboardImage returns image bytes and their format extension from the
// system clipboard, or ("", nil, nil) when the clipboard holds no image.
// Tries wl-paste (Wayland), xclip then xsel (X11), pngpaste (macOS), and
// PowerShell (Windows/WSL).
func readClipboardImage() (string, []byte, error) {
	for _, tool := range []struct {
		name string
		fn   func() (string, []byte, error)
	}{
		{"wl-paste", wlPasteImage},
		{"xclip", xclipImage},
		{"xsel", xselImage},
		{"pngpaste", pngpasteImage},
		{"powershell.exe", powershellImage},
	} {
		if _, err := exec.LookPath(tool.name); err != nil {
			continue
		}
		ext, data, err := tool.fn()
		if err != nil || data != nil {
			return ext, data, err
		}
	}
	return "", nil, nil
}

// hasImageType reports whether types contains an image MIME type.
func hasImageType(types []byte) (string, bool) {
	for _, line := range strings.Split(string(types), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "image/") {
			ext := strings.TrimPrefix(line, "image/")
			for _, e := range imageExts {
				if e == ext {
					return ext, true
				}
			}
			return ext, true // unknown image/*: keep its subtype as extension
		}
	}
	return "", false
}

func run(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

func wlPasteImage() (string, []byte, error) {
	types, err := run("wl-paste", "--list-types")
	if err != nil {
		return "", nil, err
	}
	ext, ok := hasImageType(types)
	if !ok {
		return "", nil, nil
	}
	data, err := run("wl-paste", "--type", "image/"+ext)
	return ext, data, err
}

func xclipImage() (string, []byte, error) {
	targets, err := run("xclip", "-selection", "clipboard", "-o", "-t", "TARGETS")
	if err != nil {
		return "", nil, err
	}
	ext, ok := hasImageType(targets)
	if !ok {
		return "", nil, nil
	}
	data, err := run("xclip", "-selection", "clipboard", "-o", "-t", "image/"+ext)
	return ext, data, err
}

func xselImage() (string, []byte, error) {
	// xsel has no TARGETS listing; probe for image output directly.
	data, err := run("xsel", "--clipboard", "--output", "--target", "image/png")
	if err != nil || len(data) == 0 {
		return "", nil, err
	}
	return "png", data, nil
}

func pngpasteImage() (string, []byte, error) {
	tmp, err := os.CreateTemp("", "loopy-paste-*.png")
	if err != nil {
		return "", nil, err
	}
	tmp.Close()
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := exec.Command("pngpaste", tmp.Name()).Run(); err != nil {
		return "", nil, nil // no image on the clipboard
	}
	data, err := os.ReadFile(tmp.Name())
	if len(data) == 0 {
		return "", nil, err
	}
	return "png", data, err
}

func powershellImage() (string, []byte, error) {
	const script = `Add-Type -AssemblyName System.Windows.Forms; ` +
		`$img = [Windows.Forms.Clipboard]::GetImage(); ` +
		`if ($img -eq $null) { exit 1 }; ` +
		`$img.Save([Console]::OpenStandardOutput(), [System.Drawing.Imaging.ImageFormat]::Png)`
	data, err := exec.Command("powershell.exe", "-NoProfile", "-Command", script).Output()
	if err != nil || len(data) == 0 {
		return "", nil, err
	}
	return "png", data, nil
}

// saveClipboardImage writes data to ~/.loopy/pastes/ and returns the path.
func saveClipboardImage(ext string, data []byte) (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "pastes")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	b := make([]byte, 3)
	rand.Read(b)
	name := fmt.Sprintf("%s-%s.%s", time.Now().Format("20060102-150405"), hex.EncodeToString(b), ext)
	path := filepath.Join(dir, name)
	return path, os.WriteFile(path, data, 0o600)
}

// pasteImageCmd reads the clipboard image off the UI thread.
func pasteImageCmd() tea.Msg {
	ext, data, err := readClipboardImage()
	if err != nil {
		return imageMsg{err: err}
	}
	if data == nil {
		return imageMsg{}
	}
	path, err := saveClipboardImage(ext, data)
	if err != nil {
		return imageMsg{err: err}
	}
	return imageMsg{path: path}
}
