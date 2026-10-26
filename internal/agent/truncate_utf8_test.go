package agent

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateLogKeepsValidUTF8(t *testing.T) {
	for _, unit := range []string{"a", "é", "世", "😀"} {
		in := "x" + strings.Repeat(unit, 400)
		got := truncateLog(in)
		if !utf8.ValidString(got) {
			t.Errorf("truncateLog(%q x400) produced invalid UTF-8", unit)
		}
		if len(got) > 256 {
			t.Errorf("truncateLog(%q x400) len = %d, want <= 256", unit, len(got))
		}
	}
}
