package brag

import (
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// extractSVG pulls the first inline <svg>…</svg> out of a component source
// (JSX/TSX, Vue, Svelte, Astro, HTML) and turns it into a standalone SVG file:
// JSX comments, spreads and className are dropped, {"literal"} and {2} values
// become plain attributes, camelCase presentation attributes become
// kebab-case. Dynamic parts it can't resolve are dropped and reported in
// notes, so the caller knows what to check.
func extractSVG(src string) (svg string, notes []string, err error) {
	start := strings.Index(src, "<svg")
	if start < 0 {
		return "", nil, fmt.Errorf("no inline <svg> in this file")
	}
	end, err := svgEnd(src, start)
	if err != nil {
		return "", nil, err
	}
	out, notes := stripExpressions(src[start:end])
	out = classAttr.ReplaceAllString(out, "")
	out = tagRe.ReplaceAllStringFunc(out, func(tag string) string {
		return camelAttr.ReplaceAllStringFunc(tag, func(m string) string {
			name := strings.TrimSuffix(strings.TrimSpace(m), "=")
			if svgCamel[name] {
				return m
			}
			if name == "xlinkHref" {
				return " xlink:href="
			}
			return " " + kebab(name) + "="
		})
	})
	if !strings.Contains(out[:strings.Index(out, ">")], "xmlns=") {
		out = strings.Replace(out, "<svg", `<svg xmlns="http://www.w3.org/2000/svg"`, 1)
	}
	if strings.Contains(out, "xlink:href") && !strings.Contains(out, "xmlns:xlink") {
		out = strings.Replace(out, "<svg", `<svg xmlns:xlink="http://www.w3.org/1999/xlink"`, 1)
	}
	if strings.Contains(out, "currentColor") {
		notes = append(notes, "it uses currentColor: give it a color (or fill) where you place it, or it renders black")
	}
	if err := wellFormed(out); err != nil {
		return "", notes, fmt.Errorf("the extracted SVG isn't valid XML (%v); read the file and copy the paths verbatim with write_file instead", err)
	}
	return blankLines.ReplaceAllString(out, "\n") + "\n", notes, nil
}

var (
	tagRe      = regexp.MustCompile(`<[a-zA-Z][^<>]*>`)
	camelAttr  = regexp.MustCompile(`\s[a-z]+[A-Z][A-Za-z]*=`)
	classAttr  = regexp.MustCompile(`\s(className|class)="[^"]*"`)
	blankLines = regexp.MustCompile(`\n\s*\n`)
	numberRe   = regexp.MustCompile(`^-?[0-9.]+$`)
)

// SVG attributes that really are camelCase.
var svgCamel = map[string]bool{}

func init() {
	for _, n := range strings.Fields(`viewBox preserveAspectRatio gradientUnits gradientTransform patternUnits
		patternContentUnits patternTransform clipPathUnits maskUnits maskContentUnits stdDeviation baseFrequency
		numOctaves filterUnits primitiveUnits markerWidth markerHeight markerUnits refX refY pathLength spreadMethod
		startOffset textLength lengthAdjust tableValues kernelMatrix xChannelSelector yChannelSelector
		diffuseConstant specularExponent surfaceScale attributeName repeatCount keyTimes keySplines calcMode`) {
		svgCamel[n] = true
	}
}

func kebab(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('-')
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// svgEnd returns the index just past the </svg> closing the <svg> at start.
func svgEnd(src string, start int) (int, error) {
	depth := 0
	for i := start; i < len(src); {
		switch {
		case strings.HasPrefix(src[i:], "<svg"):
			depth++
			i += 4
		case strings.HasPrefix(src[i:], "</svg>"):
			depth--
			i += 6
			if depth == 0 {
				return i, nil
			}
		default:
			i++
		}
	}
	return 0, fmt.Errorf("the <svg> is never closed")
}

// stripExpressions resolves or removes every {…} in a JSX fragment.
func stripExpressions(s string) (string, []string) {
	var b strings.Builder
	var notes []string
	for i := 0; i < len(s); {
		if s[i] != '{' {
			b.WriteByte(s[i])
			i++
			continue
		}
		j := matchBrace(s, i)
		expr := strings.TrimSpace(s[i+1 : j])
		i = j + 1
		cur := b.String()
		if strings.HasSuffix(cur, "=") { // attribute value
			if v, ok := literal(expr); ok {
				b.WriteString(`"` + v + `"`)
				continue
			}
			// Drop the whole attribute: name= already written.
			k := strings.LastIndexAny(cur[:len(cur)-1], " \t\n")
			name := cur[k+1 : len(cur)-1]
			b.Reset()
			b.WriteString(cur[:max(k, 0)])
			notes = append(notes, fmt.Sprintf("dropped dynamic attribute %s={%s}; set it by hand if the logo needs it", name, clip(strings.Join(strings.Fields(expr), " "), 60)))
			continue
		}
		switch {
		case strings.HasPrefix(expr, "/*") && strings.HasSuffix(expr, "*/"):
		case strings.HasPrefix(expr, "..."):
			notes = append(notes, "dropped spread {"+clip(strings.Join(strings.Fields(expr), " "), 60)+"} (usually aria/role props; fine for a logo file)")
		default:
			if v, ok := literal(expr); ok { // {"text"} as a child
				b.WriteString(xmlEscape(v))
			} else {
				notes = append(notes, "dropped dynamic content {"+clip(expr, 60)+"}")
			}
		}
	}
	return b.String(), notes
}

// matchBrace returns the index of the } closing the { at i, skipping strings.
func matchBrace(s string, i int) int {
	depth := 0
	for j := i; j < len(s); j++ {
		switch c := s[j]; c {
		case '"', '\'', '`':
			for j++; j < len(s) && s[j] != c; j++ {
				if s[j] == '\\' {
					j++
				}
			}
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return len(s) - 1
}

func literal(expr string) (string, bool) {
	if numberRe.MatchString(expr) {
		return expr, true
	}
	if len(expr) >= 2 {
		q := expr[0]
		if (q == '"' || q == '\'' || q == '`') && expr[len(expr)-1] == q && !strings.Contains(expr, "${") {
			return strings.ReplaceAll(expr[1:len(expr)-1], `"`, "&quot;"), true
		}
	}
	return "", false
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func wellFormed(s string) error {
	d := xml.NewDecoder(strings.NewReader(s))
	for {
		_, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
