package brag

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// layoutMetrics are the fixed zones every scene shares, so captions, the
// product and footnotes land in the same place from cut to cut.
type layoutMetrics struct {
	SafeTop, SafeBottom, SafeX int // keep clear of the feed's overlays
	Gap                        int // between zones
	CaptionH, FootH            int // fixed zone heights
	CaptionSize, SubSize       int // font sizes, px
	FootSize                   int
	SplitGap                   int // landscape copy/stage columns
}

func layoutFor(format string) layoutMetrics {
	switch format {
	case "vertical": // LinkedIn/Reels/Shorts put UI over the bottom ~12% and the top edge
		return layoutMetrics{SafeTop: 120, SafeBottom: 220, SafeX: 72, Gap: 40, CaptionH: 300, FootH: 120, CaptionSize: 88, SubSize: 44, FootSize: 38}
	case "square":
		return layoutMetrics{SafeTop: 72, SafeBottom: 96, SafeX: 72, Gap: 32, CaptionH: 200, FootH: 80, CaptionSize: 72, SubSize: 38, FootSize: 32}
	}
	return layoutMetrics{SafeTop: 72, SafeBottom: 84, SafeX: 120, Gap: 36, CaptionH: 200, FootH: 80, CaptionSize: 84, SubSize: 40, FootSize: 32, SplitGap: 96}
}

// layoutCSS is the shared layout system written into the host index.html.
// Scene files use these classes instead of hand-placed percentages.
func layoutCSS(format string) string {
	m := layoutFor(format)
	css := fmt.Sprintf(`
    /* ---- Layout system (brag scaffold). Every scene uses it; don't hand-place with percentages. ----
       .lay        caption zone / stage / footnote zone, inside the safe area, centred on one axis
       .lay.lay-2  caption + stage (no footnote)        .lay.lay-1  stage only (product fills the safe area)
       .lay-split  landscape: copy column left, stage right (both vertically centred)
       .lay-screen a screenshot (only when the user asked for screenshots): keeps its shape, never larger than its pixels
       Zone children must stay in flow: no class="clip" or position:absolute on them (that pulls them
       out of the grid); time things with GSAP instead. */
    :root {
      --safe-top: %dpx; --safe-bottom: %dpx; --safe-x: %dpx; --zone-gap: %dpx;
      --caption-h: %dpx; --foot-h: %dpx;
      --caption-size: %dpx; --sub-size: %dpx; --foot-size: %dpx;
    }
    .lay {
      position: absolute; top: var(--safe-top); bottom: var(--safe-bottom); left: var(--safe-x); right: var(--safe-x);
      display: grid; grid-template-rows: var(--caption-h) minmax(0, 1fr) var(--foot-h); row-gap: var(--zone-gap);
      justify-items: center; text-align: center;
    }
    .lay.lay-2 { grid-template-rows: var(--caption-h) minmax(0, 1fr); }
    .lay.lay-1 { grid-template-rows: minmax(0, 1fr); }
    .lay > * { min-width: 0; min-height: 0; max-width: 100%%; }
    .lay-caption {
      align-self: end; margin: 0; font-family: var(--font-display); font-weight: 700;
      font-size: var(--caption-size); line-height: 1.04; letter-spacing: -0.02em; color: var(--text);
      text-wrap: balance;
    }
    .lay-sub { margin: 16px 0 0; font-size: var(--sub-size); line-height: 1.2; font-weight: 500; color: var(--muted, var(--text)); }
    .lay-stage { position: relative; width: 100%%; height: 100%%; display: flex; align-items: center; justify-content: center; }
    .lay-screen { display: block; max-width: 100%%; max-height: 100%%; width: auto; height: auto; }
    .lay-foot {
      align-self: start; margin: 0; max-width: 24em; font-size: var(--foot-size); line-height: 1.25;
      color: var(--muted, var(--text));
    }
`, m.SafeTop, m.SafeBottom, m.SafeX, m.Gap, m.CaptionH, m.FootH, m.CaptionSize, m.SubSize, m.FootSize)
	if m.SplitGap > 0 {
		css += fmt.Sprintf(`    .lay-split {
      position: absolute; top: var(--safe-top); bottom: var(--safe-bottom); left: var(--safe-x); right: var(--safe-x);
      display: grid; grid-template-columns: minmax(0, 0.85fr) minmax(0, 1.15fr); column-gap: %dpx;
      align-items: center; text-align: left;
    }
    .lay-split > * { min-width: 0; min-height: 0; }
    .lay-split .lay-caption { align-self: auto; }
    .lay-split .lay-stage { height: 100%%; }
`, m.SplitGap)
	}
	return css
}

var layClassRe = regexp.MustCompile(`class="[^"]*\blay(-split)?\b`)

// layoutProblems lists scene files that don't use the scaffold's layout
// system. Runs scaffolded before it existed are skipped.
func layoutProblems(r *run) []string {
	comp := filepath.Join(r.Dir, "composition")
	host, err := os.ReadFile(filepath.Join(comp, "index.html"))
	if err != nil || !strings.Contains(string(host), ".lay {") {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(comp, "compositions", "*.html"))
	var out []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil || layClassRe.Match(b) {
			continue
		}
		out = append(out, fmt.Sprintf("compositions/%s doesn't use the layout system: put its caption, product and footnote in a .lay (or .lay-split) container so it lines up with the other scenes", filepath.Base(f)))
	}
	return out
}
