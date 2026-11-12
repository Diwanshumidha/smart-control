package brag

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// writeScaffold lays out a Hyperframes project from the approved plan: a host
// index.html that mounts one file per scene, palette and fonts as CSS
// variables, the music track, and per-scene stubs that already enter AND exit.
func (a *app) writeScaffold(r *run, p *planIn) ([]string, error) {
	comp := filepath.Join(r.Dir, "composition")
	if err := os.MkdirAll(filepath.Join(comp, "compositions"), 0o755); err != nil {
		return nil, err
	}
	w, h, _ := formatSize(p.Format)
	total := p.totalDuration()
	var files []string
	write := func(rel, content string, keep bool) error {
		path := filepath.Join(comp, filepath.FromSlash(rel))
		if keep {
			if _, err := os.Stat(path); err == nil {
				return nil
			}
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		files = append(files, "composition/"+rel)
		return os.WriteFile(path, []byte(content), 0o644)
	}

	if err := write("hyperframes.json", `{
  "$schema": "https://hyperframes.heygen.com/schema/hyperframes.json",
  "registry": "https://raw.githubusercontent.com/heygen-com/hyperframes/main/registry",
  "paths": { "blocks": "compositions", "components": "compositions/components", "assets": "assets" },
  "media": { "autoProxy": true },
  "authoringSkill": "hyperframes"
}
`, true); err != nil {
		return nil, err
	}
	if err := write("meta.json", fmt.Sprintf(`{ "id": %q, "name": %q, "createdAt": %q }
`, r.ID, p.Title, time.Now().UTC().Format(time.RFC3339)), true); err != nil {
		return nil, err
	}

	// Music
	music := ""
	if p.Music != "" && p.Music != "none" {
		if dir, err := a.ensureAssets(); err == nil {
			src := filepath.Join(dir, filepath.FromSlash(p.Music))
			if b, err := os.ReadFile(src); err == nil {
				music = "assets/" + filepath.Base(src)
				_ = os.MkdirAll(filepath.Join(comp, "assets"), 0o755)
				if err := os.WriteFile(filepath.Join(comp, filepath.FromSlash(music)), b, 0o644); err == nil {
					files = append(files, "composition/"+music)
				}
			}
		}
	}

	// Host
	var vars strings.Builder
	for _, c := range p.Palette {
		fmt.Fprintf(&vars, "      --%s: %s;\n", c.Role, strings.ToUpper(c.Hex))
	}
	fmt.Fprintf(&vars, "      --font-display: %q, sans-serif;\n      --font-body: %q, sans-serif;\n", p.DisplayFont, p.BodyFont)
	fontNote := ""
	for _, f := range []string{p.DisplayFont, p.BodyFont} {
		if !contains(bundled, f) {
			fontNote += fmt.Sprintf("\n    /* %s is not bundled: add an @font-face with the font file under assets/fonts/ */", f)
		}
	}
	var hosts strings.Builder
	t := 0.0
	for i, s := range p.Scenes {
		fmt.Fprintf(&hosts, `    <!-- %d. %s (%.2fs → %.2fs) -->
    <div id="%s" data-composition-id="%s" data-composition-src="compositions/%s.html"
         data-start="%s" data-duration="%s" data-track-index="%d" data-width="%d" data-height="%d"></div>
`, i+1, html.EscapeString(s.Name), t, t+s.Duration, s.ID, s.ID, s.ID, num(t), num(s.Duration), 1, w, h)
		t += s.Duration
	}
	audio := ""
	if music != "" {
		audio = fmt.Sprintf(`
    <!-- Music: %s -->
    <audio id="music" class="clip" data-start="0" data-duration="%s" data-track-index="10"
           data-volume="0.3" data-fade-in="0.6" data-fade-out="1.5" src="%s"></audio>
`, html.EscapeString(p.MusicNotes), num(total), music)
	}
	host := fmt.Sprintf(`<!doctype html>
<!-- %s — brag scaffold generated from the approved plan (plan.json).
     Scenes live in compositions/<id>.html. Overlap neighbouring scenes or add
     transition blocks between them as the plan says; every scene must exit on purpose. -->
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=%d, height=%d" />
    <script src="https://cdn.jsdelivr.net/npm/gsap@3.14.2/dist/gsap.min.js"></script>
    <style>
    :root {
%s    }%s
    html, body { margin: 0; width: %dpx; height: %dpx; overflow: hidden; background: var(--background); }
    #root { position: relative; width: %dpx; height: %dpx; overflow: hidden; font-family: var(--font-body); color: var(--text); }
    #backdrop { position: absolute; inset: 0; background: var(--background); }
%s    </style>
  </head>
  <body>
    <div id="root" data-composition-id="main" data-start="0" data-duration="%s" data-width="%d" data-height="%d">
    <div id="backdrop"></div>
%s%s    </div>
    <script>
      window.__timelines = window.__timelines || {};
      window.__timelines["main"] = gsap.timeline({ paused: true });
    </script>
  </body>
</html>
`, html.EscapeString(p.Title), w, h, vars.String(), fontNote, w, h, w, h, layoutCSS(p.Format), num(total), w, h, hosts.String(), audio)
	if err := write("index.html", host, false); err != nil {
		return nil, err
	}

	for i, s := range p.Scenes {
		if err := write("compositions/"+s.ID+".html", sceneStub(i+1, &s, w, h), false); err != nil {
			return nil, err
		}
	}
	return files, nil
}

func sceneStub(n int, s *planScene, w, h int) string {
	id := s.ID
	title := html.EscapeString(s.Script)
	if title == "" {
		title = html.EscapeString(s.Name)
	}
	title = strings.ReplaceAll(title, "\n", "<br />")
	exitAt := s.Duration - 0.45
	if exitAt < 0.6 {
		exitAt = s.Duration * 0.6
	}
	comment := func(label, v string) string {
		if v == "" {
			return ""
		}
		return fmt.Sprintf("\n       %s: %s", label, strings.ReplaceAll(v, "--", "—"))
	}
	return fmt.Sprintf(`<template>
  <!-- SCENE %d · %s · %.2fs   (STUB: replace with the real scene from the plan)%s%s%s%s%s
  -->
  <style>
    #%s { position: absolute; inset: 0; overflow: hidden; }
  </style>
  <div id="%s" data-composition-id="%s" data-width="%d" data-height="%d">
    <!-- Layout: keep .lay (zones defined in index.html) so this scene lines up with the others.
         The product UI goes in .lay-stage, rebuilt in HTML/CSS from the product's source (captures are
         only a reference; screenshots only if the user asked for them);
         use .lay-2 / .lay-1 to drop zones, never hand-placed percentages. -->
    <div class="lay lay-2">
      <h1 id="%s-title" class="lay-caption">%s</h1>
      <div id="%s-stage" class="lay-stage"></div>
    </div>
  </div>
  <script>
    (function () {
      const tl = gsap.timeline({ paused: true });
      // Enter
      tl.fromTo("#%s-title", { opacity: 0, y: 40, filter: "blur(8px)" },
        { opacity: 1, y: 0, filter: "blur(0px)", duration: 0.5, ease: "power3.out" }, 0.1);
      // Exit — every scene leaves on purpose; swap for the planned transition.
      tl.fromTo("#%s-title", { opacity: 1, y: 0 },
        { opacity: 0, y: -40, duration: 0.35, ease: "power2.in", immediateRender: false }, %s);
      window.__timelines["%s"] = tl;
    })();
  </script>
</template>
`, n, strings.ReplaceAll(s.Name, "--", "—"), s.Duration,
		comment("Visual", s.Visual), comment("Motion", s.Motion), comment("Templates", strings.Join(s.Templates, ", ")),
		comment("Transition out", s.TransitionOut), comment("Sound", s.SFX),
		id, id, id, w, h, id, title, id, id, id, num(exitAt), id)
}

func num(f float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", f), "0"), ".")
}
