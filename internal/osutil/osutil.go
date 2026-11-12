// Package osutil holds small desktop integrations: opening files with the
// default app, revealing them in the file manager, and probing what the
// system's media stack can play.
package osutil

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

// Open opens path with the user's default application (video player, image viewer).
func Open(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return startDetached(cmd)
}

// Reveal shows path in the OS file manager.
func Reveal(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", "-R", path)
	case "windows":
		cmd = exec.Command("explorer", "/select,", path)
	default:
		cmd = exec.Command("xdg-open", filepath.Dir(path))
	}
	return startDetached(cmd)
}

func startDetached(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// MediaSupport says whether the desktop webview can play audio/video inline.
type MediaSupport struct {
	Supported bool     `json:"supported"`
	Missing   []string `json:"missing,omitempty"` // GStreamer elements not found
	Hint      string   `json:"hint,omitempty"`
}

var (
	mediaOnce sync.Once
	media     MediaSupport
)

// InlineMedia reports (once, cached) whether WebKitGTK can play media.
//
// On Linux WebKitGTK plays <video>/<audio> through GStreamer. Without an audio
// sink (gst-plugins-good) the web process aborts as soon as a media element is
// created (observed: "GStreamer element autoaudiosink not found" followed by a
// SIGABRT ~1s after opening a screen with a video), which freezes the window.
// Other platforms' webviews ship their own media stack.
func InlineMedia() MediaSupport {
	mediaOnce.Do(func() {
		if runtime.GOOS != "linux" {
			media = MediaSupport{Supported: true}
			return
		}
		media = probeGStreamer()
	})
	return media
}

func probeGStreamer() MediaSupport {
	inspect, err := exec.LookPath("gst-inspect-1.0")
	if err != nil {
		return MediaSupport{Missing: []string{"gst-inspect-1.0"}, Hint: hint}
	}
	has := func(el string) bool { return exec.Command(inspect, "--exists", el).Run() == nil }
	var missing []string
	for _, el := range []string{"playbin", "autoaudiosink", "autovideosink"} {
		if !has(el) {
			missing = append(missing, el)
		}
	}
	h264 := false
	for _, el := range []string{"avdec_h264", "openh264dec", "vah264dec", "nvh264dec", "vaapih264dec"} {
		if has(el) {
			h264 = true
			break
		}
	}
	if !h264 {
		missing = append(missing, "h264 decoder (avdec_h264)")
	}
	if len(missing) > 0 {
		return MediaSupport{Missing: missing, Hint: hint}
	}
	return MediaSupport{Supported: true}
}

const hint = "Videos open in your default player instead. For in-app playback install the GStreamer plugins " +
	"(Arch: gst-plugins-base gst-plugins-good gst-libav; Debian/Ubuntu: gstreamer1.0-plugins-base gstreamer1.0-plugins-good gstreamer1.0-libav)."
