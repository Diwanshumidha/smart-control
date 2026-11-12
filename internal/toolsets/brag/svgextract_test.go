package brag

import (
	"strings"
	"testing"
)

const brandMarkTSX = `import { cn } from "@/lib/utils";

export function BrandMark({ showName = true, className }: { showName?: boolean; className?: string }) {
  return (
    <span className={cn("flex items-center gap-2", className)}>
      {/* the mark keeps its own colours */}
      <svg
        viewBox="0 0 176.3 225.8"
        className="h-8 w-auto shrink-0"
        {...(showName
          ? { "aria-hidden": true }
          : { role: "img", "aria-label": "Acme" })}
      >
        <defs>
          <linearGradient id="g" gradientUnits="userSpaceOnUse"><stop offset={0} stopColor="#008fe5" /></linearGradient>
        </defs>
        <g transform="matrix(.8 0 0 .8 -10 -30)" strokeWidth={2} fillRule="evenodd">
          {/* outer trace */}
          <path fill="#008fe5" d="M244.5 364.9c10.2.7 10.1 1.7 28.2 12.2z" />
          <path fill={color} d="M238.5 639.1z" />
        </g>
      </svg>
      {showName && <span>Acme</span>}
    </span>
  );
}`

func TestExtractSVG(t *testing.T) {
	svg, notes, err := extractSVG(brandMarkTSX)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<svg xmlns="http://www.w3.org/2000/svg"`, `viewBox="0 0 176.3 225.8"`, `gradientUnits="userSpaceOnUse"`,
		`offset="0"`, `stop-color="#008fe5"`, `stroke-width="2"`, `fill-rule="evenodd"`,
		`d="M244.5 364.9c10.2.7 10.1 1.7 28.2 12.2z"`, `<path d="M238.5 639.1z" />`,
	} {
		if !strings.Contains(svg, want) {
			t.Errorf("missing %s in\n%s", want, svg)
		}
	}
	for _, gone := range []string{"className", "outer trace", "showName", "{"} {
		if strings.Contains(svg, gone) {
			t.Errorf("%q should be gone:\n%s", gone, svg)
		}
	}
	if len(notes) != 2 { // the spread and fill={color}
		t.Errorf("notes = %q", notes)
	}
}

func TestExtractSVGNone(t *testing.T) {
	if _, _, err := extractSVG("export const x = 1"); err == nil {
		t.Fatal("want an error without <svg>")
	}
}
