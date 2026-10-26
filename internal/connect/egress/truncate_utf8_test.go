package egress

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateKeepsValidUTF8(t *testing.T) {
	for _, unit := range []string{"a", "é", "世", "😀"} {
		in := "x" + strings.Repeat(unit, 400)
		got := truncate(in)
		if !utf8.ValidString(got) {
			t.Errorf("truncate(%q x400) produced invalid UTF-8", unit)
		}
		if len(got) > 300 {
			t.Errorf("truncate(%q x400) len = %d, want <= 300", unit, len(got))
		}
	}
}
