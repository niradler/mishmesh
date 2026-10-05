package agent

import (
	"testing"
	"time"
)

func TestJitteredStaysWithinHalfToFull(t *testing.T) {
	for _, d := range []time.Duration{time.Second, 4 * time.Second, maxBackoff} {
		for i := 0; i < 500; i++ {
			got := jittered(d)
			if got < d/2 || got > d {
				t.Fatalf("jittered(%v) = %v, want within [%v, %v]", d, got, d/2, d)
			}
		}
	}
}
