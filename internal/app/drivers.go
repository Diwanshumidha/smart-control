package app

// Browser drivers register themselves with internal/browser on import. To add
// one (e.g. a remote CDP session or a WebDriver BiDi backend), create a
// package under internal/browser/ that calls browser.Register in init and
// import it here. Voice providers work the same way with internal/voice.
import (
	_ "github.com/Diwanshumidha/smart-control/internal/browser/cdp"
	_ "github.com/Diwanshumidha/smart-control/internal/browser/playwright"
	_ "github.com/Diwanshumidha/smart-control/internal/browser/shared"

	_ "github.com/Diwanshumidha/smart-control/internal/voice/kokoro"
)
