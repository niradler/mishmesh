package tunnel

import (
	"bytes"
	"io"
	"testing"
)

func TestCopyPooledCopiesAllBytes(t *testing.T) {
	for _, size := range []int{0, 1, spliceBufSize - 1, spliceBufSize, spliceBufSize + 1, 5 * spliceBufSize} {
		payload := bytes.Repeat([]byte{0xab}, size)
		var dst bytes.Buffer
		n, err := copyPooled(&dst, bytes.NewReader(payload))
		if err != nil || n != int64(size) || !bytes.Equal(dst.Bytes(), payload) {
			t.Fatalf("size %d: n=%d err=%v equal=%v", size, n, err, bytes.Equal(dst.Bytes(), payload))
		}
	}
}

func BenchmarkCopyPooled(b *testing.B) {
	payload := bytes.Repeat([]byte{1}, 1<<20)
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	for i := 0; i < b.N; i++ {
		_, _ = copyPooled(io.Discard, bytes.NewReader(payload))
	}
}
