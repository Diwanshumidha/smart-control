package app

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/Diwanshumidha/smart-control/internal/browser"
	"github.com/Diwanshumidha/smart-control/internal/config"
	"github.com/Diwanshumidha/smart-control/internal/osutil"
	"github.com/Diwanshumidha/smart-control/internal/toolsets/brag"
	"github.com/Diwanshumidha/smart-control/internal/voice"
)

// Doctor reports which external tools are available.
func (a *App) Doctor(ctx context.Context) string {
	return Doctor(ctx, a.Version, a.Config)
}

func Doctor(ctx context.Context, version string, cfg *config.Config) string {
	bravePath := cfg.Browser.Path
	var b strings.Builder
	mark := func(ok bool) string {
		if ok {
			return "OK  "
		}
		return "MISS"
	}
	brave := browser.FindBrave(bravePath)
	fmt.Fprintf(&b, "[%s] %-22s %s\n", mark(brave != ""), "brave (browser tools)", brave)
	_, err := exec.LookPath("rg")
	fmt.Fprintf(&b, "[%s] %-22s %s\n", mark(err == nil), "ripgrep (grep, optional)", "falls back to a built-in search")
	if m := osutil.InlineMedia(); m.Supported {
		fmt.Fprintf(&b, "[%s] %-22s %s\n", mark(true), "in-app video playback", "")
	} else {
		fmt.Fprintf(&b, "[%s] %-22s missing %s. %s\n", mark(false), "in-app video playback", strings.Join(m.Missing, ", "), m.Hint)
	}
	b.WriteString("\n--- brag (launch videos) ---\n")
	b.WriteString(brag.Doctor(ctx, version))
	for _, p := range voice.Providers(voice.Settings{DataDir: cfg.DataDir, Voice: cfg.Voice}) {
		active := ""
		if p.Name == cfg.Voice.Provider {
			active = " (selected)"
		}
		fmt.Fprintf(&b, "[%s] %-22s %s\n", mark(p.Available), "voice: "+p.Name+active, p.Detail)
	}
	return b.String()
}
