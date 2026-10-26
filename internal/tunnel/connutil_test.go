package tunnel

import (
	"net"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestTruncateUTF8(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"short", "abc", 10, "abc"},
		{"exact", "abc", 3, "abc"},
		{"ascii cut", "abcdef", 3, "abc"},
		{"cut inside two-byte rune", "xéé", 2, "x"},
		{"cut on rune boundary", "xéé", 3, "xé"},
		{"cut inside four-byte rune", "x😀", 3, "x"},
		{"empty", "", 5, ""},
		{"zero budget", "abc", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateUTF8(tt.in, tt.max)
			if got != tt.want {
				t.Errorf("TruncateUTF8(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("invalid UTF-8 %q", got)
			}
		})
	}
	long := "x" + strings.Repeat("世", 500)
	if got := TruncateUTF8(long, 256); len(got) > 256 || !utf8.ValidString(got) {
		t.Errorf("long input: len=%d valid=%v", len(got), utf8.ValidString(got))
	}
}

func TestWriteWithDeadlineClearsDeadline(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() {
		buf := make([]byte, 1)
		_, _ = b.Read(buf)
	}()
	if err := WriteWithDeadline(a, time.Second, func() error {
		_, err := a.Write([]byte{1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	go func() {
		buf := make([]byte, 1)
		_, _ = b.Read(buf)
	}()
	if _, err := a.Write([]byte{2}); err != nil {
		t.Fatalf("deadline leaked past the helper: %v", err)
	}
}

func TestReadWithDeadlineTimesOut(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	_, err := ReadWithDeadline(a, 20*time.Millisecond, func() (int, error) {
		return a.Read(make([]byte, 1))
	})
	if err == nil {
		t.Fatal("expected a timeout")
	}
}
