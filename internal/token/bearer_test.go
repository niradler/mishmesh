package token

import "testing"

func TestBearer(t *testing.T) {
	cases := []struct {
		header string
		want   string
	}{
		{"Bearer abc", "abc"},
		{"bearer abc", "abc"},
		{"  Bearer   abc  ", "abc"},
		{"Basic abc", ""},
		{"Bearer", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := Bearer(tc.header); got != tc.want {
			t.Errorf("Bearer(%q) = %q, want %q", tc.header, got, tc.want)
		}
	}
}
