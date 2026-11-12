package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

// prepareDisplay picks the GTK backend before Wails initialises GTK and
// returns the page zoom the UI needs.
//
// WebKitGTK (GTK3) on native Wayland can't do fractional scaling: at a 1.2
// monitor scale it renders every frame at 2× and the compositor downsamples,
// which measured ~36 fps on a 144 Hz panel (NVIDIA + Hyprland). Through
// XWayland the same UI renders at 1× and holds the full refresh rate. So on
// Wayland we default to XWayland; SMART_CONTROL_WAYLAND=1 opts back into
// native Wayland, and an explicit GDK_BACKEND=x11/wayland is respected.
//
// With Hyprland's xwayland:force_zero_scaling, XWayland windows are not
// upscaled (sharp, but small), so the page is zoomed by the monitor scale.
func prepareDisplay() (zoom float64) {
	zoom = 1
	if os.Getenv("WAYLAND_DISPLAY") == "" || os.Getenv("SMART_CONTROL_WAYLAND") == "1" {
		return zoom
	}
	switch os.Getenv("GDK_BACKEND") {
	case "wayland":
		return zoom // explicitly requested
	case "x11":
	default:
		if os.Getenv("DISPLAY") == "" {
			return zoom // no XWayland available
		}
		_ = os.Setenv("GDK_BACKEND", "x11")
		log.Print("display: using XWayland for smooth rendering (SMART_CONTROL_WAYLAND=1 for native Wayland)")
	}
	if os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "" && hyprZeroScaling() {
		if s := hyprFocusedScale(); s > 1 {
			zoom = s
		}
	}
	return zoom
}

func hyprctl(args ...string) []byte {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "hyprctl", append(args, "-j")...).Output()
	if err != nil {
		return nil
	}
	return out
}

func hyprZeroScaling() bool {
	// Hyprland reports this as {"bool": true}; older versions as {"int": 1}.
	var opt struct {
		Bool *bool `json:"bool"`
		Int  int   `json:"int"`
	}
	if json.Unmarshal(hyprctl("getoption", "xwayland:force_zero_scaling"), &opt) != nil {
		return false
	}
	return (opt.Bool != nil && *opt.Bool) || opt.Int == 1
}

func hyprFocusedScale() float64 {
	var mons []struct {
		Scale   float64 `json:"scale"`
		Focused bool    `json:"focused"`
	}
	if json.Unmarshal(hyprctl("monitors"), &mons) != nil {
		return 1
	}
	for _, m := range mons {
		if m.Focused {
			return m.Scale
		}
	}
	if len(mons) > 0 {
		return mons[0].Scale
	}
	return 1
}

// floatOnActiveWorkspace makes the approval window float, centred, on the
// workspace you're looking at (Hyprland). It runs again whenever a new
// approval arrives, pulling the window to wherever you are now.
func floatOnActiveWorkspace() {
	if os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") == "" {
		return
	}
	go func() {
		var ws struct {
			ID int `json:"id"`
		}
		_ = json.Unmarshal(hyprctl("activeworkspace"), &ws)
		// By pid: under XWayland the window class doesn't follow ProgramName.
		win := fmt.Sprintf("pid:%d", os.Getpid())
		batch := fmt.Sprintf("dispatch setfloating %[1]s ; dispatch movetoworkspacesilent %[2]d,%[1]s ; dispatch focuswindow %[1]s ; dispatch centerwindow 1", win, ws.ID)
		// The window may not be mapped yet on the first call.
		for _, wait := range []time.Duration{0, 250 * time.Millisecond, 750 * time.Millisecond} {
			time.Sleep(wait)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			out, err := exec.CommandContext(ctx, "hyprctl", "--batch", batch).CombinedOutput()
			cancel()
			if err == nil && !strings.Contains(strings.ToLower(string(out)), "no such window") && !strings.Contains(strings.ToLower(string(out)), "not found") {
				return
			}
		}
	}()
}
