package mcpx

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClipKeepsUTF8(t *testing.T) {
	s := strings.Repeat("é", 10) // 2 bytes each
	for n := 1; n < len(s); n++ {
		if c := Clip(s, n); !utf8.ValidString(c) {
			t.Fatalf("Clip(%d) produced invalid UTF-8: %q", n, c)
		}
	}
}
