package brag

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
)

var planPageTmpl = template.Must(template.New("plan").Funcs(template.FuncMap{
	"secs":  func(f float64) string { return fmt.Sprintf("%.1fs", f) },
	"lines": func(s string) []string { return strings.Split(s, "\n") },
}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.P.Title}} — brag plan</title>
<link rel="stylesheet" href="{{.FontsURL}}">
<style>
:root{--bg:#f6f5f2;--card:#fff;--ink:#17181c;--muted:#6b6e76;--line:#e3e1db;--accent:{{.Accent}}}
@media (prefers-color-scheme:dark){:root{--bg:#121316;--card:#1b1d21;--ink:#ecedf0;--muted:#9a9ea8;--line:#2b2e34}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:16px/1.55 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif}
main{max-width:1040px;margin:0 auto;padding:40px 16px 80px}
.kicker{font:600 12px ui-monospace,monospace;letter-spacing:.12em;text-transform:uppercase;color:var(--muted)}
h1{font-size:clamp(28px,5vw,44px);line-height:1.1;margin:6px 0 12px;letter-spacing:-.02em}
h2{font-size:20px;margin:44px 0 14px;letter-spacing:-.01em}
.angle{font-size:18px;max-width:760px}
.meta{display:flex;flex-wrap:wrap;gap:8px;margin-top:16px}
.pill{border:1px solid var(--line);background:var(--card);border-radius:999px;padding:4px 12px;font-size:14px}
.status{border-color:var(--accent);font-weight:600}
.swatches{display:grid;grid-template-columns:repeat(auto-fill,minmax(150px,1fr));gap:12px}
.sw{background:var(--card);border:1px solid var(--line);border-radius:14px;overflow:hidden}
.sw .chip{height:84px}.sw div.t{padding:10px 12px;font-size:13px}.sw b{display:block;font:600 14px ui-monospace,monospace}
.fonts{display:grid;grid-template-columns:repeat(auto-fit,minmax(260px,1fr));gap:12px;margin-top:12px}
.font{background:var(--card);border:1px solid var(--line);border-radius:14px;padding:16px}
.font .s{font-size:30px;line-height:1.15;margin-top:4px}
.timeline{display:flex;height:14px;border-radius:7px;overflow:hidden;margin-bottom:18px;border:1px solid var(--line)}
.timeline span{display:block;height:100%;border-right:2px solid var(--bg)}
.scene{background:var(--card);border:1px solid var(--line);border-radius:16px;padding:18px 20px;margin-bottom:12px}
.scene header{display:flex;justify-content:space-between;gap:12px;align-items:baseline;flex-wrap:wrap}
.scene h3{margin:0;font-size:18px}.when{font:13px ui-monospace,monospace;color:var(--muted)}
blockquote{margin:12px 0;padding:12px 16px;border-left:4px solid var(--accent);background:var(--bg);border-radius:0 10px 10px 0;font-size:20px;font-weight:600}
dl{display:grid;grid-template-columns:120px 1fr;gap:6px 14px;margin:10px 0 0;font-size:15px}
dt{color:var(--muted)}dd{margin:0}
@media (max-width:560px){dl{grid-template-columns:1fr}dt{margin-top:6px}}
.tag{display:inline-block;font:12px ui-monospace,monospace;border:1px solid var(--line);border-radius:6px;padding:1px 6px;margin:0 4px 4px 0}
.templates{display:grid;grid-template-columns:repeat(auto-fill,minmax(230px,1fr));gap:14px}
.tpl{background:var(--card);border:1px solid var(--line);border-radius:14px;overflow:hidden;text-decoration:none;color:inherit;display:flex;flex-direction:column}
.tpl:hover{border-color:var(--accent)}
.tpl .img{aspect-ratio:16/9;background:var(--bg) center/cover no-repeat;display:flex;align-items:center;justify-content:center;color:var(--muted);font:12px ui-monospace,monospace}
.tpl .b{padding:12px 14px}.tpl b{font:600 14px ui-monospace,monospace}.tpl p{margin:6px 0 0;font-size:13px;color:var(--muted)}
.copy{background:var(--card);border:1px solid var(--line);border-radius:14px;padding:16px;font-size:17px}
footer{margin-top:40px;color:var(--muted);font-size:14px}
</style></head><body><main>
<div class="kicker">brag plan · awaiting your approval</div>
<h1>{{.P.Title}}</h1>
<p class="angle">{{.P.Angle}}</p>
<div class="meta"><span class="pill status">Reply in ChatGPT to approve or change</span><span class="pill">{{.P.Tone}}</span><span class="pill">{{.P.Format}} {{.W}}×{{.H}}</span><span class="pill">{{secs .Total}}</span><span class="pill">{{len .P.Scenes}} scenes</span></div>

<h2>Theme</h2>
<div class="swatches">{{range .P.Palette}}<div class="sw"><div class="chip" style="background:{{.Hex}}"></div><div class="t"><b>{{.Hex}}</b>{{.Role}}{{if .Name}} · {{.Name}}{{end}}</div></div>{{end}}</div>
<div class="fonts">
<div class="font"><div class="kicker">Display</div><div class="s" style="font-family:'{{.P.DisplayFont}}',ui-sans-serif;font-weight:700">{{.P.DisplayFont}}</div></div>
<div class="font"><div class="kicker">Body</div><div class="s" style="font-family:'{{.P.BodyFont}}',ui-sans-serif">{{.P.BodyFont}}</div></div>
</div>

<h2>Script &amp; storyboard</h2>
<div class="timeline">{{range $i, $s := .P.Scenes}}<span title="{{$s.Name}}" style="flex:{{$s.Duration}};background:{{index $.Strip $i}}"></span>{{end}}</div>
{{range $i, $s := .Scenes}}<section class="scene">
<header><h3>{{$s.N}}. {{$s.S.Name}}</h3><span class="when">{{secs $s.Start}} → {{secs $s.End}}</span></header>
{{if $s.S.Script}}<blockquote>{{range lines $s.S.Script}}{{.}}<br>{{end}}</blockquote>{{end}}
<dl>{{if $s.S.Narration}}<dt>Voiceover</dt><dd>&ldquo;{{$s.S.Narration}}&rdquo;</dd>{{end}}<dt>Visual</dt><dd>{{$s.S.Visual}}</dd><dt>Motion</dt><dd>{{$s.S.Motion}}</dd>
{{if $s.S.Templates}}<dt>Templates</dt><dd>{{range $s.S.Templates}}<span class="tag">{{.}}</span>{{end}}</dd>{{end}}
{{if $s.S.TransitionOut}}<dt>Transition out</dt><dd>{{$s.S.TransitionOut}}</dd>{{end}}
{{if $s.S.SFX}}<dt>Sound</dt><dd>{{$s.S.SFX}}</dd>{{end}}
{{if $s.S.ShowsProduct}}<dt>Product</dt><dd>Shows the real product</dd>{{end}}</dl>
</section>{{end}}

<h2>Templates</h2>
<div class="templates">{{range .Templates}}<a class="tpl" href="{{.Page}}" target="_blank" rel="noopener">
<div class="img" {{if .Preview}}style="background-image:url('{{.Preview}}')"{{end}}>{{if not .Preview}}open the catalog page to preview{{end}}</div>
<div class="b"><b>{{.Name}}</b><p>{{.Desc}}</p></div></a>{{end}}</div>

<h2>Audio</h2>
<p><b>Music:</b> {{.Music}}{{if .P.MusicNotes}}<br>{{.P.MusicNotes}}{{end}}</p>

<h2>Share copy (draft)</h2>
<div class="copy">{{.P.ShareCopy}}</div>
{{if .P.Avoid}}<h2>Deliberately avoiding</h2><ul>{{range .P.Avoid}}<li>{{.}}</li>{{end}}</ul>{{end}}
<footer>Generated by smart-control. Approve or ask for changes in your ChatGPT chat; building starts only after you approve.</footer>
</main></body></html>`))

type planPageScene struct {
	N          int
	S          planScene
	Start, End float64
}

type planPageTemplate struct {
	Name, Page, Preview, Desc string
}

func renderPlanHTML(p *planIn, c *catalog) string {
	w, h, _ := formatSize(p.Format)
	data := struct {
		P         *planIn
		W, H      int
		Total     float64
		Accent    template.CSS
		Strip     []template.CSS
		Scenes    []planPageScene
		Templates []planPageTemplate
		Music     string
		FontsURL  string
	}{P: p, W: w, H: h, Total: p.totalDuration(), Music: p.Music}
	var fams []string
	for i, f := range []string{p.DisplayFont, p.BodyFont} {
		if f != "" && (i == 0 || f != p.DisplayFont) {
			fams = append(fams, "family="+strings.ReplaceAll(f, " ", "+")+":wght@400;700")
		}
	}
	data.FontsURL = "https://fonts.googleapis.com/css2?" + strings.Join(fams, "&") + "&display=swap"
	data.Accent = template.CSS(cssColor(p.color("accent"), "#5b5bd6"))
	cycle := []string{p.color("accent"), p.color("accent2"), p.color("text"), p.color("muted"), p.color("surface")}
	t := 0.0
	for i, s := range p.Scenes {
		data.Scenes = append(data.Scenes, planPageScene{N: i + 1, S: s, Start: t, End: t + s.Duration})
		t += s.Duration
		col := ""
		for k := 0; k < len(cycle) && col == ""; k++ {
			col = cycle[(i+k)%len(cycle)]
		}
		data.Strip = append(data.Strip, template.CSS(cssColor(col, "#888888")))
	}
	for _, n := range p.templateNames() {
		tp := planPageTemplate{Name: n, Page: "https://hyperframes.heygen.com/catalog", Desc: ""}
		if c != nil {
			if it := c.get(n); it != nil {
				tp.Page = it.PageURL()
				tp.Desc = it.Use
				if tp.Desc == "" {
					tp.Desc = clip(it.Description, 140)
				}
				if c.previewOK(it) {
					tp.Preview = it.PreviewURL()
				}
			}
		}
		data.Templates = append(data.Templates, tp)
	}
	if data.Music == "" {
		data.Music = "none"
	}
	var buf bytes.Buffer
	if err := planPageTmpl.Execute(&buf, data); err != nil {
		return "<pre>" + template.HTMLEscapeString(err.Error()) + "</pre>"
	}
	return buf.String()
}

func cssColor(c, fallback string) string {
	if hexRe.MatchString(c) {
		return c
	}
	return fallback
}
